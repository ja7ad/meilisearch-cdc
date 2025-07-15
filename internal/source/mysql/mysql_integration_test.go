//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/testcontainers/testcontainers-go"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

var binlogArgs = []string{"--log-bin=mysql-bin", "--binlog-format=ROW", "--binlog-row-image=FULL", "--server-id=1"}

func startMySQL(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcmysql.Run(ctx, "mysql:8.4",
		tcmysql.WithUsername("root"), tcmysql.WithPassword("test"), tcmysql.WithDatabase("cdc"),
		testcontainers.WithCmdArgs(binlogArgs...))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start mysql: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func startMariaDB(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcmariadb.Run(ctx, "mariadb:11",
		tcmariadb.WithUsername("root"), tcmariadb.WithPassword("test"), tcmariadb.WithDatabase("cdc"),
		testcontainers.WithCmdArgs(binlogArgs...))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("start mariadb: %v", err)
	}
	dsn, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func execSQL(t *testing.T, dsn, q string, args ...any) {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func TestMySQL(t *testing.T)   { runFlavor(t, model.SourceMySQL, startMySQL(t)) }
func TestMariaDB(t *testing.T) { runFlavor(t, model.SourceMariaDB, startMariaDB(t)) }

func runFlavor(t *testing.T, typ model.SourceType, dsn string) {
	t.Run("conformance", func(t *testing.T) {
		execSQL(t, dsn, "CREATE TABLE items (id int PRIMARY KEY, name varchar(100), price double)")
		sourcetest.Run(t, sourcetest.Harness{
			NewSource: func(t *testing.T) source.Source {
				s, err := source.New(model.Source{Type: typ, DSN: dsn, Table: "items"}, "conformance")
				if err != nil {
					t.Fatal(err)
				}
				return s
			},
			Insert: func(t *testing.T, id int, name string, price float64) {
				execSQL(t, dsn, "INSERT INTO items VALUES (?, ?, ?)", id, name, price)
			},
			Update: func(t *testing.T, id int, name string) {
				execSQL(t, dsn, "UPDATE items SET name = ? WHERE id = ?", name, id)
			},
			Delete: func(t *testing.T, id int) {
				execSQL(t, dsn, "DELETE FROM items WHERE id = ?", id)
			},
		})
	})
	t.Run("types", func(t *testing.T) { testTypes(t, typ, dsn) })
	t.Run("errors", func(t *testing.T) { testErrors(t, typ, dsn) })
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

func testTypes(t *testing.T, typ model.SourceType, dsn string) {
	execSQL(t, dsn, `CREATE TABLE typed (
		id int unsigned PRIMARY KEY, big bigint unsigned, tiny tinyint unsigned, neg int,
		color enum('red','green'), tags set('x','y','z'), doc json, price decimal(10,2),
		ts timestamp(3) NULL, dt datetime, d date, title varchar(20), body text, raw varbinary(8), f float)`)
	const insert = `INSERT INTO typed VALUES (4000000000, 18446744073709551615, 255, -7, 'green', 'x,z',
		'{"a":1,"b":[true,"s"],"c":1.5}', 12.50, '2024-05-06 07:08:09.250', '2024-05-06 07:08:09', '2024-05-06',
		'hello', 'long text', 0xDEADBEEF, 1.1)`
	execSQL(t, dsn, insert)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	src, err := source.New(model.Source{Type: typ, DSN: dsn, Table: "typed"}, "types")
	if err != nil {
		t.Fatal(err)
	}
	if err := src.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	start, err := src.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snap := &collector{}
	if err := src.Snapshot(ctx, snap.emit); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": int64(4000000000), "big": uint64(18446744073709551615), "tiny": int64(255), "neg": int64(-7),
		"color": "green", "tags": "x,z",
		"doc":   map[string]any{"a": int64(1), "b": []any{true, "s"}, "c": 1.5},
		"price": 12.5,
		"ts":    time.Date(2024, 5, 6, 7, 8, 9, 250_000_000, time.UTC),
		"dt":    time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC),
		"d":     time.Date(2024, 5, 6, 0, 0, 0, 0, time.UTC),
		"title": "hello", "body": "long text", "raw": []byte{0xDE, 0xAD, 0xBE, 0xEF}, "f": 1.1,
	}
	check := func(where string, row map[string]any) {
		t.Helper()
		for k, w := range want {
			if !reflect.DeepEqual(row[k], w) {
				t.Errorf("%s: %s = %#v (%T), want %#v", where, k, row[k], row[k], w)
			}
		}
	}
	check("snapshot", snap.wait(t, 1)[0].Row)

	stream := &collector{}
	done := make(chan error, 1)
	go func() { done <- src.Stream(ctx, start, stream.emit) }()
	execSQL(t, dsn, "UPDATE typed SET id = 5 WHERE id = 4000000000")
	execSQL(t, dsn, "DELETE FROM typed")
	execSQL(t, dsn, insert)
	execSQL(t, dsn, "TRUNCATE TABLE typed")

	evs := stream.wait(t, 5)
	if evs[0].Op != model.OpDelete || evs[0].Row["id"] != int64(4000000000) || len(evs[0].Row) != 1 {
		t.Errorf("pk change: want delete of old key first, got %+v", evs[0])
	}
	if evs[1].Op != model.OpUpsert || evs[1].Row["id"] != int64(5) {
		t.Errorf("pk change: want upsert of new key, got %+v", evs[1])
	}
	if evs[0].Position != evs[1].Position || evs[0].Time.IsZero() {
		t.Errorf("events of one transaction must share position and time: %+v / %+v", evs[0], evs[1])
	}
	if evs[2].Op != model.OpDelete || evs[2].Row["id"] != int64(5) {
		t.Errorf("delete: got %+v", evs[2])
	}
	check("stream", evs[3].Row)
	if evs[4].Op != model.OpTruncate {
		t.Errorf("truncate: got %+v", evs[4])
	}
	for _, e := range evs {
		if !strings.HasPrefix(string(e.Position), "mysql-bin.") {
			t.Errorf("unexpected position %q", e.Position)
		}
	}
	_, p1, _ := ParsePosition(evs[1].Position)
	_, p2, _ := ParsePosition(evs[2].Position)
	if p2 <= p1 {
		t.Errorf("positions must increase: %s then %s", evs[1].Position, evs[2].Position)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("stream did not stop after cancel")
	}
}

func testErrors(t *testing.T, typ model.SourceType, dsn string) {
	ctx := context.Background()
	open := func(table, dsn string) error {
		s, err := source.New(model.Source{Type: typ, DSN: dsn, Table: table}, "errors")
		if err != nil {
			return err
		}
		defer s.Close()
		return s.Open(ctx)
	}
	if err := open("missing", dsn); !source.IsPermanent(err) {
		t.Errorf("missing table: want permanent, got %v", err)
	}
	if err := open("items", strings.Replace(dsn, "root:test@", "root:wrong@", 1)); !source.IsPermanent(err) {
		t.Errorf("bad password: want permanent, got %v", err)
	}

	// A purged binlog is a lost position.
	s, _ := source.New(model.Source{Type: typ, DSN: dsn, Table: "items"}, "errors")
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err := s.Stream(ctx, "mysql-bin.000000:4", func(context.Context, []model.Event) error { return nil })
	if !errors.Is(err, source.ErrPositionLost) {
		t.Errorf("purged binlog: want ErrPositionLost, got %v", err)
	}

	execSQL(t, dsn, "SET GLOBAL binlog_row_image = 'MINIMAL'")
	defer execSQL(t, dsn, "SET GLOBAL binlog_row_image = 'FULL'")
	if err := open("items", dsn); !source.IsPermanent(err) || !strings.Contains(err.Error(), "binlog_row_image") {
		t.Errorf("minimal row image: want permanent, got %v", err)
	}
}
