//go:build e2e

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	tcdynamodb "github.com/testcontainers/testcontainers-go/modules/dynamodb"
	tcmariadb "github.com/testcontainers/testcontainers-go/modules/mariadb"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	tcmysql "github.com/testcontainers/testcontainers-go/modules/mysql"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func baseTask(name, index string, source map[string]any) map[string]any {
	return map[string]any{
		"name":   name,
		"source": source,
		"index": map[string]any{
			"uid": index, "manageSettings": true,
			"settings": map[string]any{"filterableAttributes": []string{"price"}, "sortableAttributes": []string{"price"}},
		},
		"sync": map[string]any{"batchSize": 2000, "flushInterval": "100ms", "maxInflight": 8,
			"safety": map[string]any{"zeroDowntimeResync": true}},
	}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	_, err := db.Exec(q, args...)
	require.NoError(t, err, q)
}

func TestPostgresEndToEnd(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("shop"), tcpostgres.WithUsername("cdc"),
		tcpostgres.WithPassword("cdc"), testcontainers.WithCmdArgs("-c", "wal_level=logical"), tcpostgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE products (id bigint PRIMARY KEY, name text NOT NULL, price numeric(10,2), tags jsonb, created_at timestamptz DEFAULT now())`)
	const seeded = 20000
	mustExec(t, db, `INSERT INTO products SELECT g, 'product '||g, (g % 500)::numeric / 4, jsonb_build_object('color', CASE WHEN g % 2 = 0 THEN 'red' ELSE 'blue' END) FROM generate_series(1, $1) g`, seeded)

	dir := t.TempDir()
	svc := startService(t, dir)
	index := indexName(t)
	task := baseTask("postgres products", index, map[string]any{"type": "postgres", "dsn": dsn, "table": "products"})
	task["mapping"] = map[string]any{"timeFormat": "unix"}
	start := time.Now()
	id := svc.createTask(task)
	svc.waitState(id, model.StateStreaming)
	waitDocs(t, index, seeded)
	t.Logf("postgres snapshot: %d rows in %s (%.0f docs/s)", seeded, time.Since(start).Round(time.Millisecond), seeded/time.Since(start).Seconds())

	doc := getDoc(index, "42")
	require.NotNil(t, doc)
	assert.Equal(t, map[string]any{"color": "red"}, doc["tags"])
	_, isNum := doc["created_at"].(float64)
	assert.True(t, isNum, "unix time format")

	// Live changes, including a multi-row transaction.
	tx, _ := db.Begin()
	_, _ = tx.Exec(`INSERT INTO products (id, name, price) VALUES (100001, 'fresh', 1.5)`)
	_, _ = tx.Exec(`UPDATE products SET name = 'renamed' WHERE id = 1`)
	_, _ = tx.Exec(`DELETE FROM products WHERE id = 2`)
	require.NoError(t, tx.Commit())
	waitDoc(t, index, "100001", named("fresh"))
	waitDoc(t, index, "1", named("renamed"))
	waitDoc(t, index, "2", gone)

	// Crash: kill -9 while a burst is being written, then restart.
	mustExec(t, db, `INSERT INTO products (id, name, price) SELECT g, 'burst '||g, 1 FROM generate_series(200001, 205000) g`)
	time.Sleep(150 * time.Millisecond)
	svc.stop(syscall.SIGKILL)
	mustExec(t, db, `UPDATE products SET name = 'while down' WHERE id = 3`)
	mustExec(t, db, `DELETE FROM products WHERE id BETWEEN 10 AND 19`)
	svc = startService(t, dir)
	svc.waitState(id, model.StateStreaming)
	want := int64(seeded + 1 - 1 + 5000 - 10)
	waitDocs(t, index, want)
	waitDoc(t, index, "3", named("while down"))
	assert.Equal(t, int64(seeded), svc.status(id).Snapshot.Done, "recovered from the checkpoint without a new snapshot")

	// Filter on a managed setting.
	res, err := meili.Index(index).Search(ctx, "", &meilisearch.SearchRequest{Filter: "price < 1", Limit: 1})
	require.NoError(t, err)
	assert.Positive(t, res.EstimatedTotalHits)

	// Diff is clean, then zero-downtime resync keeps the index searchable.
	svc.do("POST", "/api/v1/tasks/"+id+"/diff", map[string]any{"compareFields": true}, nil)
	var rep map[string]any
	eventually(t, 2*time.Minute, func() bool {
		svc.do("GET", "/api/v1/tasks/"+id+"/diff", nil, &rep)
		return rep["status"] == "done"
	}, func() string { return fmt.Sprintf("diff: %v", rep) })
	assert.Equal(t, true, rep["inSync"], "%v", rep)

	require.Equal(t, 200, svc.do("POST", "/api/v1/tasks/"+id+"/resync", nil, nil))
	searchable := true
	stop := make(chan struct{})
	go func() {
		for {
			select {
			case <-stop:
				return
			default:
			}
			if r, err := meili.Index(index).Search(ctx, "product", &meilisearch.SearchRequest{Limit: 1}); err != nil || len(r.Hits) == 0 {
				searchable = false
			}
			time.Sleep(20 * time.Millisecond)
		}
	}()
	svc.waitState(id, model.StateStreaming)
	close(stop)
	assert.True(t, searchable, "search never went empty during the resync")
	waitDocs(t, index, want)

	// Deleting the task with dropSource releases the replication slot.
	require.Equal(t, 204, svc.do("DELETE", "/api/v1/tasks/"+id+"?dropSource=true", nil, nil))
	var slots int
	require.NoError(t, db.QueryRow(`SELECT count(*) FROM pg_replication_slots`).Scan(&slots))
	assert.Zero(t, slots)
}

func mysqlLike(t *testing.T, flavor string) {
	ctx := context.Background()
	args := []string{"--log-bin=mysql-bin", "--binlog-format=ROW", "--binlog-row-image=FULL", "--server-id=1"}
	var (
		dsn string
		err error
	)
	if flavor == "mariadb" {
		c, rerr := tcmariadb.Run(ctx, "mariadb:11", tcmariadb.WithUsername("root"), tcmariadb.WithPassword("test"), tcmariadb.WithDatabase("shop"), testcontainers.WithCmdArgs(args...))
		testcontainers.CleanupContainer(t, c)
		require.NoError(t, rerr)
		dsn, err = c.ConnectionString(ctx)
	} else {
		c, rerr := tcmysql.Run(ctx, "mysql:8.4", tcmysql.WithUsername("root"), tcmysql.WithPassword("test"), tcmysql.WithDatabase("shop"), testcontainers.WithCmdArgs(args...))
		testcontainers.CleanupContainer(t, c)
		require.NoError(t, rerr)
		dsn, err = c.ConnectionString(ctx)
	}
	require.NoError(t, err)
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE products (id INT UNSIGNED PRIMARY KEY, sku VARCHAR(32), name VARCHAR(100), price DECIMAL(10,2), status ENUM('live','archived') DEFAULT 'live')`)
	for i := 1; i <= 2000; i += 500 {
		q := "INSERT INTO products (id, sku, name, price) VALUES "
		var vals []any
		for j := i; j < i+500; j++ {
			if j > i {
				q += ","
			}
			q += "(?, ?, ?, ?)"
			vals = append(vals, j, fmt.Sprintf("SKU-%d", j), fmt.Sprintf("product %d", j), float64(j)/10)
		}
		mustExec(t, db, q, vals...)
	}

	svc := startService(t, t.TempDir())
	index := indexName(t)
	task := baseTask(flavor+" products", index, map[string]any{"type": flavor, "dsn": dsn, "table": "products"})
	task["mapping"] = map[string]any{"softDeleteColumn": "status", "softDeleteValue": "archived"}
	id := svc.createTask(task)
	svc.waitState(id, model.StateStreaming)
	waitDocs(t, index, 2000)

	mustExec(t, db, `INSERT INTO products (id, sku, name, price) VALUES (5000, 'SKU-N', 'fresh', 1)`)
	mustExec(t, db, `UPDATE products SET name = 'renamed' WHERE id = 1`)
	mustExec(t, db, `DELETE FROM products WHERE id = 2`)
	mustExec(t, db, `UPDATE products SET status = 'archived' WHERE id = 3`)
	waitDoc(t, index, "5000", named("fresh"))
	waitDoc(t, index, "1", named("renamed"))
	waitDoc(t, index, "2", gone)
	waitDoc(t, index, "3", gone)
	waitDocs(t, index, 1999)

	// Schema change: a new column flows through after ALTER TABLE.
	mustExec(t, db, `ALTER TABLE products ADD COLUMN brand VARCHAR(50)`)
	mustExec(t, db, `UPDATE products SET brand = 'acme' WHERE id = 4`)
	waitDoc(t, index, "4", func(d map[string]any) bool { return d != nil && d["brand"] == "acme" })
}

func TestMySQLEndToEnd(t *testing.T)   { mysqlLike(t, "mysql") }
func TestMariaDBEndToEnd(t *testing.T) { mysqlLike(t, "mariadb") }

func TestMongoEndToEnd(t *testing.T) {
	ctx := context.Background()
	c, err := tcmongo.Run(ctx, "mongo:7", tcmongo.WithReplicaSet("rs0"))
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	uri, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	u, _ := url.Parse(uri)
	q := u.Query()
	q.Set("directConnection", "true")
	u.RawQuery = q.Encode()
	uri = u.String()
	cl, err := mongo.Connect(options.Client().ApplyURI(uri))
	require.NoError(t, err)
	defer cl.Disconnect(ctx)
	coll := cl.Database("shop").Collection("products")
	var docs []any
	for i := 1; i <= 1000; i++ {
		docs = append(docs, bson.M{"_id": i, "name": fmt.Sprintf("product %d", i), "price": i, "nested": bson.M{"a": i}})
	}
	_, err = coll.InsertMany(ctx, docs)
	require.NoError(t, err)

	svc := startService(t, t.TempDir())
	index := indexName(t)
	id := svc.createTask(baseTask("mongo products", index, map[string]any{"type": "mongodb", "dsn": uri, "database": "shop", "table": "products"}))
	svc.waitState(id, model.StateStreaming)
	waitDocs(t, index, 1000)
	assert.Equal(t, map[string]any{"a": float64(5)}, getDoc(index, "5")["nested"])

	_, err = coll.InsertOne(ctx, bson.M{"_id": 5000, "name": "fresh"})
	require.NoError(t, err)
	_, err = coll.UpdateOne(ctx, bson.M{"_id": 1}, bson.M{"$set": bson.M{"name": "renamed"}})
	require.NoError(t, err)
	_, err = coll.DeleteOne(ctx, bson.M{"_id": 2})
	require.NoError(t, err)
	waitDoc(t, index, "5000", named("fresh"))
	waitDoc(t, index, "1", named("renamed"))
	waitDoc(t, index, "2", gone)
}

func TestDynamoDBEndToEnd(t *testing.T) {
	ctx := context.Background()
	c, err := tcdynamodb.Run(ctx, "amazon/dynamodb-local:2.2.1", tcdynamodb.WithDisableTelemetry())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	hostPort, err := c.ConnectionString(ctx)
	require.NoError(t, err)
	endpoint := "http://" + hostPort
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint),
		Credentials: credentials.NewStaticCredentialsProvider("dummy", "dummy", "")})
	_, err = db.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String("products"),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
		StreamSpecification:  &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeNewAndOldImages},
	})
	require.NoError(t, err)
	put := func(id int, name string) {
		_, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("products"), Item: map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberN{Value: strconv.Itoa(id)}, "name": &types.AttributeValueMemberS{Value: name},
		}})
		require.NoError(t, err)
	}
	for i := 1; i <= 200; i++ {
		put(i, fmt.Sprintf("product %d", i))
	}
	svc := startService(t, t.TempDir())
	index := indexName(t)
	id := svc.createTask(baseTask("dynamo products", index, map[string]any{
		"type": "dynamodb", "table": "products", "region": "us-east-1", "endpoint": endpoint, "accessKeyId": "dummy", "secretAccessKey": "dummy",
	}))
	svc.waitState(id, model.StateStreaming)
	waitDocs(t, index, 200)
	put(5000, "fresh")
	put(1, "renamed")
	_, err = db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String("products"), Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberN{Value: "2"}}})
	require.NoError(t, err)
	waitDoc(t, index, "5000", named("fresh"))
	waitDoc(t, index, "1", named("renamed"))
	waitDoc(t, index, "2", gone)
}

func TestKafkaDebeziumEndToEnd(t *testing.T) {
	ctx := context.Background()
	c, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v23.3.3", redpanda.WithAutoCreateTopics())
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	broker, err := c.KafkaSeedBroker(ctx)
	require.NoError(t, err)
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.AllowAutoTopicCreation())
	require.NoError(t, err)
	defer cl.Close()
	topic := "dbserver.inventory.products"
	send := func(op string, id int, after map[string]any) {
		key, _ := json.Marshal(map[string]any{"id": id})
		env := map[string]any{"op": op, "ts_ms": time.Now().UnixMilli(), "before": map[string]any{"id": id}}
		if after != nil {
			env["after"] = after
		}
		val, _ := json.Marshal(map[string]any{"payload": env})
		require.NoError(t, cl.ProduceSync(ctx, &kgo.Record{Topic: topic, Key: key, Value: val}).FirstErr())
	}
	for i := 1; i <= 100; i++ {
		send("r", i, map[string]any{"id": i, "name": fmt.Sprintf("product %d", i)})
	}
	svc := startService(t, t.TempDir())
	index := indexName(t)
	id := svc.createTask(baseTask("debezium products", index, map[string]any{"type": "kafka", "brokers": []string{broker}, "table": topic}))
	svc.waitState(id, model.StateStreaming)
	waitDocs(t, index, 100)
	send("c", 5000, map[string]any{"id": 5000, "name": "fresh"})
	send("u", 1, map[string]any{"id": 1, "name": "renamed"})
	send("d", 2, nil)
	waitDoc(t, index, "5000", named("fresh"))
	waitDoc(t, index, "1", named("renamed"))
	waitDoc(t, index, "2", gone)
}

// PostgreSQL does not send unchanged TOASTed values on update. In replace
// mode the document must keep them instead of losing the field.
func TestPostgresToastColumnsSurviveUpdates(t *testing.T) {
	ctx := context.Background()
	ctr, err := tcpostgres.Run(ctx, "postgres:17-alpine", tcpostgres.WithDatabase("shop"), tcpostgres.WithUsername("cdc"),
		tcpostgres.WithPassword("cdc"), testcontainers.WithCmdArgs("-c", "wal_level=logical"), tcpostgres.BasicWaitStrategies())
	testcontainers.CleanupContainer(t, ctr)
	require.NoError(t, err)
	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)
	db, err := sql.Open("pgx", dsn)
	require.NoError(t, err)
	defer db.Close()
	mustExec(t, db, `CREATE TABLE articles (id bigint PRIMARY KEY, title text, price int, body text)`)
	mustExec(t, db, `CREATE TABLE articles_full (id bigint PRIMARY KEY, title text, price int, body text)`)
	mustExec(t, db, `ALTER TABLE articles_full REPLICA IDENTITY FULL`)
	// ~13 KB of incompressible text is stored out of line (TOAST).
	const big = `(SELECT string_agg(md5(random()::text), '') FROM generate_series(1, 400))`
	for _, tbl := range []string{"articles", "articles_full"} {
		mustExec(t, db, `INSERT INTO `+tbl+` SELECT g, 'title '||g, g, `+big+` FROM generate_series(1, 3) g`)
	}

	svc := startService(t, t.TempDir())
	for _, tbl := range []string{"articles", "articles_full"} {
		index := indexName(t) + "_" + tbl
		id := svc.createTask(baseTask(tbl, index, map[string]any{"type": "postgres", "dsn": dsn, "table": tbl}))
		svc.waitState(id, model.StateStreaming)
		waitDocs(t, index, 3)
		var body string
		require.NoError(t, db.QueryRow(`SELECT body FROM `+tbl+` WHERE id = 1`).Scan(&body))
		require.Greater(t, len(body), 10_000)

		mustExec(t, db, `UPDATE `+tbl+` SET price = 99 WHERE id = 1`)
		waitDoc(t, index, "1", func(d map[string]any) bool { return d["price"] == float64(99) })
		doc := getDoc(index, "1")
		assert.Equal(t, body, doc["body"], "%s: the unchanged TOAST column is kept", tbl)
		assert.Equal(t, "title 1", doc["title"])
	}
}
