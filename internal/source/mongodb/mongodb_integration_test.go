//go:build integration

package mongodb

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tcmongo "github.com/testcontainers/testcontainers-go/modules/mongodb"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func startMongo(t *testing.T, opts ...testcontainers.ContainerCustomizer) string {
	t.Helper()
	ctx := context.Background()
	c, err := tcmongo.Run(ctx, "mongo:7", opts...)
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	uri, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// The replica set advertises the container address, which the host
	// cannot always reach (Docker Desktop); talk to the mapped port only.
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("directConnection", "true")
	u.RawQuery = q.Encode()
	return u.String()
}

func TestConformance(t *testing.T) {
	uri := startMongo(t, tcmongo.WithReplicaSet("rs0"))
	ctx := context.Background()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Disconnect(ctx)
	coll := client.Database("shop").Collection("items")

	cfg := model.Source{Type: model.SourceMongoDB, DSN: uri, Database: "shop", Table: "items"}
	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			if _, err := coll.InsertOne(ctx, bson.D{{Key: "_id", Value: id}, {Key: "name", Value: name}, {Key: "price", Value: price}}); err != nil {
				t.Fatal(err)
			}
		},
		Update: func(t *testing.T, id int, name string) {
			if _, err := coll.UpdateByID(ctx, id, bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: name}}}}); err != nil {
				t.Fatal(err)
			}
		},
		Delete: func(t *testing.T, id int) {
			if _, err := coll.DeleteOne(ctx, bson.D{{Key: "_id", Value: id}}); err != nil {
				t.Fatal(err)
			}
		},
		KeyColumn: "_id",
	})

	t.Run("drop invalidates", func(t *testing.T) {
		s := New(cfg)
		if err := s.Open(ctx); err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		pos, err := s.CurrentPosition(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := coll.Drop(ctx); err != nil {
			t.Fatal(err)
		}
		var truncated bool
		sctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		err = s.Stream(sctx, pos, func(_ context.Context, evs []model.Event) error {
			for _, e := range evs {
				truncated = truncated || e.Op == model.OpTruncate
			}
			return nil
		})
		if !source.IsPermanent(err) || !truncated {
			t.Fatalf("want truncate then permanent error, got truncate=%v err=%v", truncated, err)
		}
	})
}

func TestStandaloneIsPermanent(t *testing.T) {
	uri := startMongo(t)
	s := New(model.Source{DSN: uri, Database: "shop", Table: "items"})
	defer s.Close()
	if err := s.Open(context.Background()); !source.IsPermanent(err) {
		t.Fatalf("want permanent error, got %v", err)
	}
}
