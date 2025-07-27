//go:build integration

package sqlpoll

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

// runSQL runs the conformance suite with an updated_at timestamp cursor,
// which exercises time typed positions.
func runSQL(t *testing.T, driver, dsn, schema, ddl, now string, ph func(int) string) {
	t.Helper()
	db, err := sql.Open(map[string]string{"postgres": "pgx", "mysql": "mysql"}[driver], dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(ddl); err != nil {
		t.Fatal(err)
	}
	exec := func(t *testing.T, q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	cfg := model.Source{
		Type: model.SourceSQLPoll, Driver: driver, DSN: dsn, Schema: schema, Table: "items",
		CursorColumn: "updated_at", PollInterval: model.Duration(200 * time.Millisecond),
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
			exec(t, "INSERT INTO items (id, name, price, updated_at) VALUES ("+ph(1)+", "+ph(2)+", "+ph(3)+", "+now+")", id, name, price)
		},
		Update: func(t *testing.T, id int, name string) {
			exec(t, "UPDATE items SET name = "+ph(1)+", updated_at = "+now+" WHERE id = "+ph(2), name, id)
		},
		Delete: func(t *testing.T, id int) {
			exec(t, "UPDATE items SET deleted = TRUE, updated_at = "+now+" WHERE id = "+ph(1), id)
		},
		SkipDelete: true,
		Timeout:    20 * time.Second,
	})
}

func TestConformancePostgres(t *testing.T) {
	ctx := context.Background()
	c, err := tcpostgres.Run(ctx, "postgres:16-alpine", tcpostgres.WithDatabase("shop"),
		tcpostgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	runSQL(t, "postgres", dsn, "public", `CREATE TABLE items (
		id bigint PRIMARY KEY, name text NOT NULL, price numeric(10,2),
		deleted boolean NOT NULL DEFAULT false, meta jsonb DEFAULT '{"a":1}',
		updated_at timestamptz NOT NULL)`,
		"clock_timestamp()", func(i int) string { return "$" + string(rune('0'+i)) })
}

func TestConformanceMySQL(t *testing.T) {
	ctx := context.Background()
	c, err := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithDatabase("shop"))
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	dsn, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	runSQL(t, "mysql", dsn, "", `CREATE TABLE items (
		id BIGINT PRIMARY KEY, name VARCHAR(100) NOT NULL, price DECIMAL(10,2),
		deleted BOOLEAN NOT NULL DEFAULT FALSE, meta JSON,
		updated_at DATETIME(6) NOT NULL)`,
		"NOW(6)", func(int) string { return "?" })
}
