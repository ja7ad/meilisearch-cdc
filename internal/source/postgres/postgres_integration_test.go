//go:build integration

package postgres

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func itoa(i int) string               { return strconv.Itoa(i) }
func errorsIs(err, target error) bool { return errors.Is(err, target) }

func startPostgres(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("cdc"),
		tcpostgres.WithUsername("cdc"),
		tcpostgres.WithPassword("cdc"),
		testcontainers.WithCmdArgs("-c", "wal_level=logical"),
		tcpostgres.BasicWaitStrategies(),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func execSQL(t *testing.T, dsn, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestConformance(t *testing.T) {
	dsn := startPostgres(t)
	execSQL(t, dsn, "CREATE TABLE items (id int PRIMARY KEY, name text, price numeric)")
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(model.Source{Type: model.SourcePostgres, DSN: dsn, Table: "items"}, "Conformance-1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			execSQL(t, dsn, "INSERT INTO items VALUES ($1, $2, $3)", id, name, price)
		},
		Update: func(t *testing.T, id int, name string) {
			execSQL(t, dsn, "UPDATE items SET name = $2 WHERE id = $1", id, name)
		},
		Delete: func(t *testing.T, id int) {
			execSQL(t, dsn, "DELETE FROM items WHERE id = $1", id)
		},
	})
}

type collector struct {
	mu  sync.Mutex
	evs []model.Event
}

func (c *collector) emit(_ context.Context, evs []model.Event) error {
	c.mu.Lock()
	c.evs = append(c.evs, evs...)
	c.mu.Unlock()
	return nil
}

func (c *collector) wait(t *testing.T, n int) []model.Event {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		if len(c.evs) >= n {
			out := append([]model.Event(nil), c.evs...)
			c.mu.Unlock()
			return out
		}
		c.mu.Unlock()
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d events", n)
	return nil
}

func TestTypesAndKeyChange(t *testing.T) {
	dsn := startPostgres(t)
	execSQL(t, dsn, `CREATE TABLE typed (
		id int PRIMARY KEY,
		doc jsonb, arr int[], ts timestamptz, d date, price numeric(10,2), big numeric,
		uid uuid, title varchar(20), blob bytea, flag bool, f4 real)`)
	const insert = `INSERT INTO typed VALUES (1, '{"a":1,"b":[true,"x"],"c":1.5}', '{1,2}',
		'2024-05-06 07:08:09.5+02', '2024-05-06', 12.50, 123456789012345678901234567890.123,
		'a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11', 'hello', '\xdeadbeef', true, 1.1)`
	execSQL(t, dsn, insert)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := New(model.Source{DSN: dsn, Table: "typed"}, "types")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	defer src.(source.Dropper).Drop(context.Background())
	start, err := src.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := &collector{}
	if err := src.Snapshot(ctx, snap.emit); err != nil {
		t.Fatal(err)
	}
	checkRow := func(where string, row map[string]any) {
		t.Helper()
		doc, ok := row["doc"].(map[string]any)
		if !ok || doc["a"] != int64(1) || doc["c"] != 1.5 {
			t.Errorf("%s: doc = %#v", where, row["doc"])
		}
		if arr, ok := row["arr"].([]any); !ok || len(arr) != 2 || arr[0] != int64(1) {
			t.Errorf("%s: arr = %#v", where, row["arr"])
		}
		want := time.Date(2024, 5, 6, 5, 8, 9, 500_000_000, time.UTC)
		if ts, ok := row["ts"].(time.Time); !ok || !ts.Equal(want) {
			t.Errorf("%s: ts = %#v", where, row["ts"])
		}
		if d, ok := row["d"].(time.Time); !ok || !d.Equal(time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC)) {
			t.Errorf("%s: d = %#v", where, row["d"])
		}
		if row["price"] != 12.5 {
			t.Errorf("%s: price = %#v", where, row["price"])
		}
		if row["big"] != "123456789012345678901234567890.123" {
			t.Errorf("%s: big = %#v", where, row["big"])
		}
		if row["uid"] != "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11" {
			t.Errorf("%s: uid = %#v", where, row["uid"])
		}
		if row["title"] != "hello" {
			t.Errorf("%s: title = %#v", where, row["title"])
		}
		if b, ok := row["blob"].([]byte); !ok || string(b) != "\xde\xad\xbe\xef" {
			t.Errorf("%s: blob = %#v", where, row["blob"])
		}
		if row["flag"] != true || row["f4"] != 1.1 || row["id"] != int64(1) {
			t.Errorf("%s: flag/f4/id = %#v %#v %#v", where, row["flag"], row["f4"], row["id"])
		}
	}
	rows := snap.wait(t, 1)
	checkRow("snapshot", rows[0].Row)

	stream := &collector{}
	go func() { _ = src.Stream(ctx, start, stream.emit) }()
	execSQL(t, dsn, "UPDATE typed SET id = 2 WHERE id = 1")
	execSQL(t, dsn, "DELETE FROM typed")
	execSQL(t, dsn, insert)
	execSQL(t, dsn, "TRUNCATE typed")

	evs := stream.wait(t, 5)
	if evs[0].Op != model.OpDelete || evs[0].Row["id"] != int64(1) || len(evs[0].Row) != 1 {
		t.Errorf("pk change: want delete of old key first, got %+v", evs[0])
	}
	if evs[1].Op != model.OpUpsert || evs[1].Row["id"] != int64(2) {
		t.Errorf("pk change: want upsert of new key, got %+v", evs[1])
	}
	if evs[0].Position != evs[1].Position || evs[0].Position == "" || evs[0].Time.IsZero() {
		t.Errorf("events of one transaction must share the commit position and time: %+v %+v", evs[0], evs[1])
	}
	if evs[2].Op != model.OpDelete || evs[2].Row["id"] != int64(2) {
		t.Errorf("delete: got %+v", evs[2])
	}
	checkRow("stream", evs[3].Row)
	if evs[4].Op != model.OpTruncate {
		t.Errorf("truncate: got %+v", evs[4])
	}
	p1, _ := ParsePosition(evs[1].Position)
	p2, _ := ParsePosition(evs[2].Position)
	if p2 <= p1 {
		t.Errorf("positions must increase in commit order: %s then %s", evs[1].Position, evs[2].Position)
	}
	cancel()
}

func TestPermanentErrors(t *testing.T) {
	dsn := startPostgres(t)
	ctx := context.Background()

	src, _ := New(model.Source{DSN: dsn, Table: "missing"}, "perm")
	if err := src.Open(ctx); !source.IsPermanent(err) {
		t.Errorf("missing table: want permanent error, got %v", err)
	}
	src.Close()

	execSQL(t, dsn, "CREATE TABLE nokey (a int)")
	src, _ = New(model.Source{DSN: dsn, Table: "nokey"}, "perm")
	if err := src.Open(ctx); !source.IsPermanent(err) {
		t.Errorf("no primary key: want permanent error, got %v", err)
	}
	src.Close()

	cfg, _ := pgx.ParseConfig(dsn)
	cfg.Password = "wrong"
	bad := "postgres://cdc:wrong@" + cfg.Host + ":" + itoa(int(cfg.Port)) + "/cdc?sslmode=disable"
	src, _ = New(model.Source{DSN: bad, Table: "nokey"}, "perm")
	if err := src.Open(ctx); !source.IsPermanent(err) {
		t.Errorf("bad password: want permanent error, got %v", err)
	}
	src.Close()

	// A dropped slot is reported as a lost position.
	execSQL(t, dsn, "CREATE TABLE ok (id int PRIMARY KEY)")
	src, _ = New(model.Source{DSN: dsn, Table: "ok"}, "lost")
	if err := src.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	pos, err := src.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := src.(source.Dropper).Drop(ctx); err != nil {
		t.Fatal(err)
	}
	err = src.Stream(ctx, pos, func(context.Context, []model.Event) error { return nil })
	if !errorsIs(err, source.ErrPositionLost) {
		t.Errorf("dropped slot: want ErrPositionLost, got %v", err)
	}
}
