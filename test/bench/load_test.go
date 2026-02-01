//go:build bench

// Package bench is a load test of the whole service against a real
// Meilisearch. It produces the CPU profile used for profile guided
// optimisation:
//
//	go test -tags bench -run TestLoad -cpuprofile cpu.out -memprofile mem.out ./test/bench/
package bench

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"os"
	"runtime"
	"strconv"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/all"
	"github.com/ja7ad/meilisearch-cdc/internal/source/memory"
)

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func TestLoad(t *testing.T) {
	ctx := context.Background()
	rows := envInt("BENCH_ROWS", 200_000)
	updates := envInt("BENCH_UPDATES", 50_000)

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        "getmeili/meilisearch:latest",
			ExposedPorts: []string{"7700/tcp"},
			Env:          map[string]string{"MEILI_MASTER_KEY": "bench", "MEILI_NO_ANALYTICS": "true"},
			WaitingFor:   wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.PortEndpoint(ctx, "7700/tcp", "http")
	require.NoError(t, err)

	cfg := config.Config{DataDir: t.TempDir(), MeiliURL: url, MeiliAPIKey: "bench", MeiliGzip: os.Getenv("BENCH_GZIP") != "0",
		MeiliTimeout: time.Minute, LogLevel: "error", StopTimeout: 10 * time.Second, AlertInterval: time.Minute}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, a.Start(ctx))
	defer a.Close(context.Background())

	tab := memory.Table("bench")
	for i := 1; i <= rows; i++ {
		tab.Upsert(row(i, 0))
	}
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	start := time.Now()
	v, err := a.Engine.Create(model.Task{
		Name:   "bench",
		Source: model.Source{Type: memory.Type, Table: "bench"},
		Index:  model.Index{UID: "bench", Settings: []byte(`{"filterableAttributes":["category","price"],"sortableAttributes":["price"]}`), ManageSettings: true},
		Sync:   model.Sync{BatchSize: envInt("BENCH_BATCH", 10_000), MaxInflight: envInt("BENCH_INFLIGHT", 8), FlushInterval: model.Duration(200 * time.Millisecond)},
	}, true)
	require.NoError(t, err)
	meili := meilisearch.New(url, meilisearch.WithAPIKey("bench"))
	waitDocs(t, meili, int64(rows))
	snap := time.Since(start)

	start = time.Now()
	for i := 1; i <= updates; i++ {
		tab.Upsert(row(1+(i*7919)%rows, i)) // spread updates over the table
	}
	last := 1 + (updates*7919)%rows
	waitField(t, meili, last, updates)
	stream := time.Since(start)

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	st := a.Engine.Status(v.ID)
	t.Logf("snapshot: %d rows in %s = %.0f docs/s", rows, snap.Round(time.Millisecond), float64(rows)/snap.Seconds())
	t.Logf("stream:   %d updates in %s = %.0f docs/s (coalesced %d, batches %d)", updates, stream.Round(time.Millisecond),
		float64(updates)/stream.Seconds(), st.Counters.Coalesced, st.Counters.Batches)
	t.Logf("memory:   heap in use %.1f MiB, total allocated %.1f MiB, GC cycles %d",
		float64(after.HeapInuse)/(1<<20), float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), after.NumGC-before.NumGC)
}

func row(i, version int) map[string]any {
	return map[string]any{
		"id": int64(i), "name": fmt.Sprintf("Product %d", i), "description": "A product used to measure the throughput of the pipeline end to end",
		"category": []string{"books", "games", "music", "garden"}[i%4], "price": float64(i%1000) / 10, "in_stock": i%3 != 0,
		"tags": []any{"alpha", "beta"}, "meta": map[string]any{"color": "red", "size": int64(i % 50)},
		"updated_at": time.Unix(1_700_000_000+int64(i), 0), "version": int64(version),
	}
}

func waitDocs(t *testing.T, m meilisearch.ServiceManager, n int64) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		if st, err := m.Index("bench").GetStats(context.Background(), nil); err == nil && st.NumberOfDocuments == n && !st.IsIndexing {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("snapshot did not finish")
}

func waitField(t *testing.T, m meilisearch.ServiceManager, id, version int) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var d map[string]any
		if m.Index("bench").GetDocument(context.Background(), strconv.Itoa(id), nil, &d) == nil && d["version"] == float64(version) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("updates did not arrive")
}

// TestLoadPostgres measures the logical replication path: pgoutput
// decoding, type conversion and the writer.
func TestLoadPostgres(t *testing.T) {
	ctx := context.Background()
	rows := envInt("BENCH_ROWS", 200_000)
	updates := envInt("BENCH_UPDATES", 50_000)
	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("shop"), tcpostgres.WithUsername("cdc"),
		tcpostgres.WithPassword("cdc"), testcontainers.WithCmdArgs("-c", "wal_level=logical"), tcpostgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, pg)
	require.NoError(t, err)
	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	_, err = db.Exec(`CREATE TABLE products (id bigint PRIMARY KEY, name text, description text, category text, price numeric(10,2),
		in_stock boolean, tags text[], meta jsonb, updated_at timestamptz, version int)`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO products SELECT g, 'Product '||g, 'A product used to measure the throughput of the pipeline end to end',
		(ARRAY['books','games','music','garden'])[1+g%4], (g%1000)/10.0, g%3<>0, ARRAY['alpha','beta'],
		jsonb_build_object('color','red','size',g%50), now(), 0 FROM generate_series(1,$1) g`, rows)
	require.NoError(t, err)

	meiliURL := startMeili(t)
	cfg := config.Config{DataDir: t.TempDir(), MeiliURL: meiliURL, MeiliAPIKey: "bench", MeiliGzip: true,
		MeiliTimeout: time.Minute, LogLevel: "error", StopTimeout: 10 * time.Second, AlertInterval: time.Minute}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, a.Start(ctx))
	defer a.Close(context.Background())
	meili := meilisearch.New(meiliURL, meilisearch.WithAPIKey("bench"))

	start := time.Now()
	_, err = a.Engine.Create(model.Task{
		Name: "bench-pg", Source: model.Source{Type: model.SourcePostgres, DSN: dsn, Table: "products"},
		Index: model.Index{UID: "bench"}, Mapping: model.Mapping{TimeFormat: model.TimeUnix},
		Sync: model.Sync{BatchSize: 10_000, MaxInflight: 8, FlushInterval: model.Duration(200 * time.Millisecond)},
	}, true)
	require.NoError(t, err)
	waitDocs(t, meili, int64(rows))
	snap := time.Since(start)

	start = time.Now()
	// Many small transactions, like an application would issue.
	for i := 0; i < updates; i += 500 {
		_, err := db.Exec(`UPDATE products SET version = version + 1, updated_at = now() WHERE id BETWEEN $1 AND $2`, i+1, i+500)
		require.NoError(t, err)
	}
	waitVersion(t, meili, updates, 1)
	stream := time.Since(start)
	t.Logf("postgres snapshot: %d rows in %s = %.0f docs/s", rows, snap.Round(time.Millisecond), float64(rows)/snap.Seconds())
	t.Logf("postgres stream:   %d updates in %s = %.0f docs/s", updates, stream.Round(time.Millisecond), float64(updates)/stream.Seconds())
}

func startMeili(t *testing.T) string {
	c, err := testcontainers.GenericContainer(context.Background(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "getmeili/meilisearch:latest", ExposedPorts: []string{"7700/tcp"},
			Env:        map[string]string{"MEILI_MASTER_KEY": "bench", "MEILI_NO_ANALYTICS": "true"},
			WaitingFor: wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.PortEndpoint(context.Background(), "7700/tcp", "http")
	require.NoError(t, err)
	return url
}

func waitVersion(t *testing.T, m meilisearch.ServiceManager, id, version int) {
	deadline := time.Now().Add(10 * time.Minute)
	for time.Now().Before(deadline) {
		var d map[string]any
		if m.Index("bench").GetDocument(context.Background(), strconv.Itoa(id), nil, &d) == nil && d["version"] == float64(version) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("updates did not arrive")
}
