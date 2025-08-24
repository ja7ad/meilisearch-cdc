package kafka

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestParseChangePlain(t *testing.T) {
	c, err := parseChange([]byte(`{"op":"u","ts_ms":1700000000000,"before":{"id":1,"name":"a"},"after":{"id":1,"name":"b","price":9.5,"tags":["x"],"meta":{"k":2}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Op != model.OpUpsert || c.Read || !c.Time.Equal(time.UnixMilli(1700000000000)) {
		t.Fatalf("change: %+v", c)
	}
	want := map[string]any{"id": int64(1), "name": "b", "price": 9.5, "tags": []any{"x"}, "meta": map[string]any{"k": int64(2)}}
	if !reflect.DeepEqual(c.After, want) {
		t.Fatalf("after: %#v", c.After)
	}
}

func TestParseChangeWithSchema(t *testing.T) {
	msg := `{
	 "schema": {"type":"struct","fields":[
	   {"field":"before","type":"struct","fields":[]},
	   {"field":"after","type":"struct","fields":[
	     {"field":"id","type":"int32"},
	     {"field":"created","type":"int64","name":"io.debezium.time.MicroTimestamp"},
	     {"field":"day","type":"int32","name":"io.debezium.time.Date"},
	     {"field":"zoned","type":"string","name":"io.debezium.time.ZonedTimestamp"},
	     {"field":"amount","type":"bytes","name":"org.apache.kafka.connect.data.Decimal","parameters":{"scale":"2"}},
	     {"field":"neg","type":"bytes","name":"org.apache.kafka.connect.data.Decimal","parameters":{"scale":"1"}},
	     {"field":"blob","type":"bytes"}
	   ]}]},
	 "payload": {"op":"r","ts_ms":5,"before":null,"after":{
	   "id":7,"created":1700000000123456,"day":1,"zoned":"2026-01-02T03:04:05Z",
	   "amount":"MDk=","neg":"/w==","blob":"AQI="}}
	}`
	c, err := parseChange([]byte(msg))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Read || c.Op != model.OpUpsert || c.Before != nil {
		t.Fatalf("change: %+v", c)
	}
	want := map[string]any{
		"id":      int64(7),
		"created": time.UnixMicro(1700000000123456).UTC(),
		"day":     time.Unix(86400, 0).UTC(),
		"zoned":   time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
		"amount":  123.45, // 0x3039 = 12345
		"neg":     -0.1,   // 0xff = -1
		"blob":    []byte{1, 2},
	}
	if !reflect.DeepEqual(c.After, want) {
		t.Fatalf("after:\n%#v\nwant\n%#v", c.After, want)
	}
}

func TestParseChangeOps(t *testing.T) {
	c, err := parseChange([]byte(`{"op":"d","before":{"id":3},"after":null}`))
	if err != nil || c.Op != model.OpDelete || c.Before["id"] != int64(3) {
		t.Fatalf("delete: %+v %v", c, err)
	}
	c, err = parseChange([]byte(`{"schema":null,"payload":{"op":"t"}}`))
	if err != nil || c.Op != model.OpTruncate {
		t.Fatalf("truncate: %+v %v", c, err)
	}
	for _, msg := range []string{`{"op":"m"}`, `{"schema":{},"payload":null}`, `{"ts_ms":1}`} {
		if _, err := parseChange([]byte(msg)); !errors.Is(err, errSkip) {
			t.Errorf("%s: want skip, got %v", msg, err)
		}
	}
	if _, err := parseChange([]byte(`not json`)); err == nil || errors.Is(err, errSkip) {
		t.Fatalf("invalid json: %v", err)
	}
	if _, err := parseChange([]byte(`{"op":"c","after":null}`)); err == nil {
		t.Fatal("create without after must fail")
	}
}

func TestToEvent(t *testing.T) {
	ev, err := toEvent(&kgo.Record{Key: []byte(`{"schema":{"type":"struct"},"payload":{"id":4}}`), Value: []byte(`{"op":"d","before":null}`)})
	if err != nil || ev.Op != model.OpDelete || ev.Row["id"] != int64(4) {
		t.Fatalf("delete from key: %+v %v", ev, err)
	}
	ev, err = toEvent(&kgo.Record{Key: []byte(`{"id":4}`)})
	if err != nil || ev != nil {
		t.Fatalf("tombstone: %+v %v", ev, err)
	}
}

func TestFolder(t *testing.T) {
	f := newFolder()
	add := func(key, value string) {
		t.Helper()
		var k []byte
		if key != "" {
			k = []byte(key)
		}
		if err := f.add(&kgo.Record{Key: k, Value: []byte(value)}); err != nil {
			t.Fatal(err)
		}
	}
	add(`{"id":1}`, `{"op":"c","after":{"id":1,"v":"a"}}`)
	add(`{"id":2}`, `{"op":"c","after":{"id":2}}`)
	add(`{"id":1}`, `{"op":"u","after":{"id":1,"v":"b"}}`)
	add(`{"id":2}`, `{"op":"d","before":{"id":2}}`)
	add(`{"id":2}`, ``)
	add(`{"id":3}`, `{"op":"c","after":{"id":3}}`)
	add(`{"id":3}`, `{"op":"d","before":{"id":3}}`)
	add(`{"id":3}`, `{"op":"c","after":{"id":3,"again":true}}`)
	rows := f.rows()
	want := []map[string]any{{"id": int64(1), "v": "b"}, {"id": int64(3), "again": true}}
	if !reflect.DeepEqual(rows, want) {
		t.Fatalf("rows: %#v", rows)
	}
	add(`{"id":9}`, `{"op":"t"}`)
	if rows := f.rows(); len(rows) != 0 {
		t.Fatalf("after truncate: %v", rows)
	}
}

func TestOffsets(t *testing.T) {
	o := offsets{"db.public.items/0": 5, "a/b/12": 7}
	pos := o.encode()
	if string(pos) != `{"a/b/12":7,"db.public.items/0":5}` {
		t.Fatalf("encode: %s", pos)
	}
	back, err := decodeOffsets(pos)
	if err != nil || !reflect.DeepEqual(back, o) {
		t.Fatalf("decode: %v %v", back, err)
	}
	if bt := back.byTopic(); bt["a/b"][12] != 7 || bt["db.public.items"][0] != 5 {
		t.Fatalf("byTopic: %v", bt)
	}
	if empty, err := decodeOffsets(""); err != nil || len(empty) != 0 {
		t.Fatalf("empty: %v %v", empty, err)
	}
	for _, bad := range []string{`{"nopartition":1}`, `{"t/x":1}`, `[`} {
		if _, err := decodeOffsets(model.Position(bad)); err == nil {
			t.Errorf("%s: want error", bad)
		}
	}
}

func TestParseChangePayloadOnlyWrapper(t *testing.T) {
	c, err := parseChange([]byte(`{"payload":{"op":"c","after":{"id":7,"name":"x"}}}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Op != model.OpUpsert || c.After["name"] != "x" {
		t.Fatalf("unexpected change: %+v", c)
	}
}
