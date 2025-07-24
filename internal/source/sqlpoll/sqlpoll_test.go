package sqlpoll

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func newSQLite(t *testing.T) (*sql.DB, string) {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", sqliteDSN(dsn))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dsn
}

func TestConformanceSQLite(t *testing.T) {
	db, dsn := newSQLite(t)
	if _, err := db.Exec(`CREATE TABLE items (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		price REAL,
		meta JSON,
		deleted INTEGER NOT NULL DEFAULT 0,
		version INTEGER NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	var version atomic.Int64
	exec := func(t *testing.T, q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg := model.Source{
		Type: model.SourceSQLPoll, Driver: "sqlite", DSN: dsn, Table: "items",
		CursorColumn: "version", PollInterval: model.Duration(100 * time.Millisecond),
	}
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			exec(t, `INSERT INTO items (id, name, price, meta, version) VALUES (?, ?, ?, '{"a":[1,2]}', ?)`, id, name, price, version.Add(1))
		},
		Update: func(t *testing.T, id int, name string) {
			exec(t, `UPDATE items SET name = ?, version = ? WHERE id = ?`, name, version.Add(1), id)
		},
		// Hard deletes are invisible to polling; soft delete instead, which
		// mapping.softDeleteColumn turns into a document delete.
		Delete: func(t *testing.T, id int) {
			exec(t, `UPDATE items SET deleted = 1, version = ? WHERE id = ?`, version.Add(1), id)
		},
		SkipDelete: true,
		Timeout:    10 * time.Second,
	})
}

func TestStreamPagesAndTypes(t *testing.T) {
	db, dsn := newSQLite(t)
	if _, err := db.Exec(`CREATE TABLE t (a INTEGER, b TEXT, v INTEGER, data BLOB, meta JSON, PRIMARY KEY (a, b))`); err != nil {
		t.Fatal(err)
	}
	// More rows than one batch, many sharing a cursor value.
	tx, _ := db.Begin()
	for i := 0; i < 2500; i++ {
		if _, err := tx.Exec(`INSERT INTO t VALUES (?, ?, ?, ?, '{"n":1.5,"k":[1]}')`, i%7, string(rune('a'+i%26))+time.Duration(i).String(), i/10, []byte{1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	s := New(model.Source{Driver: "sqlite", DSN: dsn, Table: "t", CursorColumn: "v", PollInterval: model.Duration(50 * time.Millisecond)})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	schema, err := s.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.PrimaryKey) != 2 || schema.PrimaryKey[0] != "a" || schema.PrimaryKey[1] != "b" || schema.RowCount != 2500 {
		t.Fatalf("schema: %+v", schema)
	}

	var snap int
	if err := s.Snapshot(ctx, func(_ context.Context, evs []model.Event) error {
		snap += len(evs)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if snap != 2500 {
		t.Fatalf("snapshot rows %d", snap)
	}

	seen := map[string]bool{}
	var lastPos model.Position
	sctx, stop := context.WithCancel(ctx)
	err = s.Stream(sctx, `{"cursor":null,"pk":[]}`, func(_ context.Context, evs []model.Event) error {
		for _, e := range evs {
			k := e.Row["b"].(string)
			if seen[k] {
				t.Errorf("duplicate row %v", k)
			}
			seen[k] = true
			if _, ok := e.Row["data"].([]byte); !ok {
				t.Errorf("blob not []byte: %T", e.Row["data"])
			}
			m, ok := e.Row["meta"].(map[string]any)
			if !ok || m["n"] != 1.5 {
				t.Errorf("json not decoded: %#v", e.Row["meta"])
			}
			if e.Position <= lastPos && lastPos != "" && e.Position == lastPos {
				t.Errorf("position not advancing")
			}
			lastPos = e.Position
		}
		if len(seen) == 2500 {
			stop()
		}
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("stream: %v", err)
	}
	if len(seen) != 2500 {
		t.Fatalf("streamed %d rows", len(seen))
	}
	cur, err := s.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cur != lastPos {
		t.Fatalf("current position %s, last streamed %s", cur, lastPos)
	}
}

func TestOpenErrors(t *testing.T) {
	db, dsn := newSQLite(t)
	if _, err := db.Exec(`CREATE TABLE nopk (id INTEGER, v INTEGER)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, cfg := range map[string]model.Source{
		"bad driver":     {Driver: "oracle", DSN: dsn, Table: "nopk", CursorColumn: "v"},
		"missing table":  {Driver: "sqlite", DSN: dsn, Table: "missing", CursorColumn: "v"},
		"missing cursor": {Driver: "sqlite", DSN: dsn, Table: "nopk", CursorColumn: "x"},
		"no pk":          {Driver: "sqlite", DSN: dsn, Table: "nopk", CursorColumn: "v"},
	} {
		s := New(cfg)
		err := s.Open(ctx)
		s.Close()
		if !source.IsPermanent(err) {
			t.Errorf("%s: want permanent error, got %v", name, err)
		}
	}
}

// Without a cursor the schema and a snapshot can be read (to help choose
// the cursor), but positions and streaming need it.
func TestOpenWithoutCursor(t *testing.T) {
	db, dsn := newSQLite(t)
	if _, err := db.Exec(`CREATE TABLE items (id INTEGER PRIMARY KEY, name TEXT, version INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO items VALUES (1, 'a', 1), (2, 'b', 2)`); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	s := New(model.Source{Driver: "sqlite", DSN: dsn, Table: "items"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	schema, err := s.Schema(ctx)
	if err != nil || len(schema.Columns) != 3 {
		t.Fatalf("schema %+v, %v", schema, err)
	}
	n := 0
	if err := s.Snapshot(ctx, func(_ context.Context, evs []model.Event) error { n += len(evs); return nil }); err != nil || n != 2 {
		t.Fatalf("snapshot %d rows, %v", n, err)
	}
	if _, err := s.CurrentPosition(ctx); !source.IsPermanent(err) {
		t.Errorf("current position: want permanent error, got %v", err)
	}
	if err := s.Stream(ctx, "", func(context.Context, []model.Event) error { return nil }); !source.IsPermanent(err) {
		t.Errorf("stream: want permanent error, got %v", err)
	}
}
