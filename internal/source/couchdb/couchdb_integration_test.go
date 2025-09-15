//go:build integration

package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	tccouchdb "github.com/testcontainers/testcontainers-go/modules/couchdb"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

// couch is a tiny test client.
type couch struct {
	t    *testing.T
	base string // with credentials
}

func (c couch) do(method, path string, body any) map[string]any {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.base+path, r)
	if err != nil {
		c.t.Fatal(err)
	}
	u, _ := url.Parse(c.base)
	if u.User != nil {
		p, _ := u.User.Password()
		req.SetBasicAuth(u.User.Username(), p)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if resp.StatusCode >= 300 && out["error"] != "file_exists" {
		c.t.Fatalf("%s %s: HTTP %d %v", method, path, resp.StatusCode, out)
	}
	return out
}

func startCouch(t *testing.T) couch {
	t.Helper()
	ctx := context.Background()
	ctr, err := tccouchdb.Run(ctx, "couchdb:3", tccouchdb.WithAdminCredentials("admin", "secret"))
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatal(err)
	}
	base, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c := couch{t: t, base: base}
	// System databases keep a single node quiet; ignore "already exists".
	for _, db := range []string{"_users", "_replicator"} {
		c.do(http.MethodPut, "/"+db, nil)
	}
	return c
}

func TestConformance(t *testing.T) {
	c := startCouch(t)
	c.do(http.MethodPut, "/shop", nil)
	// A design document must never surface as a row.
	c.do(http.MethodPut, "/shop/_design/v", map[string]any{"views": map[string]any{}})

	cfg := model.Source{Type: model.SourceCouchDB, DSN: c.base, Table: "shop"}
	rev := func(id int) string {
		return c.do(http.MethodGet, "/shop/"+strconv.Itoa(id), nil)["_rev"].(string)
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
			c.do(http.MethodPut, "/shop/"+strconv.Itoa(id), map[string]any{"name": name, "price": price})
		},
		Update: func(t *testing.T, id int, name string) {
			doc := c.do(http.MethodGet, "/shop/"+strconv.Itoa(id), nil)
			doc["name"] = name
			c.do(http.MethodPut, "/shop/"+strconv.Itoa(id), doc)
		},
		Delete: func(t *testing.T, id int) {
			c.do(http.MethodDelete, fmt.Sprintf("/shop/%d?rev=%s", id, url.QueryEscape(rev(id))), nil)
		},
		KeyColumn: "_id",
	})
}

func TestSchemaAndAttachments(t *testing.T) {
	c := startCouch(t)
	ctx := context.Background()
	c.do(http.MethodPut, "/docs", nil)
	c.do(http.MethodPut, "/docs/a", map[string]any{"title": "x", "n": 1,
		"_attachments": map[string]any{"f.txt": map[string]any{"content_type": "text/plain", "data": "aGk="}}})
	c.do(http.MethodPut, "/docs/_design/d", map[string]any{"language": "javascript"})

	s := New(model.Source{DSN: c.base, Database: "docs"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	schema, err := s.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if schema.PrimaryKey[0] != "_id" || !schema.Inferred || schema.RowCount != 2 {
		t.Fatalf("schema %+v", schema)
	}
	for _, col := range schema.Columns {
		if col.Name == "_attachments" || col.Name == "language" {
			t.Fatalf("unexpected column %+v", col)
		}
	}
	var rows []model.Event
	if err := s.Snapshot(ctx, func(_ context.Context, evs []model.Event) error { rows = append(rows, evs...); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Row["n"] != int64(1) || rows[0].Row["_rev"] == nil {
		t.Fatalf("snapshot %+v", rows)
	}
	if _, ok := rows[0].Row["_attachments"]; ok {
		t.Fatal("attachments not dropped")
	}

	// Deleting the database mid stream is permanent.
	pos, err := s.CurrentPosition(ctx)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	go func() { done <- s.Stream(sctx, pos, func(context.Context, []model.Event) error { return nil }) }()
	time.Sleep(300 * time.Millisecond)
	c.do(http.MethodDelete, "/docs", nil)
	select {
	case err := <-done:
		if !source.IsPermanent(err) {
			t.Fatalf("want permanent error, got %v", err)
		}
	case <-time.After(45 * time.Second):
		t.Fatal("stream did not notice the deleted database")
	}

	if err := New(model.Source{DSN: c.base, Table: "missing"}).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("missing db: %v", err)
	}
	u, _ := url.Parse(c.base)
	u.User = url.UserPassword("admin", "wrong")
	if err := New(model.Source{DSN: u.String(), Table: "docs"}).Open(ctx); !source.IsPermanent(err) {
		t.Fatalf("bad password: %v", err)
	}
}
