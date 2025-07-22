package sqlpoll

import (
	"reflect"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestQuote(t *testing.T) {
	pg, _ := dialectFor("pgx")
	my, _ := dialectFor("mysql")
	if got := pg.table("s", `we"ird`); got != `"s"."we""ird"` {
		t.Fatalf("pg table: %s", got)
	}
	if got := my.quote("we`ird"); got != "`we``ird`" {
		t.Fatalf("mysql quote: %s", got)
	}
}

func TestPollQuery(t *testing.T) {
	pg, _ := dialectFor("postgres")
	q, args := pg.pollQuery(`"t"`, "v", []string{"a", "b"}, position{Cursor: int64(5), PK: []any{int64(1), "x"}}, 1000)
	want := `SELECT * FROM "t" WHERE ("v" > $1) OR ("v" = $2 AND "a" > $3) OR ("v" = $4 AND "a" = $5 AND "b" > $6) ORDER BY "v", "a", "b" LIMIT 1000`
	if q != want {
		t.Fatalf("query:\n%s\nwant:\n%s", q, want)
	}
	if !reflect.DeepEqual(args, []any{int64(5), int64(5), int64(1), int64(5), int64(1), "x"}) {
		t.Fatalf("args: %v", args)
	}

	my, _ := dialectFor("mysql")
	q, args = my.pollQuery("`t`", "v", []string{"id"}, position{Cursor: int64(5), PK: []any{int64(1)}}, 10)
	want = "SELECT * FROM `t` WHERE (`v` > ?) OR (`v` = ? AND `id` > ?) ORDER BY `v`, `id` LIMIT 10"
	if q != want || len(args) != 3 {
		t.Fatalf("query: %s %v", q, args)
	}

	q, args = my.pollQuery("`t`", "v", []string{"id"}, position{}, 10)
	if q != "SELECT * FROM `t` WHERE `v` IS NOT NULL ORDER BY `v`, `id` LIMIT 10" || len(args) != 0 {
		t.Fatalf("from start: %s %v", q, args)
	}

	q, args = my.pollQuery("`t`", "v", []string{"id"}, position{Cursor: int64(3)}, 10)
	if q != "SELECT * FROM `t` WHERE `v` > ? ORDER BY `v`, `id` LIMIT 10" || len(args) != 1 {
		t.Fatalf("cursor only: %s %v", q, args)
	}
}

func TestSnapshotQuery(t *testing.T) {
	sq, _ := dialectFor("sqlite")
	q, args := sq.snapshotQuery(`"t"`, []string{"id"}, nil, 100)
	if q != `SELECT * FROM "t" ORDER BY "id" LIMIT 100` || args != nil {
		t.Fatalf("first page: %s", q)
	}
	q, args = sq.snapshotQuery(`"t"`, []string{"id"}, []any{int64(7)}, 100)
	if q != `SELECT * FROM "t" WHERE ("id" > ?) ORDER BY "id" LIMIT 100` || len(args) != 1 {
		t.Fatalf("next page: %s", q)
	}
}

func TestLatestQuery(t *testing.T) {
	pg, _ := dialectFor("pgx")
	got := pg.latestQuery(`"t"`, "updated_at", []string{"id"})
	want := `SELECT "updated_at", "id" FROM "t" WHERE "updated_at" IS NOT NULL ORDER BY "updated_at" DESC, "id" DESC LIMIT 1`
	if got != want {
		t.Fatalf("%s", got)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	ts := time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
	p := encodePosition(position{Cursor: ts, PK: []any{int64(42), "k", []byte("b"), uint64(1 << 63)}})
	want := `{"cursor":"2026-01-02T03:04:05.123456789Z","pk":[42,"k","b",9223372036854775808]}`
	if string(p) != want {
		t.Fatalf("encode: %s", p)
	}
	got, err := decodePosition(p, func(i int) bool { return i == 0 })
	if err != nil {
		t.Fatal(err)
	}
	if !got.Cursor.(time.Time).Equal(ts) {
		t.Fatalf("cursor: %v", got.Cursor)
	}
	if !reflect.DeepEqual(got.PK, []any{int64(42), "k", "b", uint64(1 << 63)}) {
		t.Fatalf("pk: %#v", got.PK)
	}

	got, err = decodePosition(`{"cursor":1.5,"pk":null}`, func(int) bool { return false })
	if err != nil || got.Cursor != 1.5 || got.PK != nil {
		t.Fatalf("float cursor: %#v %v", got, err)
	}
	if _, err := decodePosition(model.Position("nope"), func(int) bool { return false }); err == nil {
		t.Fatal("want error")
	}
}
