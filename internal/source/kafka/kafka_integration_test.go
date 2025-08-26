//go:build integration

package kafka

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/redpanda"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

const topic = "dbserver1.inventory.items"

func TestConformance(t *testing.T) {
	ctx := context.Background()
	c, err := redpanda.Run(ctx, "docker.redpanda.com/redpandadata/redpanda:v23.3.3")
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	broker, err := c.KafkaSeedBroker(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.DefaultProduceTopic(topic))
	if err != nil {
		t.Fatal(err)
	}
	defer cl.Close()
	adm := kadm.NewClient(cl)
	if _, err := adm.CreateTopic(ctx, 3, 1, nil, topic); err != nil {
		t.Fatal(err)
	}

	// The harness writes synthetic Debezium events; rows keeps the full row
	// image like a database would.
	var mu sync.Mutex
	rows := map[int]map[string]any{}
	produce := func(t *testing.T, id int, value any) {
		t.Helper()
		key, _ := json.Marshal(map[string]any{"id": id})
		var val []byte
		if value != nil {
			val, _ = json.Marshal(value)
		}
		if err := cl.ProduceSync(ctx, &kgo.Record{Key: key, Value: val}).FirstErr(); err != nil {
			t.Fatal(err)
		}
	}
	ts := func() int64 { return time.Now().UnixMilli() }

	cfg := model.Source{Type: model.SourceKafka, Brokers: []string{broker}, Table: topic}
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			mu.Lock()
			row := map[string]any{"id": id, "name": name, "price": price}
			rows[id] = row
			mu.Unlock()
			// With the JsonConverter schema wrapper.
			produce(t, id, map[string]any{
				"schema": map[string]any{"type": "struct", "fields": []any{
					map[string]any{"field": "after", "type": "struct", "fields": []any{
						map[string]any{"field": "id", "type": "int32"},
						map[string]any{"field": "name", "type": "string"},
						map[string]any{"field": "price", "type": "double"},
					}},
				}},
				"payload": map[string]any{"op": "c", "before": nil, "after": row, "ts_ms": ts()},
			})
		},
		Update: func(t *testing.T, id int, name string) {
			mu.Lock()
			before := rows[id]
			after := map[string]any{"id": id, "name": name, "price": before["price"]}
			rows[id] = after
			mu.Unlock()
			produce(t, id, map[string]any{"op": "u", "before": before, "after": after, "ts_ms": ts()})
		},
		Delete: func(t *testing.T, id int) {
			mu.Lock()
			before := rows[id]
			delete(rows, id)
			mu.Unlock()
			produce(t, id, map[string]any{"op": "d", "before": before, "after": nil, "ts_ms": ts()})
			produce(t, id, nil) // tombstone
		},
	})

	// Ack committed the group offsets.
	committed, err := adm.FetchOffsets(ctx, "cdc-t1")
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	committed.Each(func(o kadm.OffsetResponse) { total += o.At })
	if total == 0 {
		t.Fatalf("no committed offsets for group cdc-t1")
	}

	t.Run("schema", func(t *testing.T) {
		s := New(cfg, "t1")
		if err := s.Open(ctx); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		sc, err := s.Schema(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprint(sc.PrimaryKey) != "[id]" || !sc.Inferred || len(sc.Columns) != 3 {
			t.Fatalf("schema: %+v", sc)
		}
	})

	t.Run("missing topic", func(t *testing.T) {
		c := cfg
		c.Table = "nope"
		s := New(c, "t1")
		defer s.Close()
		if err := s.Open(ctx); !source.IsPermanent(err) {
			t.Fatalf("want permanent error, got %v", err)
		}
	})
}
