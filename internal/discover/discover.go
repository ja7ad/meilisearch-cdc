// Package discover tests source connections and lists their tables,
// collections, topics or streams for the connector browser.
package discover

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	_ "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	_ "modernc.org/sqlite"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// Table is a discovered table, collection, topic or stream.
type Table struct {
	Name   string `json:"name"`
	Schema string `json:"schema,omitempty"`
	Kind   string `json:"kind"`
	Rows   int64  `json:"rows"`
}

// Result is the outcome of a connection test.
type Result struct {
	OK        bool    `json:"ok"`
	Error     string  `json:"error,omitempty"`
	LatencyMs int64   `json:"latencyMs"`
	Version   string  `json:"version,omitempty"`
	Tables    []Table `json:"tables"`
}

// MaxTables bounds discovery results.
const MaxTables = 1000

// Run connects with src, measures latency and lists tables. It never
// returns an error: failures are reported in the result.
func Run(ctx context.Context, src model.Source) Result {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	start := time.Now()
	version, tables, err := list(ctx, src)
	res := Result{LatencyMs: time.Since(start).Milliseconds(), Version: version, Tables: tables}
	if res.Tables == nil {
		res.Tables = []Table{}
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	sort.Slice(res.Tables, func(i, j int) bool {
		if res.Tables[i].Schema != res.Tables[j].Schema {
			return res.Tables[i].Schema < res.Tables[j].Schema
		}
		return res.Tables[i].Name < res.Tables[j].Name
	})
	if len(res.Tables) > MaxTables {
		res.Tables = res.Tables[:MaxTables]
	}
	res.OK = true
	return res
}

// Filter keeps tables whose name contains q (case insensitive).
func Filter(tables []Table, q string) []Table {
	q = strings.ToLower(strings.TrimSpace(q))
	if q == "" {
		return tables
	}
	out := []Table{}
	for _, t := range tables {
		if strings.Contains(strings.ToLower(t.Schema+"."+t.Name), q) {
			out = append(out, t)
		}
	}
	return out
}

func list(ctx context.Context, s model.Source) (string, []Table, error) {
	switch s.Type {
	case model.SourcePostgres, model.SourceCockroachDB:
		return postgres(ctx, s.DSN, s.Type == model.SourceCockroachDB)
	case model.SourceMySQL, model.SourceMariaDB:
		return sqlTables(ctx, "mysql", s.DSN, "SELECT VERSION()",
			`SELECT table_schema, table_name, table_type, COALESCE(table_rows, -1) FROM information_schema.tables
			 WHERE table_schema NOT IN ('mysql','information_schema','performance_schema','sys')`)
	case model.SourceSQLServer:
		return sqlTables(ctx, "sqlserver", s.DSN, "SELECT @@VERSION",
			`SELECT TABLE_SCHEMA, TABLE_NAME, TABLE_TYPE, -1 FROM INFORMATION_SCHEMA.TABLES WHERE TABLE_SCHEMA NOT IN ('cdc','sys')`)
	case model.SourceSQLPoll:
		switch s.Driver {
		case "pgx", "postgres":
			return postgres(ctx, s.DSN, false)
		case "mysql":
			return list(ctx, model.Source{Type: model.SourceMySQL, DSN: s.DSN})
		case "sqlite":
			return sqlTables(ctx, "sqlite", s.DSN, "SELECT sqlite_version()",
				`SELECT '', name, type, -1 FROM sqlite_master WHERE type IN ('table','view') AND name NOT LIKE 'sqlite_%'`)
		}
		return "", nil, fmt.Errorf("unsupported driver %q", s.Driver)
	case model.SourceMongoDB:
		return mongoTables(ctx, s)
	case model.SourceDynamoDB:
		return dynamoTables(ctx, s)
	case model.SourceKafka:
		return kafkaTopics(ctx, s.Brokers)
	case model.SourceCouchDB:
		return couchDatabases(ctx, s.DSN)
	case model.SourceRedis:
		return redisStreams(ctx, s.DSN)
	case model.SourceWebhook:
		return "", []Table{}, nil
	}
	return "", nil, fmt.Errorf("discovery is not supported for %q", s.Type)
}

func kind(t string) string {
	t = strings.ToLower(t)
	if strings.Contains(t, "view") {
		return "view"
	}
	return "table"
}

func postgres(ctx context.Context, dsn string, crdb bool) (string, []Table, error) {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return "", nil, err
	}
	defer conn.Close(context.Background())
	var version string
	_ = conn.QueryRow(ctx, "SELECT version()").Scan(&version)
	q := `SELECT t.table_schema, t.table_name, t.table_type,
	        COALESCE((SELECT c.reltuples::bigint FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
	                  WHERE n.nspname = t.table_schema AND c.relname = t.table_name), -1)
	      FROM information_schema.tables t
	      WHERE t.table_schema NOT IN ('pg_catalog','information_schema','crdb_internal','pg_extension','pg_toast')`
	if crdb {
		q = `SELECT table_schema, table_name, table_type, -1 FROM information_schema.tables
		     WHERE table_schema NOT IN ('pg_catalog','information_schema','crdb_internal','pg_extension')`
	}
	rows, err := conn.Query(ctx, q)
	if err != nil {
		return version, nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		var typ string
		if err := rows.Scan(&t.Schema, &t.Name, &typ, &t.Rows); err != nil {
			return version, nil, err
		}
		t.Kind = kind(typ)
		out = append(out, t)
	}
	return firstLine(version), out, rows.Err()
}

func sqlTables(ctx context.Context, driver, dsn, versionQ, tablesQ string) (string, []Table, error) {
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return "", nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var version string
	if err := db.QueryRowContext(ctx, versionQ).Scan(&version); err != nil {
		return "", nil, err
	}
	rows, err := db.QueryContext(ctx, tablesQ)
	if err != nil {
		return version, nil, err
	}
	defer rows.Close()
	var out []Table
	for rows.Next() {
		var t Table
		var typ string
		if err := rows.Scan(&t.Schema, &t.Name, &typ, &t.Rows); err != nil {
			return version, nil, err
		}
		t.Kind = kind(typ)
		out = append(out, t)
	}
	return firstLine(version), out, rows.Err()
}

func firstLine(s string) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	return s
}

func mongoTables(ctx context.Context, s model.Source) (string, []Table, error) {
	cl, err := mongo.Connect(options.Client().ApplyURI(s.DSN).SetServerSelectionTimeout(10 * time.Second))
	if err != nil {
		return "", nil, err
	}
	defer cl.Disconnect(context.Background())
	var info struct {
		Version string `bson:"version"`
	}
	if err := cl.Database("admin").RunCommand(ctx, map[string]any{"buildInfo": 1}).Decode(&info); err != nil {
		return "", nil, err
	}
	if s.Database == "" {
		names, err := cl.ListDatabaseNames(ctx, map[string]any{})
		if err != nil {
			return info.Version, nil, err
		}
		var out []Table
		for _, n := range names {
			if n != "admin" && n != "local" && n != "config" {
				out = append(out, Table{Name: n, Kind: "database", Rows: -1})
			}
		}
		return "MongoDB " + info.Version, out, nil
	}
	names, err := cl.Database(s.Database).ListCollectionNames(ctx, map[string]any{})
	if err != nil {
		return info.Version, nil, err
	}
	var out []Table
	for _, n := range names {
		if !strings.HasPrefix(n, "system.") {
			out = append(out, Table{Name: n, Schema: s.Database, Kind: "collection", Rows: -1})
		}
	}
	return "MongoDB " + info.Version, out, nil
}

func dynamoTables(ctx context.Context, s model.Source) (string, []Table, error) {
	opts := []func(*awsconfig.LoadOptions) error{awsconfig.WithRegion(s.Region)}
	if s.AccessKeyID != "" {
		opts = append(opts, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(s.AccessKeyID, s.SecretAccessKey, "")))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return "", nil, err
	}
	db := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if s.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.Endpoint)
		}
	})
	var out []Table
	p := dynamodb.NewListTablesPaginator(db, &dynamodb.ListTablesInput{})
	for p.HasMorePages() && len(out) < MaxTables {
		page, err := p.NextPage(ctx)
		if err != nil {
			return "", nil, err
		}
		for _, n := range page.TableNames {
			out = append(out, Table{Name: n, Kind: "table", Rows: -1})
		}
	}
	return "DynamoDB", out, nil
}

func kafkaTopics(ctx context.Context, brokers []string) (string, []Table, error) {
	if len(brokers) == 0 {
		return "", nil, errors.New("no brokers")
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(brokers...))
	if err != nil {
		return "", nil, err
	}
	defer cl.Close()
	topics, err := kadm.NewClient(cl).ListTopics(ctx)
	if err != nil {
		return "", nil, err
	}
	var out []Table
	for _, t := range topics.Sorted() {
		if !t.IsInternal && !strings.HasPrefix(t.Topic, "_") {
			out = append(out, Table{Name: t.Topic, Kind: "topic", Rows: -1})
		}
	}
	return "Kafka", out, nil
}

func couchDatabases(ctx context.Context, dsn string) (string, []Table, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return "", nil, err
	}
	get := func(path string, v any) error {
		ref := *u
		ref.Path = strings.TrimRight(u.Path, "/") + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref.String(), nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return errors.New(strings.ReplaceAll(err.Error(), u.String(), u.Redacted()))
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 300 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("couchdb: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
		}
		return json.NewDecoder(resp.Body).Decode(v)
	}
	var root struct {
		Version string `json:"version"`
	}
	if err := get("/", &root); err != nil {
		return "", nil, err
	}
	var dbs []string
	if err := get("/_all_dbs", &dbs); err != nil {
		return root.Version, nil, err
	}
	var out []Table
	for _, d := range dbs {
		if !strings.HasPrefix(d, "_") {
			out = append(out, Table{Name: d, Kind: "database", Rows: -1})
		}
	}
	return "CouchDB " + root.Version, out, nil
}

func redisStreams(ctx context.Context, dsn string) (string, []Table, error) {
	opts, err := redis.ParseURL(dsn)
	if err != nil {
		return "", nil, err
	}
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	info, err := rdb.Info(ctx, "server").Result()
	if err != nil {
		return "", nil, err
	}
	version := "Redis"
	for _, line := range strings.Split(info, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "redis_version:"); ok {
			version += " " + v
		}
	}
	var out []Table
	var cursor uint64
	for {
		keys, next, err := rdb.ScanType(ctx, cursor, "*", 500, "stream").Result()
		if err != nil {
			return version, nil, err
		}
		for _, k := range keys {
			out = append(out, Table{Name: k, Kind: "stream", Rows: -1})
		}
		cursor = next
		if cursor == 0 || len(out) >= MaxTables {
			break
		}
	}
	return version, out, nil
}
