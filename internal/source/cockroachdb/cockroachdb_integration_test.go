//go:build integration

package cockroachdb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	tccrdb "github.com/testcontainers/testcontainers-go/modules/cockroachdb"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func startCockroach(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tccrdb.Run(ctx, "cockroachdb/cockroach:latest-v24.3", tccrdb.WithInsecure())
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	cc, err := ctr.ConnectionConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgresql://%s@%s/%s?sslmode=disable", cc.User, net.JoinHostPort(cc.Host, strconv.Itoa(int(cc.Port))), cc.Database)
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
	dsn := startCockroach(t)
	execSQL(t, dsn, "CREATE TABLE items (id INT PRIMARY KEY, name STRING, price DECIMAL(10,2), created TIMESTAMPTZ DEFAULT now())")
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(model.Source{Type: model.SourceCockroachDB, DSN: dsn, Table: "items"}, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			execSQL(t, dsn, "INSERT INTO items (id, name, price) VALUES ($1, $2, $3)", id, name, price)
		},
		Update: func(t *testing.T, id int, name string) {
			execSQL(t, dsn, "UPDATE items SET name = $2 WHERE id = $1", id, name)
		},
		Delete: func(t *testing.T, id int) {
			execSQL(t, dsn, "DELETE FROM items WHERE id = $1", id)
		},
	})
}

func TestValuesAndErrors(t *testing.T) {
	dsn := startCockroach(t)
	ctx := context.Background()
	execSQL(t, dsn, "CREATE TABLE things (a INT, b STRING, price DECIMAL(10,2), at TIMESTAMPTZ, PRIMARY KEY (a, b))")
	execSQL(t, dsn, "CREATE TABLE nopk (x INT)")
	execSQL(t, dsn, "INSERT INTO things VALUES (1, 'x', 9.5, '2024-01-02 03:04:05+00')")

	s := New(model.Source{DSN: dsn, Table: "things"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	schema, err := s.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(schema.PrimaryKey) != 2 || schema.PrimaryKey[0] != "a" || schema.PrimaryKey[1] != "b" || len(schema.Columns) != 4 {
		t.Fatalf("schema %+v", schema)
	}
	pos, err := s.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var snap []model.Event
	if err := s.Snapshot(ctx, func(_ context.Context, evs []model.Event) error { snap = append(snap, evs...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(snap) != 1 {
		t.Fatalf("snapshot %+v", snap)
	}
	at, ok := snap[0].Row["at"].(time.Time)
	if !ok || !at.Equal(time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)) || snap[0].Row["price"] != 9.5 || snap[0].Row["a"] != int64(1) {
		t.Fatalf("snapshot row %#v", snap[0].Row)
	}

	// Composite key delete through the stream, and the same value types.
	sctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	events := make(chan model.Event, 10)
	go func() {
		_ = s.Stream(sctx, pos, func(_ context.Context, evs []model.Event) error {
			for _, e := range evs {
				events <- e
			}
			return nil
		})
	}()
	execSQL(t, dsn, "INSERT INTO things VALUES (2, 'y', 1, '2024-01-02 03:04:05.5+00')")
	execSQL(t, dsn, "DELETE FROM things WHERE a = 1")
	var got []model.Event
	for len(got) < 2 {
		select {
		case e := <-events:
			got = append(got, e)
		case <-sctx.Done():
			t.Fatalf("timed out, got %+v", got)
		}
	}
	// Changefeeds order rows per key only.
	if got[0].Op == model.OpDelete {
		got[0], got[1] = got[1], got[0]
	}
	if got[0].Op != model.OpUpsert || got[0].Row["price"] != 1.0 || got[0].Position == "" {
		t.Fatalf("insert %+v", got[0])
	}
	if _, ok := got[0].Row["at"].(time.Time); !ok {
		t.Fatalf("stream time %#v", got[0].Row["at"])
	}
	if got[1].Op != model.OpDelete || got[1].Row["a"] != int64(1) || got[1].Row["b"] != "x" {
		t.Fatalf("delete %+v", got[1])
	}
	cancel()

	if err := New(model.Source{DSN: dsn, Table: "nopk"}).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("no pk: %v", err)
	}
	if err := New(model.Source{DSN: dsn, Table: "missing"}).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("missing: %v", err)
	}

	// A cursor older than the table's retained history is lost.
	old := "1000000000000000000.0000000000" // 2001
	lctx, lcancel := context.WithTimeout(ctx, 60*time.Second)
	defer lcancel()
	err = s.Stream(lctx, model.Position(old), func(context.Context, []model.Event) error { return nil })
	if !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("old cursor: want ErrPositionLost, got %v", err)
	}
}

// An idle changefeed reports progress from its resolved timestamps, so the
// checkpoint and the lag stay current without any change in the table.
func TestIdleProgress(t *testing.T) {
	dsn := startCockroach(t)
	execSQL(t, dsn, "CREATE TABLE quiet (id INT PRIMARY KEY, name STRING)")
	s, err := source.New(model.Source{Type: model.SourceCockroachDB, DSN: dsn, Table: "quiet"}, "t-idle")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	start, err := s.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var progress []model.Event
	errStop := errors.New("enough")
	err = s.Stream(ctx, start, func(_ context.Context, evs []model.Event) error {
		for _, ev := range evs {
			if ev.Op != model.OpProgress {
				t.Fatalf("unexpected change %v on an idle table", ev.Op)
			}
			progress = append(progress, ev)
		}
		// The first resolved timestamps catch up from the cursor; stop once
		// progress is current.
		if last := progress[len(progress)-1]; len(progress) >= 2 && time.Since(last.Time) < 15*time.Second {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("stream: %v (%d progress events)", err, len(progress))
	}
	last := progress[len(progress)-1]
	if !hlcLess(string(start), string(last.Position)) {
		t.Errorf("position did not advance: %s -> %s", start, last.Position)
	}
	if last.Time.After(time.Now().Add(time.Second)) {
		t.Errorf("progress time %s is in the future", last.Time)
	}
}
