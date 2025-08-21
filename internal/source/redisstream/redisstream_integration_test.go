//go:build integration

package redisstream

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func startRedis(t *testing.T) (string, *redis.Client) {
	t.Helper()
	ctx := context.Background()
	c, err := tcredis.Run(ctx, "redis:7-alpine")
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	opts, err := redis.ParseURL(uri)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { client.Close() })
	return uri, client
}

func xadd(t *testing.T, c *redis.Client, key string, values ...any) string {
	t.Helper()
	id, err := c.XAdd(context.Background(), &redis.XAddArgs{Stream: key, Values: values}).Result()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestConformance(t *testing.T) {
	uri, client := startRedis(t)
	cfg := model.Source{Type: model.SourceRedis, DSN: uri, Table: "items"}
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			xadd(t, client, "items", "op", "upsert", "id", strconv.Itoa(id),
				"data", fmt.Sprintf(`{"id":%d,"name":%q,"price":%v}`, id, name, price))
		},
		Update: func(t *testing.T, id int, name string) {
			xadd(t, client, "items", "id", strconv.Itoa(id), "name", name)
		},
		Delete: func(t *testing.T, id int) {
			xadd(t, client, "items", "op", "delete", "id", strconv.Itoa(id))
		},
	})
}

func TestSnapshotCompacts(t *testing.T) {
	uri, client := startRedis(t)
	ctx := context.Background()
	xadd(t, client, "s", "id", "1", "name", "a")
	xadd(t, client, "s", "id", "2", "name", "b")
	xadd(t, client, "s", "op", "truncate")
	xadd(t, client, "s", "id", "3", "name", "c")
	xadd(t, client, "s", "id", "3", "name", "c2")
	xadd(t, client, "s", "id", "4", "name", "d")
	xadd(t, client, "s", "op", "delete", "id", "4")

	s := New(model.Source{DSN: uri, Table: "s"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var got []model.Event
	if err := s.Snapshot(ctx, func(_ context.Context, evs []model.Event) error { got = append(got, evs...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Row["id"] != "3" || got[0].Row["name"] != "c2" || !got[0].Snapshot {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestErrors(t *testing.T) {
	uri, client := startRedis(t)
	ctx := context.Background()

	if err := client.Set(ctx, "str", "x", 0).Err(); err != nil {
		t.Fatal(err)
	}
	s := New(model.Source{DSN: uri, Table: "str"})
	if err := s.Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("non stream key: want permanent, got %v", err)
	}
	s.Close()

	for i := 0; i < 10; i++ {
		xadd(t, client, "trim", "id", strconv.Itoa(i))
	}
	msgs, _ := client.XRangeN(ctx, "trim", "-", "+", 2).Result()
	if err := client.XTrimMaxLen(ctx, "trim", 3).Err(); err != nil {
		t.Fatal(err)
	}
	s = New(model.Source{DSN: uri, Table: "trim"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	err := s.Stream(sctx, model.Position(msgs[1].ID), func(context.Context, []model.Event) error { return nil })
	if !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("trimmed: want ErrPositionLost, got %v", err)
	}
	pos, err := s.CurrentPosition(ctx)
	if err != nil || pos == "0-0" {
		t.Fatalf("current position = %q, %v", pos, err)
	}

	empty := New(model.Source{DSN: uri, Table: "missing"})
	if err := empty.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	if pos, err := empty.CurrentPosition(ctx); err != nil || pos != "0-0" {
		t.Fatalf("missing stream position = %q, %v", pos, err)
	}
	schema, err := empty.Schema(ctx)
	if err != nil || schema.PrimaryKey[0] != "id" {
		t.Fatalf("schema = %+v, %v", schema, err)
	}

	bad := New(model.Source{DSN: "redis://:wrong@" + uri[len("redis://"):], Table: "x"})
	if err := client.ConfigSet(ctx, "requirepass", "secret").Err(); err != nil {
		t.Fatal(err)
	}
	if err := bad.Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("bad password: want permanent, got %v", err)
	}
	bad.Close()
}
