package redisstream

import (
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func TestParseEntry(t *testing.T) {
	cases := []struct {
		name   string
		values map[string]any
		op     model.Op
		key    string
		row    map[string]any
	}{
		{"data", map[string]any{"op": "upsert", "id": "4", "data": `{"name":"a","price":9.5,"n":2,"tags":[1]}`},
			model.OpUpsert, "4", map[string]any{"id": "4", "name": "a", "price": 9.5, "n": int64(2), "tags": []any{int64(1)}}},
		{"data with own id", map[string]any{"id": "4", "data": `{"id":4,"name":"a"}`},
			model.OpUpsert, "4", map[string]any{"id": int64(4), "name": "a"}},
		{"fields", map[string]any{"id": "7", "name": "b", "price": "3"},
			model.OpUpsert, "7", map[string]any{"id": "7", "name": "b", "price": "3"}},
		{"delete", map[string]any{"op": "DELETE", "id": "7", "name": "ignored"},
			model.OpDelete, "7", map[string]any{"id": "7"}},
		{"truncate", map[string]any{"op": "truncate"}, model.OpTruncate, "", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch, err := parseEntry(redis.XMessage{ID: "1-0", Values: c.values})
			if err != nil {
				t.Fatal(err)
			}
			if ch.op != c.op || ch.key != c.key || len(ch.row) != len(c.row) {
				t.Fatalf("got %+v", ch)
			}
			for k, v := range c.row {
				if _, isSlice := v.([]any); isSlice {
					continue
				}
				if ch.row[k] != v {
					t.Fatalf("row[%s] = %#v, want %#v", k, ch.row[k], v)
				}
			}
		})
	}
	for _, bad := range []map[string]any{
		{"op": "merge", "id": "1"},
		{"name": "no id"},
		{"id": "1", "data": "[1]"},
		{"id": "1", "data": "{"},
	} {
		if _, err := parseEntry(redis.XMessage{ID: "1-0", Values: bad}); !source.IsPermanent(err) {
			t.Errorf("parseEntry(%v) = %v, want permanent error", bad, err)
		}
	}
}

func TestParseID(t *testing.T) {
	id, err := parseID("1700000000000-3")
	if err != nil || id.ms != 1700000000000 || id.seq != 3 || id.String() != "1700000000000-3" {
		t.Fatalf("got %+v %v", id, err)
	}
	if z, err := parseID("0"); err != nil || !z.isZero() {
		t.Fatalf("0 = %+v %v", z, err)
	}
	if !(streamID{1, 5}).less(streamID{2, 0}) || (streamID{2, 0}).less(streamID{1, 9}) || !(streamID{2, 0}).less(streamID{2, 1}) {
		t.Fatal("less is wrong")
	}
	for _, bad := range []string{"", "x", "1-x", "-1"} {
		if _, err := parseID(bad); err == nil {
			t.Errorf("parseID(%q) succeeded", bad)
		}
	}
}

func TestPositionLost(t *testing.T) {
	// added is entries-added (0 on servers older than 7).
	mk := func(last, maxDel, first string, n, added int64) *redis.XInfoStream {
		return &redis.XInfoStream{LastGeneratedID: last, MaxDeletedEntryID: maxDel, FirstEntry: redis.XMessage{ID: first}, Length: n, EntriesAdded: added}
	}
	cases := []struct {
		name string
		pos  string
		info *redis.XInfoStream
		lost bool
	}{
		{"zero, nothing removed", "0-0", mk("10-0", "0-0", "1-0", 3, 3), false},
		{"zero, trimmed", "0-0", mk("10-0", "0-0", "5-0", 3, 10), true},
		{"zero, xdel", "0-0", mk("10-0", "4-0", "5-0", 3, 4), true},
		{"after trimmed range", "6-0", mk("10-0", "0-0", "5-0", 3, 10), false},
		{"inside trimmed range", "3-0", mk("10-0", "0-0", "5-0", 3, 10), true},
		{"before xdel", "3-0", mk("10-0", "4-0", "1-0", 3, 4), true},
		{"recreated", "20-0", mk("10-0", "0-0", "1-0", 3, 3), true},
		{"empty after trim", "3-0", mk("10-0", "0-0", "", 0, 10), true},
		{"empty, caught up", "10-0", mk("10-0", "0-0", "", 0, 10), false},
		{"old server, before first", "1-0", mk("10-0", "", "5-0", 3, 0), true},
		{"old server, adjacent", "4-0", mk("10-0", "", "4-1", 3, 0), false},
		{"old server, inside", "6-0", mk("10-0", "", "5-0", 3, 0), false},
		{"old server, zero", "0-0", mk("10-0", "", "5-0", 3, 0), false},
	}
	for _, c := range cases {
		pos, _ := parseID(c.pos)
		err := positionLost(pos, c.info)
		if lost := errors.Is(err, source.ErrPositionLost); lost != c.lost {
			t.Errorf("%s: lost = %v (%v), want %v", c.name, lost, err, c.lost)
		}
	}
}

func TestInferColumns(t *testing.T) {
	cols := inferColumns([]map[string]any{
		{"id": "1", "name": "a", "price": 1.5},
		{"id": "2", "name": nil, "price": int64(1)},
	})
	want := map[string]model.Column{
		"id":    {Name: "id", Type: "string", PrimaryKey: true},
		"name":  {Name: "name", Type: "string", Nullable: true},
		"price": {Name: "price", Type: "mixed"},
	}
	if len(cols) != len(want) {
		t.Fatalf("got %+v", cols)
	}
	for _, c := range cols {
		if want[c.Name] != c {
			t.Errorf("column %+v, want %+v", c, want[c.Name])
		}
	}
	if cols := inferColumns(nil); len(cols) != 1 || cols[0].Name != "id" {
		t.Fatalf("empty sample: %+v", cols)
	}
}
