//go:build integration

package sqlserver

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmssql "github.com/testcontainers/testcontainers-go/modules/mssql"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

// startSQLServer runs SQL Server 2022 with SQL Server Agent (needed by the
// CDC capture job) and returns a DSN for the database "shop". The image is
// amd64 only; on arm64 hosts it runs under emulation (Rosetta in OrbStack or
// Docker Desktop). When it cannot start the test is skipped.
func startSQLServer(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	ctr, err := tcmssql.Run(ctx, "mcr.microsoft.com/mssql/server:2022-latest",
		tcmssql.WithAcceptEULA(),
		tcmssql.WithPassword("Strong@Passw0rd"),
		testcontainers.WithEnv(map[string]string{"MSSQL_AGENT_ENABLED": "true"}),
		testcontainers.WithImagePlatform("linux/amd64"),
	)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Skipf("SQL Server container could not start on this host (the image is amd64 only and needs emulation on arm64): %v", err)
	}
	master, err := ctr.ConnectionString(ctx, "encrypt=disable")
	if err != nil {
		t.Fatal(err)
	}
	execSQL(t, master, "CREATE DATABASE shop")
	dsn, err := withDatabase(master, "shop")
	if err != nil {
		t.Fatal(err)
	}
	return dsn
}

func execSQL(t *testing.T, dsn, query string, args ...any) {
	t.Helper()
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := db.ExecContext(ctx, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func TestConformance(t *testing.T) {
	dsn := startSQLServer(t)
	ctx := context.Background()

	// Errors before CDC is configured are permanent and explain the fix.
	execSQL(t, dsn, "CREATE TABLE dbo.items (id int PRIMARY KEY, name nvarchar(100), price float)")
	cfg := model.Source{Type: model.SourceSQLServer, DSN: dsn, Table: "items"}
	if err := New(cfg).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("cdc disabled: want permanent error, got %v", err)
	}
	execSQL(t, dsn, "EXEC sys.sp_cdc_enable_db")
	if err := New(cfg).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("table not tracked: want permanent error, got %v", err)
	}
	execSQL(t, dsn, "EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'items', @role_name = NULL")

	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			execSQL(t, dsn, "INSERT INTO dbo.items (id, name, price) VALUES (@p1, @p2, @p3)", id, name, price)
		},
		Update: func(t *testing.T, id int, name string) {
			execSQL(t, dsn, "UPDATE dbo.items SET name = @p2 WHERE id = @p1", id, name)
		},
		Delete: func(t *testing.T, id int) {
			execSQL(t, dsn, "DELETE FROM dbo.items WHERE id = @p1", id)
		},
		Timeout: 120 * time.Second,
	})

	t.Run("transaction and key update", func(t *testing.T) {
		s := New(cfg)
		if err := s.Open(ctx); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		pos, err := s.CurrentPosition(ctx)
		if err != nil {
			t.Fatal(err)
		}
		execSQL(t, dsn, "BEGIN TRAN; INSERT INTO dbo.items VALUES (10, N'ten', 1.25); UPDATE dbo.items SET id = 11 WHERE id = 10; COMMIT")
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		var got []model.Event
		err = s.Stream(sctx, pos, func(_ context.Context, evs []model.Event) error {
			got = append(got, evs...)
			for _, e := range got {
				if e.Op == model.OpUpsert && e.Row["id"] == int64(11) {
					cancel()
				}
			}
			return nil
		})
		if sctx.Err() == nil {
			t.Fatalf("stream returned %v", err)
		}
		var sawDelete10 bool
		for _, e := range got {
			if e.Position == "" || e.Time.IsZero() {
				t.Fatalf("event without position/time: %+v", e)
			}
			if e.Op == model.OpDelete && e.Row["id"] == int64(10) && len(e.Row) == 1 {
				sawDelete10 = true
			}
		}
		if !sawDelete10 || got[0].Row["price"] != 1.25 {
			t.Fatalf("events %+v", got)
		}
	})

	t.Run("lost position", func(t *testing.T) {
		s := New(cfg)
		if err := s.Open(ctx); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		err := s.Stream(ctx, "00000000000000000001:00000000000000000000", func(context.Context, []model.Event) error { return nil })
		if !errors.Is(err, source.ErrPositionLost) {
			t.Fatalf("want ErrPositionLost, got %v", err)
		}
	})
}
