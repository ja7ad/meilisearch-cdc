// Command lab seeds every source of deploy/e2e/docker-compose.yml with the
// same product catalogue and serves helpers that change it, for the full
// GUI end-to-end tests (test/ui/full).
//
//	go run ./test/ui/lab seed              # (re)create the data everywhere
//	go run ./test/ui/lab serve [-seed]     # :8799, GET /info, POST /change
//
// Every source holds the table (collection, topic, stream...) "products"
// with ids 1..Rows. POST /change {"source","op":"insert|update|delete","id","name"}
// applies one change through the source's native API.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	"github.com/redis/go-redis/v9"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Rows is the size of the seeded catalogue in every source.
const Rows = 300

// Connection details of the compose stack, as the dashboard needs them.
var (
	pgDSN      = env("LAB_POSTGRES", "postgres://cdc:cdc@localhost:15432/shop?sslmode=disable")
	mysqlDSN   = env("LAB_MYSQL", "root:cdc@tcp(localhost:13306)/shop")
	mariaDSN   = env("LAB_MARIADB", "root:cdc@tcp(localhost:13307)/shop")
	mongoURI   = env("LAB_MONGODB", "mongodb://localhost:27018/?replicaSet=rs0&directConnection=true")
	dynamoURL  = env("LAB_DYNAMODB", "http://localhost:18000")
	couchURL   = env("LAB_COUCHDB", "http://admin:secret@localhost:15984")
	mssqlDSN   = env("LAB_SQLSERVER", "sqlserver://sa:Strong%40Passw0rd@localhost:11433?database=shop&encrypt=disable")
	mssqlAdmin = env("LAB_SQLSERVER_MASTER", "sqlserver://sa:Strong%40Passw0rd@localhost:11433?encrypt=disable")
	crdbDSN    = env("LAB_COCKROACHDB", "postgresql://root@localhost:26258/shop?sslmode=disable")
	crdbAdmin  = env("LAB_COCKROACHDB_ADMIN", "postgresql://root@localhost:26258/defaultdb?sslmode=disable")
	broker     = env("LAB_KAFKA", "localhost:19092")
	redisAddr  = env("LAB_REDIS", "localhost:16379")
)

const kafkaTopic = "shop.public.products"

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// product is one catalogue row; every source stores the same fields.
type product struct {
	ID        int
	Name      string
	Category  string
	Brand     string
	Price     float64
	Stock     int
	Rating    float64
	Active    bool
	UpdatedAt time.Time
	Version   int
}

var (
	categories = []string{"books", "games", "music", "garden", "kitchen", "sports"}
	brands     = []string{"Acme", "Globex", "Initech", "Umbrella", "Hooli", "Stark"}
	adjectives = []string{"Classic", "Smart", "Compact", "Deluxe", "Eco", "Pro", "Mini", "Ultra"}
	nouns      = []string{"Lamp", "Kettle", "Novel", "Puzzle", "Speaker", "Shovel", "Racket", "Mug", "Album", "Board game"}
)

func gen(id int) product {
	return product{
		ID: id, Name: fmt.Sprintf("%s %s %d", adjectives[id%len(adjectives)], nouns[id%len(nouns)], id),
		Category: categories[id%len(categories)], Brand: brands[(id/7)%len(brands)],
		Price: float64(id%97) + 0.99, Stock: (id * 13) % 250, Rating: float64(id%50)/10 + 0.1,
		Active: id%11 != 0, UpdatedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(id) * time.Minute), Version: 1,
	}
}

func (p product) doc() map[string]any {
	return map[string]any{"id": p.ID, "name": p.Name, "category": p.Category, "brand": p.Brand, "price": p.Price,
		"stock": p.Stock, "rating": p.Rating, "active": p.Active, "updated_at": p.UpdatedAt.Format(time.RFC3339), "version": p.Version}
}

// source seeds and changes one kind of database.
type source struct {
	name   string
	seed   func(ctx context.Context) error
	upsert func(ctx context.Context, p product, insert bool) error
	delete func(ctx context.Context, id int) error
}

func main() {
	log.SetFlags(log.Ltime)
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: lab seed [source...] | lab serve [-addr :8799] [-seed]")
		os.Exit(2)
	}
	ctx := context.Background()
	switch os.Args[1] {
	case "seed":
		if err := seedAll(ctx, os.Args[2:]); err != nil {
			log.Fatal(err)
		}
	case "serve":
		fs := flag.NewFlagSet("serve", flag.ExitOnError)
		addr := fs.String("addr", "127.0.0.1:8799", "listen address")
		doSeed := fs.Bool("seed", false, "seed every source first")
		_ = fs.Parse(os.Args[2:])
		if *doSeed {
			if err := seedAll(ctx, nil); err != nil {
				log.Fatal(err)
			}
		}
		log.Printf("lab listening on http://%s", *addr)
		srv := &http.Server{Addr: *addr, Handler: handler(), ReadHeaderTimeout: 10 * time.Second}
		log.Fatal(srv.ListenAndServe())
	default:
		log.Fatalf("unknown command %q", os.Args[1])
	}
}

func seedAll(ctx context.Context, only []string) error {
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for _, s := range sources() {
		if len(only) > 0 && !slices.Contains(only, s.name) {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			start := time.Now()
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			err := s.seed(ctx)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", s.name, err))
				log.Printf("✘ %-12s %v", s.name, err)
				return
			}
			log.Printf("✓ %-12s %d rows in %s", s.name, Rows, time.Since(start).Round(time.Millisecond))
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

func sources() []source {
	return []source{
		sqlSource("postgres", "pgx", pgDSN, pgSchema, pgPlaceholders, nil),
		sqlSource("mysql", "mysql", mysqlDSN, mysqlSchema, qmarks, nil),
		sqlSource("mariadb", "mysql", mariaDSN, mysqlSchema, qmarks, nil),
		sqlSource("sqlserver", "sqlserver", mssqlDSN, mssqlSchema, atp, mssqlPrepare),
		sqlSource("cockroachdb", "pgx", crdbDSN, crdbSchema, pgPlaceholders, crdbPrepare),
		softDeletes(sqlSource("sqlpoll", "pgx", pgDSN, pollSchema, pgPlaceholders, nil)),
		mongoSource(), dynamoSource(), couchSource(), kafkaSource(), redisSource(),
	}
}

// ---------------------------------------------------------------- SQL

type schema struct {
	table string
	ddl   []string
}

var (
	pgSchema = schema{"products", []string{
		`DROP PUBLICATION IF EXISTS cdc_products`,
		`DROP TABLE IF EXISTS products`,
		`CREATE TABLE products (id int PRIMARY KEY, name text NOT NULL, category text, brand text, price numeric(10,2), stock int,
			rating double precision, active boolean, updated_at timestamptz, version int)`,
		`ALTER TABLE products REPLICA IDENTITY FULL`,
	}}
	mysqlSchema = schema{"products", []string{
		`DROP TABLE IF EXISTS products`,
		`CREATE TABLE products (id int PRIMARY KEY, name varchar(200) NOT NULL, category varchar(50), brand varchar(50), price decimal(10,2),
			stock int, rating double, active boolean, updated_at datetime(3), version int)`,
	}}
	mssqlSchema = schema{"products", []string{
		`IF OBJECT_ID('dbo.products') IS NOT NULL AND EXISTS (SELECT 1 FROM cdc.change_tables WHERE source_object_id = OBJECT_ID('dbo.products'))
			EXEC sys.sp_cdc_disable_table @source_schema = N'dbo', @source_name = N'products', @capture_instance = N'all'`,
		`IF OBJECT_ID('dbo.products') IS NOT NULL DROP TABLE dbo.products`,
		`CREATE TABLE dbo.products (id int PRIMARY KEY, name nvarchar(200) NOT NULL, category nvarchar(50), brand nvarchar(50), price decimal(10,2),
			stock int, rating float, active bit, updated_at datetime2, version int)`,
		`EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'products', @role_name = NULL`,
	}}
	crdbSchema = schema{"products", []string{
		`DROP TABLE IF EXISTS products`,
		`CREATE TABLE products (id INT PRIMARY KEY, name STRING NOT NULL, category STRING, brand STRING, price DECIMAL(10,2), stock INT,
			rating FLOAT, active BOOL, updated_at TIMESTAMPTZ, version INT)`,
	}}
	// SQL polling reads a separate Postgres table; updated_at is its cursor.
	pollSchema = schema{"catalog", []string{
		`DROP TABLE IF EXISTS catalog`,
		`CREATE TABLE catalog (id int PRIMARY KEY, name text NOT NULL, category text, brand text, price numeric(10,2), stock int,
			rating double precision, active boolean, updated_at timestamptz NOT NULL, version int, deleted boolean NOT NULL DEFAULT false)`,
	}}
)

func pgPlaceholders(n int) []string { return numbered("$", n) }
func atp(n int) []string            { return numbered("@p", n) }
func qmarks(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = "?"
	}
	return out
}

func numbered(prefix string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + strconv.Itoa(i+1)
	}
	return out
}

// softDeletes turns deletes of the SQL polling table into soft deletes:
// polling cannot see removed rows, so the task maps deleted=true to a delete.
func softDeletes(s source) source {
	s.delete = func(ctx context.Context, id int) error {
		db, err := sql.Open("pgx", pgDSN)
		if err != nil {
			return err
		}
		defer db.Close()
		_, err = db.ExecContext(ctx, `UPDATE catalog SET deleted = true, updated_at = now() WHERE id = $1`, id)
		return err
	}
	return s
}

// mssqlPrepare creates the database and turns on CDC for it.
func mssqlPrepare(ctx context.Context) error {
	admin, err := sql.Open("sqlserver", mssqlAdmin)
	if err != nil {
		return err
	}
	defer admin.Close()
	if _, err := admin.ExecContext(ctx, `IF DB_ID('shop') IS NULL CREATE DATABASE shop`); err != nil {
		return err
	}
	db, err := sql.Open("sqlserver", mssqlDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `IF (SELECT is_cdc_enabled FROM sys.databases WHERE name = 'shop') = 0 EXEC sys.sp_cdc_enable_db`)
	return err
}

// crdbPrepare creates the database and enables rangefeeds (changefeeds).
func crdbPrepare(ctx context.Context) error {
	db, err := sql.Open("pgx", crdbAdmin)
	if err != nil {
		return err
	}
	defer db.Close()
	for _, q := range []string{`CREATE DATABASE IF NOT EXISTS shop`, `SET CLUSTER SETTING kv.rangefeed.enabled = true`} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	return nil
}

var cols = []string{"id", "name", "category", "brand", "price", "stock", "rating", "active", "updated_at", "version"}

func (p product) values() []any {
	return []any{p.ID, p.Name, p.Category, p.Brand, p.Price, p.Stock, p.Rating, p.Active, p.UpdatedAt, p.Version}
}

func sqlSource(name, driver, dsn string, sc schema, ph func(int) []string, prepare func(context.Context) error) source {
	var (
		once sync.Once
		db   *sql.DB
		err  error
	)
	open := func() (*sql.DB, error) {
		once.Do(func() { db, err = sql.Open(driver, dsn) })
		return db, err
	}
	insertSQL := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", sc.table, strings.Join(cols, ", "), strings.Join(ph(len(cols)), ", "))
	p := ph(4)
	updateSQL := fmt.Sprintf("UPDATE %s SET name = %s, updated_at = %s, version = version + 1, price = price + %s WHERE id = %s", sc.table, p[0], p[1], p[2], p[3])
	deleteSQL := fmt.Sprintf("DELETE FROM %s WHERE id = %s", sc.table, ph(1)[0])
	return source{
		name: name,
		seed: func(ctx context.Context) error {
			if prepare != nil {
				if err := prepare(ctx); err != nil {
					return err
				}
			}
			db, err := open()
			if err != nil {
				return err
			}
			for _, q := range sc.ddl {
				if _, err := db.ExecContext(ctx, q); err != nil {
					return fmt.Errorf("%s: %w", strings.Fields(q)[0], err)
				}
			}
			tx, err := db.BeginTx(ctx, nil)
			if err != nil {
				return err
			}
			for i := 1; i <= Rows; i++ {
				if _, err := tx.ExecContext(ctx, insertSQL, gen(i).values()...); err != nil {
					_ = tx.Rollback()
					return err
				}
			}
			return tx.Commit()
		},
		upsert: func(ctx context.Context, pr product, insert bool) error {
			db, err := open()
			if err != nil {
				return err
			}
			if insert {
				// Like an application would: SQL polling needs a cursor that
				// grows with every write.
				pr.UpdatedAt = time.Now().UTC()
				_, err = db.ExecContext(ctx, insertSQL, pr.values()...)
				return err
			}
			res, err := db.ExecContext(ctx, updateSQL, pr.Name, time.Now().UTC(), 1, pr.ID)
			if err == nil {
				if n, _ := res.RowsAffected(); n == 0 {
					err = fmt.Errorf("no row %d", pr.ID)
				}
			}
			return err
		},
		delete: func(ctx context.Context, id int) error {
			db, err := open()
			if err != nil {
				return err
			}
			_, err = db.ExecContext(ctx, deleteSQL, id)
			return err
		},
	}
}

// ---------------------------------------------------------------- MongoDB

func mongoSource() source {
	var (
		once sync.Once
		cl   *mongo.Client
		err  error
	)
	coll := func() (*mongo.Collection, error) {
		once.Do(func() { cl, err = mongo.Connect(options.Client().ApplyURI(mongoURI)) })
		if err != nil {
			return nil, err
		}
		return cl.Database("shop").Collection("products"), nil
	}
	bdoc := func(p product) bson.M {
		d := bson.M(p.doc())
		d["_id"] = p.ID
		delete(d, "id")
		d["updated_at"] = p.UpdatedAt
		return d
	}
	return source{
		name: "mongodb",
		seed: func(ctx context.Context) error {
			c, err := coll()
			if err != nil {
				return err
			}
			if err := c.Drop(ctx); err != nil {
				return err
			}
			docs := make([]any, Rows)
			for i := range docs {
				docs[i] = bdoc(gen(i + 1))
			}
			_, err = c.InsertMany(ctx, docs)
			return err
		},
		upsert: func(ctx context.Context, p product, insert bool) error {
			c, err := coll()
			if err != nil {
				return err
			}
			if insert {
				_, err = c.InsertOne(ctx, bdoc(p))
				return err
			}
			_, err = c.UpdateByID(ctx, p.ID, bson.M{"$set": bson.M{"name": p.Name, "updated_at": time.Now().UTC()}, "$inc": bson.M{"version": 1}})
			return err
		},
		delete: func(ctx context.Context, id int) error {
			c, err := coll()
			if err != nil {
				return err
			}
			_, err = c.DeleteOne(ctx, bson.M{"_id": id})
			return err
		},
	}
}

// ---------------------------------------------------------------- DynamoDB

func dynamoSource() source {
	db := dynamodb.New(dynamodb.Options{Region: "us-east-1", BaseEndpoint: aws.String(dynamoURL),
		Credentials: credentials.NewStaticCredentialsProvider("dummy", "dummy", "")})
	item := func(p product) map[string]types.AttributeValue {
		return map[string]types.AttributeValue{
			"id": &types.AttributeValueMemberN{Value: strconv.Itoa(p.ID)}, "name": &types.AttributeValueMemberS{Value: p.Name},
			"category": &types.AttributeValueMemberS{Value: p.Category}, "brand": &types.AttributeValueMemberS{Value: p.Brand},
			"price":  &types.AttributeValueMemberN{Value: strconv.FormatFloat(p.Price, 'f', 2, 64)},
			"stock":  &types.AttributeValueMemberN{Value: strconv.Itoa(p.Stock)},
			"rating": &types.AttributeValueMemberN{Value: strconv.FormatFloat(p.Rating, 'f', 1, 64)},
			"active": &types.AttributeValueMemberBOOL{Value: p.Active}, "updated_at": &types.AttributeValueMemberS{Value: p.UpdatedAt.Format(time.RFC3339)},
			"version": &types.AttributeValueMemberN{Value: strconv.Itoa(p.Version)},
		}
	}
	table := aws.String("products")
	return source{
		name: "dynamodb",
		seed: func(ctx context.Context) error {
			_, _ = db.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: table})
			if _, err := db.CreateTable(ctx, &dynamodb.CreateTableInput{
				TableName:            table,
				AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
				KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
				BillingMode:          types.BillingModePayPerRequest,
				StreamSpecification:  &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeNewAndOldImages},
			}); err != nil {
				return err
			}
			for start := 1; start <= Rows; start += 25 {
				var reqs []types.WriteRequest
				for i := start; i < start+25 && i <= Rows; i++ {
					reqs = append(reqs, types.WriteRequest{PutRequest: &types.PutRequest{Item: item(gen(i))}})
				}
				if _, err := db.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{"products": reqs}}); err != nil {
					return err
				}
			}
			return nil
		},
		upsert: func(ctx context.Context, p product, _ bool) error {
			p.UpdatedAt, p.Version = time.Now().UTC(), p.Version+1
			_, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: table, Item: item(p)})
			return err
		},
		delete: func(ctx context.Context, id int) error {
			_, err := db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: table,
				Key: map[string]types.AttributeValue{"id": &types.AttributeValueMemberN{Value: strconv.Itoa(id)}}})
			return err
		},
	}
}

// ---------------------------------------------------------------- CouchDB

func couchSource() source {
	call := func(ctx context.Context, method, path string, body any, out any) (int, error) {
		var rd *strings.Reader
		if body != nil {
			b, _ := json.Marshal(body)
			rd = strings.NewReader(string(b))
		} else {
			rd = strings.NewReader("")
		}
		req, err := http.NewRequestWithContext(ctx, method, couchURL+path, rd)
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode, nil
	}
	doc := func(p product) map[string]any {
		d := p.doc()
		d["_id"] = strconv.Itoa(p.ID)
		return d
	}
	rev := func(ctx context.Context, id int) (string, error) {
		var cur map[string]any
		code, err := call(ctx, http.MethodGet, "/products/"+strconv.Itoa(id), nil, &cur)
		if err != nil || code != 200 {
			return "", fmt.Errorf("get %d: %d %v", id, code, err)
		}
		return fmt.Sprint(cur["_rev"]), nil
	}
	return source{
		name: "couchdb",
		seed: func(ctx context.Context) error {
			for _, db := range []string{"_users", "_replicator"} {
				_, _ = call(ctx, http.MethodPut, "/"+db, nil, nil)
			}
			_, _ = call(ctx, http.MethodDelete, "/products", nil, nil)
			if code, err := call(ctx, http.MethodPut, "/products", nil, nil); err != nil || code >= 300 {
				return fmt.Errorf("create database: %d %v", code, err)
			}
			docs := make([]any, Rows)
			for i := range docs {
				docs[i] = doc(gen(i + 1))
			}
			code, err := call(ctx, http.MethodPost, "/products/_bulk_docs", map[string]any{"docs": docs}, nil)
			if err == nil && code >= 300 {
				err = fmt.Errorf("bulk docs: %d", code)
			}
			return err
		},
		upsert: func(ctx context.Context, p product, insert bool) error {
			d := doc(p)
			if !insert {
				r, err := rev(ctx, p.ID)
				if err != nil {
					return err
				}
				d["_rev"] = r
				d["version"] = p.Version + 1
				d["updated_at"] = time.Now().UTC().Format(time.RFC3339)
			}
			code, err := call(ctx, http.MethodPut, "/products/"+strconv.Itoa(p.ID), d, nil)
			if err == nil && code >= 300 {
				err = fmt.Errorf("put: %d", code)
			}
			return err
		},
		delete: func(ctx context.Context, id int) error {
			r, err := rev(ctx, id)
			if err != nil {
				return err
			}
			code, err := call(ctx, http.MethodDelete, "/products/"+strconv.Itoa(id)+"?rev="+r, nil, nil)
			if err == nil && code >= 300 {
				err = fmt.Errorf("delete: %d", code)
			}
			return err
		},
	}
}

// ---------------------------------------------------------------- Kafka (Debezium)

func kafkaSource() source {
	var (
		once sync.Once
		cl   *kgo.Client
		err  error
	)
	client := func() (*kgo.Client, error) {
		once.Do(func() { cl, err = kgo.NewClient(kgo.SeedBrokers(broker), kgo.AllowAutoTopicCreation()) })
		return cl, err
	}
	// send produces a Debezium envelope (JSON converter with schemas off).
	send := func(ctx context.Context, op string, id int, after map[string]any) error {
		c, err := client()
		if err != nil {
			return err
		}
		key, _ := json.Marshal(map[string]any{"id": id})
		env := map[string]any{"op": op, "ts_ms": time.Now().UnixMilli(), "before": map[string]any{"id": id},
			"source": map[string]any{"connector": "postgresql", "db": "shop", "schema": "public", "table": "products"}}
		if after != nil {
			env["after"] = after
		}
		val, _ := json.Marshal(map[string]any{"payload": env})
		return c.ProduceSync(ctx, &kgo.Record{Topic: kafkaTopic, Key: key, Value: val}).FirstErr()
	}
	return source{
		name: "kafka",
		seed: func(ctx context.Context) error {
			c, err := client()
			if err != nil {
				return err
			}
			adm := kadm.NewClient(c)
			_, _ = adm.DeleteTopics(ctx, kafkaTopic)
			// Topic deletion is asynchronous; retry creation until it is gone.
			for i := 0; ; i++ {
				_, err := adm.CreateTopic(ctx, 3, 1, nil, kafkaTopic)
				if err == nil {
					break
				}
				if i > 40 {
					return fmt.Errorf("create topic: %w", err)
				}
				time.Sleep(250 * time.Millisecond)
			}
			for i := 1; i <= Rows; i++ {
				if err := send(ctx, "r", i, gen(i).doc()); err != nil {
					return err
				}
			}
			return nil
		},
		upsert: func(ctx context.Context, p product, insert bool) error {
			op := "u"
			if insert {
				op = "c"
			}
			d := p.doc()
			d["updated_at"] = time.Now().UTC().Format(time.RFC3339)
			return send(ctx, op, p.ID, d)
		},
		delete: func(ctx context.Context, id int) error { return send(ctx, "d", id, nil) },
	}
}

// ---------------------------------------------------------------- Redis streams

func redisSource() source {
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	add := func(ctx context.Context, op string, id int, data map[string]any) error {
		vals := map[string]any{"op": op, "id": strconv.Itoa(id)}
		if data != nil {
			b, _ := json.Marshal(data)
			vals["data"] = string(b)
		}
		return rdb.XAdd(ctx, &redis.XAddArgs{Stream: "products", Values: vals}).Err()
	}
	return source{
		name: "redis",
		seed: func(ctx context.Context) error {
			if err := rdb.Del(ctx, "products").Err(); err != nil {
				return err
			}
			for i := 1; i <= Rows; i++ {
				if err := add(ctx, "upsert", i, gen(i).doc()); err != nil {
					return err
				}
			}
			return nil
		},
		upsert: func(ctx context.Context, p product, _ bool) error {
			d := p.doc()
			d["updated_at"] = time.Now().UTC().Format(time.RFC3339)
			return add(ctx, "upsert", p.ID, d)
		},
		delete: func(ctx context.Context, id int) error { return add(ctx, "delete", id, nil) },
	}
}

// ---------------------------------------------------------------- HTTP

type change struct {
	Source string `json:"source"`
	Op     string `json:"op"`
	ID     int    `json:"id"`
	Name   string `json:"name"`
}

func handler() http.Handler {
	byName := map[string]source{}
	for _, s := range sources() {
		byName[s.name] = s
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /info", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(info())
	})
	mux.HandleFunc("POST /seed", func(w http.ResponseWriter, r *http.Request) {
		var only []string
		if s := r.URL.Query().Get("source"); s != "" {
			only = strings.Split(s, ",")
		}
		if err := seedAll(r.Context(), only); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// POST /sql {"sql": "..."} runs statements on the Postgres source, for
	// tests that need their own tables (guards, mapping options).
	mux.HandleFunc("POST /sql", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			SQL string `json:"sql"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		db, err := sql.Open("pgx", pgDSN)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer db.Close()
		if _, err := db.ExecContext(r.Context(), req.SQL); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /change", func(w http.ResponseWriter, r *http.Request) {
		var c change
		if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s, ok := byName[c.Source]
		if !ok {
			http.Error(w, "unknown source "+c.Source, http.StatusNotFound)
			return
		}
		p := gen(c.ID)
		if c.Name != "" {
			p.Name = c.Name
		}
		var err error
		switch c.Op {
		case "insert":
			err = s.upsert(r.Context(), p, true)
		case "update":
			err = s.upsert(r.Context(), p, false)
		case "delete":
			err = s.delete(r.Context(), c.ID)
		default:
			err = fmt.Errorf("unknown op %q", c.Op)
		}
		if err != nil {
			log.Printf("change %+v: %v", c, err)
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}

// info describes how the dashboard connects to each source: the connector
// form values, the table to sync and type specific task options.
func info() map[string]any {
	return map[string]any{
		"rows": Rows,
		"meilisearch": []map[string]string{
			{"name": "Primary", "url": env("LAB_MEILI_PRIMARY", "http://localhost:17700"), "apiKey": "e2e-master-key"}, // #nosec G101 -- test fixture
			{"name": "Replica", "url": env("LAB_MEILI_REPLICA", "http://localhost:17701"), "apiKey": "e2e-master-key"}, // #nosec G101 -- test fixture
		},
		"mailpit": map[string]any{"smtpHost": "localhost", "smtpPort": 11025, "api": "http://localhost:18025"},
		"sources": []map[string]any{
			{"source": "postgres", "type": "postgres", "connector": map[string]any{"dsn": pgDSN}, "table": "products"},
			{"source": "mysql", "type": "mysql", "connector": map[string]any{"dsn": mysqlDSN}, "table": "products"},
			{"source": "mariadb", "type": "mariadb", "connector": map[string]any{"dsn": mariaDSN}, "table": "products"},
			{"source": "mongodb", "type": "mongodb", "connector": map[string]any{"dsn": mongoURI, "database": "shop"}, "table": "products"},
			{"source": "dynamodb", "type": "dynamodb", "connector": map[string]any{"region": "us-east-1", "endpoint": dynamoURL, "accessKeyId": "dummy", "secretAccessKey": "dummy"}, "table": "products"},
			{"source": "couchdb", "type": "couchdb", "connector": map[string]any{"dsn": couchURL}, "table": "products"},
			{"source": "sqlserver", "type": "sqlserver", "connector": map[string]any{"dsn": mssqlDSN}, "table": "products"},
			{"source": "cockroachdb", "type": "cockroachdb", "connector": map[string]any{"dsn": crdbDSN}, "table": "products"},
			{"source": "kafka", "type": "kafka", "connector": map[string]any{"brokers": []string{broker}}, "table": kafkaTopic},
			{"source": "redis", "type": "redis", "connector": map[string]any{"dsn": "redis://" + redisAddr}, "table": "products"},
			{"source": "sqlpoll", "type": "sqlpoll", "connector": map[string]any{"driver": "pgx", "dsn": pgDSN}, "table": "catalog", "cursorColumn": "updated_at",
				"softDelete": map[string]string{"column": "deleted", "value": "true"}},
		},
	}
}
