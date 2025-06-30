package postgres

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestDefaultName(t *testing.T) {
	cases := map[string]string{
		"Task-1":  "cdc_task_1",
		"abc_DEF": "cdc_abc_def",
	}
	for in, want := range cases {
		if got := defaultName(in); got != want {
			t.Errorf("defaultName(%q) = %q, want %q", in, got, want)
		}
	}
	long := defaultName("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if len(long) != 63 {
		t.Errorf("long name has %d bytes, want 63", len(long))
	}
}

func TestPosition(t *testing.T) {
	lsn, err := ParsePosition("16/B374D848")
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatPosition(lsn); got != model.Position("16/B374D848") {
		t.Errorf("round trip = %q", got)
	}
	if _, err := ParsePosition("nope"); err == nil {
		t.Error("want error for invalid position")
	}
}

func TestDecimalValue(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"12.50", 12.5},
		{"100", 100.0},
		{"-0.001", -0.001},
		{"0.1", 0.1},
		{"123456789012345678901234567890", "123456789012345678901234567890"},
		{"1.00000000000000000001", "1.00000000000000000001"},
		{"NaN", "NaN"},
	}
	for _, c := range cases {
		if got := decimalValue(c.in); got != c.want {
			t.Errorf("decimalValue(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
}

func TestDecodeText(t *testing.T) {
	m := pgtype.NewMap()
	ts := time.Date(2024, 1, 2, 1, 4, 5, 0, time.UTC)
	cases := []struct {
		oid  uint32
		in   string
		want any
	}{
		{pgtype.BoolOID, "t", true},
		{pgtype.Int4OID, "42", int64(42)},
		{pgtype.Float8OID, "1.5", 1.5},
		{pgtype.Float8OID, "Infinity", "Infinity"},
		{pgtype.TextOID, "hi", "hi"},
		{pgtype.UUIDOID, "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11", "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"},
		{pgtype.NumericOID, "3.25", 3.25},
		{pgtype.TimestamptzOID, "2024-01-02 03:04:05+02", ts},
		{999999, "custom", "custom"},
	}
	for _, c := range cases {
		got, err := decodeText(m, c.oid, []byte(c.in))
		if err != nil {
			t.Fatalf("decode %q: %v", c.in, err)
		}
		if tt, ok := c.want.(time.Time); ok {
			if g, ok := got.(time.Time); !ok || !g.Equal(tt) || g.Location() != time.UTC {
				t.Errorf("decode %q = %#v, want %v", c.in, got, tt)
			}
			continue
		}
		if got != c.want {
			t.Errorf("decode %q = %#v, want %#v", c.in, got, c.want)
		}
	}
	if v, _ := decodeText(m, pgtype.TextOID, nil); v != nil {
		t.Errorf("null = %#v", v)
	}
	j, err := decodeText(m, pgtype.JSONBOID, []byte(`{"n":9007199254740993,"l":[1.5]}`))
	if err != nil {
		t.Fatal(err)
	}
	doc := j.(map[string]any)
	if doc["n"] != int64(9007199254740993) || doc["l"].([]any)[0] != 1.5 {
		t.Errorf("json = %#v", j)
	}
	arr, err := decodeText(m, pgtype.NumericArrayOID, []byte(`{1.5,NULL}`))
	if err != nil {
		t.Fatal(err)
	}
	if a := arr.([]any); a[0] != 1.5 || a[1] != nil {
		t.Errorf("numeric[] = %#v", arr)
	}
	b, err := decodeText(m, pgtype.ByteaOID, []byte(`\x0102`))
	if err != nil || string(b.([]byte)) != "\x01\x02" {
		t.Errorf("bytea = %#v, %v", b, err)
	}
}

func TestKeyChange(t *testing.T) {
	st := &stream{src: &Source{pk: []string{"id"}}}
	if key, ok := st.keyChange(map[string]any{"id": int64(1)}, map[string]any{"id": int64(2), "n": "x"}); !ok || key["id"] != int64(1) || len(key) != 1 {
		t.Errorf("keyChange = %v %v", key, ok)
	}
	if _, ok := st.keyChange(map[string]any{"id": int64(1), "n": "a"}, map[string]any{"id": int64(1), "n": "b"}); ok {
		t.Error("same key must not count as a change")
	}
}
