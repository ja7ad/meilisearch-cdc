package mysql

import (
	"encoding/binary"
	"reflect"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestPosition(t *testing.T) {
	p := FormatPosition("mysql-bin.000003", 1234)
	if p != "mysql-bin.000003:1234" {
		t.Fatalf("format = %q", p)
	}
	file, pos, err := ParsePosition(p)
	if err != nil || file != "mysql-bin.000003" || pos != 1234 {
		t.Fatalf("parse = %q %d %v", file, pos, err)
	}
	for _, bad := range []model.Position{"", "nofile", ":12", "f:x", "f:99999999999"} {
		if _, _, err := ParsePosition(bad); err == nil {
			t.Errorf("ParsePosition(%q) want error", bad)
		}
	}
}

func TestServerID(t *testing.T) {
	a, b := serverIDFor("task-a"), serverIDFor("task-b")
	if a == b {
		t.Errorf("different tasks share server id %d", a)
	}
	if a != serverIDFor("task-a") {
		t.Error("server id is not stable")
	}
	for _, id := range []string{"", "x", "task-a", "a-very-long-task-identifier-0123456789"} {
		if v := serverIDFor(id); v < 1000 || v >= 1<<31 {
			t.Errorf("server id %d out of range", v)
		}
	}
}

func TestNewDSN(t *testing.T) {
	s, err := New(model.Source{DSN: "u:p@tcp(db.local:3307)/shop", Table: "items"}, "t1", "mysql")
	if err != nil {
		t.Fatal(err)
	}
	if s.host != "db.local" || s.port != 3307 || s.database != "shop" || s.dsn.User != "u" {
		t.Errorf("parsed %+v", s)
	}
	s, err = New(model.Source{DSN: "u:p@tcp(db)/shop", Database: "other", Table: "items", ServerID: 7}, "t1", "mariadb")
	if err != nil {
		t.Fatal(err)
	}
	if s.port != 3306 || s.database != "other" || s.serverID != 7 {
		t.Errorf("parsed %+v", s)
	}
	if _, err := New(model.Source{DSN: "u:p@tcp(db)/", Table: "items"}, "t1", "mysql"); err == nil {
		t.Error("want error without database")
	}
}

func TestParseEnumValues(t *testing.T) {
	got := parseEnumValues(`enum('a','b''c','d,e')`)
	want := []string{"a", "b'c", "d,e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q", got)
	}
}

func TestDDLTarget(t *testing.T) {
	cases := []struct {
		q, verb, schema, table string
		ok                     bool
	}{
		{"TRUNCATE TABLE items", "TRUNCATE", "db", "items", true},
		{"truncate `shop`.`items`", "TRUNCATE", "shop", "items", true},
		{"ALTER TABLE `it``ems` ADD COLUMN x int", "ALTER", "db", "it`ems", true},
		{"/* gh-ost */ ALTER ONLINE TABLE shop.items DROP x", "ALTER", "shop", "items", true},
		{"DROP TABLE IF EXISTS items", "DROP", "db", "items", true},
		{"INSERT INTO items VALUES (1)", "", "", "", false},
	}
	for _, c := range cases {
		verb, schema, table, ok := ddlTarget(c.q, "db")
		if ok != c.ok || verb != c.verb || schema != c.schema || table != c.table {
			t.Errorf("ddlTarget(%q) = %q %q %q %v", c.q, verb, schema, table, ok)
		}
	}
}

func TestConvert(t *testing.T) {
	intU := newColumn("a", "int", "int unsigned", false, false, false)
	tinyU := newColumn("a", "tinyint", "tinyint unsigned", false, false, false)
	medU := newColumn("a", "mediumint", "mediumint unsigned", false, false, false)
	bigU := newColumn("a", "bigint", "bigint unsigned", false, false, false)
	intS := newColumn("a", "int", "int", false, false, false)
	enum := newColumn("a", "enum", "enum('red','green')", false, false, false)
	set := newColumn("a", "set", "set('x','y','z')", false, false, false)
	dec := newColumn("a", "decimal", "decimal(10,2)", false, false, false)
	js := newColumn("a", "json", "json", false, false, false)
	mariaJSON := newColumn("a", "longtext", "longtext", false, false, true)
	text := newColumn("a", "text", "text", false, false, false)
	bin := newColumn("a", "varbinary", "varbinary(4)", false, false, false)
	dt := newColumn("a", "datetime", "datetime(6)", false, false, false)
	date := newColumn("a", "date", "date", false, false, false)
	flt := newColumn("a", "float", "float", false, false, false)
	bit := newColumn("a", "bit", "bit(16)", false, false, false)
	bitBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bitBytes, 0x0102)

	cases := []struct {
		name   string
		c      column
		in     any
		binlog bool
		want   any
	}{
		{"int unsigned from signed binlog", intU, int32(-1), true, int64(4294967295)},
		{"int unsigned from unsigned binlog", intU, uint32(4294967295), true, int64(4294967295)},
		{"tinyint unsigned", tinyU, int8(-1), true, int64(255)},
		{"mediumint unsigned", medU, int32(-1), true, int64(16777215)},
		{"bigint unsigned max", bigU, int64(-1), true, uint64(18446744073709551615)},
		{"bigint unsigned sql", bigU, uint64(18446744073709551615), false, uint64(18446744073709551615)},
		{"bigint unsigned text", bigU, []byte("18446744073709551615"), false, uint64(18446744073709551615)},
		{"int signed", intS, int32(-5), true, int64(-5)},
		{"int text", intS, []byte("-5"), false, int64(-5)},
		{"enum binlog", enum, int64(2), true, "green"},
		{"enum sql", enum, []byte("green"), false, "green"},
		{"set binlog", set, int64(5), true, "x,z"},
		{"set sql", set, []byte("x,z"), false, "x,z"},
		{"decimal", dec, "12.50", true, 12.5},
		{"decimal huge", dec, []byte("123456789012345678901.5"), false, "123456789012345678901.5"},
		{"text binlog blob", text, []byte("hello"), true, "hello"},
		{"binary", bin, "ab", true, []byte("ab")},
		{"float", flt, float32(1.1), true, 1.1},
		{"bit binlog", bit, int64(0x0102), true, int64(0x0102)},
		{"bit sql", bit, bitBytes[6:], false, int64(0x0102)},
		{"zero datetime", dt, "0000-00-00 00:00:00", true, nil},
		{"nil", text, nil, true, nil},
	}
	for _, c := range cases {
		got, err := convert(c.c, c.in, c.binlog)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %#v, want %#v", c.name, got, c.want)
		}
	}

	for _, c := range []column{js, mariaJSON} {
		v, err := convert(c, []byte(`{"n":9007199254740993,"a":[1.5,"x"]}`), true)
		if err != nil {
			t.Fatal(err)
		}
		m := v.(map[string]any)
		if m["n"] != int64(9007199254740993) || m["a"].([]any)[0] != 1.5 {
			t.Errorf("json = %#v", v)
		}
	}

	local := time.FixedZone("x", 3600)
	v, _ := convert(dt, time.Date(2024, 1, 2, 4, 0, 0, 0, local), true)
	if tm := v.(time.Time); tm.Location() != time.UTC || tm.Hour() != 3 {
		t.Errorf("datetime = %v", v)
	}
	v, _ = convert(date, "2024-05-06", true)
	if !v.(time.Time).Equal(time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("date = %v", v)
	}
}

func TestKeyChange(t *testing.T) {
	key, changed := keyChange([]string{"id"}, map[string]any{"id": int64(1), "n": "a"}, map[string]any{"id": int64(2), "n": "a"})
	if !changed || !reflect.DeepEqual(key, map[string]any{"id": int64(1)}) {
		t.Errorf("got %v %v", key, changed)
	}
	if _, changed := keyChange([]string{"id"}, map[string]any{"id": int64(1)}, map[string]any{"id": int64(1)}); changed {
		t.Error("unchanged key reported as changed")
	}
}
