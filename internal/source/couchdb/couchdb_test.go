package couchdb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func TestSeqString(t *testing.T) {
	for raw, want := range map[string]string{`"12-g1AA"`: "12-g1AA", `42`: "42", `null`: "", ``: ""} {
		if got := seqString(json.RawMessage(raw)); got != want {
			t.Errorf("seqString(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestCleanDoc(t *testing.T) {
	var doc map[string]any
	dec := json.NewDecoder(strings.NewReader(`{"_id":"1","_rev":"1-a","_attachments":{"f":{"stub":true}},"n":3,"p":1.5,"a":[1,{"b":2}]}`))
	dec.UseNumber()
	if err := dec.Decode(&doc); err != nil {
		t.Fatal(err)
	}
	got := cleanDoc(doc)
	if _, ok := got["_attachments"]; ok {
		t.Fatal("attachments kept")
	}
	if got["_rev"] != "1-a" || got["n"] != int64(3) || got["p"] != 1.5 {
		t.Fatalf("got %#v", got)
	}
	if inner := got["a"].([]any)[1].(map[string]any); inner["b"] != int64(2) {
		t.Fatalf("nested not normalised: %#v", got["a"])
	}
}

func TestInferColumns(t *testing.T) {
	cols := inferColumns([]map[string]any{{"_id": "1", "name": "a"}, {"_id": "2", "price": 1.5}})
	if len(cols) != 3 || cols[0].Name != "_id" || !cols[0].PrimaryKey || cols[0].Nullable || !cols[1].Nullable {
		t.Fatalf("got %+v", cols)
	}
}

func TestOpenErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, _ := r.BasicAuth()
		switch {
		case u != "admin" || p != "pw":
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","reason":"Name or password is incorrect."}`))
		case r.URL.EscapedPath() == "/a%2Fb":
			w.Write([]byte(`{"doc_count":1,"update_seq":"5-x"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"not_found","reason":"Database does not exist."}`))
		}
	}))
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	ctx := context.Background()

	for name, cfg := range map[string]model.Source{
		"no dsn":   {Table: "x"},
		"no db":    {DSN: srv.URL},
		"bad dsn":  {DSN: "ftp://x", Table: "x"},
		"auth":     {DSN: "http://admin:bad@" + host, Table: "a/b"},
		"notfound": {DSN: "http://admin:pw@" + host, Table: "missing"},
	} {
		if err := New(cfg).Open(ctx); !source.IsPermanent(err) {
			t.Errorf("%s: want permanent error, got %v", name, err)
		}
	}
	s := New(model.Source{DSN: "http://admin:pw@" + host, Database: "a/b"})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if pos, err := s.CurrentPosition(ctx); err != nil || pos != "5-x" {
		t.Fatalf("position %q %v", pos, err)
	}
}

// fakeChanges serves a scripted _changes feed.
type fakeChanges struct {
	mu      sync.Mutex
	replies []func(w http.ResponseWriter, since string)
	sinces  []string
}

func (f *fakeChanges) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasSuffix(r.URL.Path, "/_changes") {
		w.Write([]byte(`{"doc_count":0,"update_seq":"0"}`))
		return
	}
	q := r.URL.Query()
	if q.Get("feed") != "longpoll" || q.Get("include_docs") != "true" || q.Get("style") != "main_only" {
		http.Error(w, "bad query", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.sinces = append(f.sinces, q.Get("since"))
	var reply func(http.ResponseWriter, string)
	if len(f.replies) > 0 {
		reply, f.replies = f.replies[0], f.replies[1:]
	}
	f.mu.Unlock()
	if reply == nil {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		w.Write([]byte(`{"results":[],"last_seq":"` + q.Get("since") + `"}`))
		return
	}
	reply(w, q.Get("since"))
}

func TestStream(t *testing.T) {
	f := &fakeChanges{replies: []func(http.ResponseWriter, string){
		func(w http.ResponseWriter, _ string) {
			w.Write([]byte(`{"results":[
				{"seq":"2-a","id":"1","changes":[{"rev":"2-x"}],"doc":{"_id":"1","_rev":"2-x","name":"n","price":10}},
				{"seq":"3-a","id":"_design/v","changes":[],"doc":{"_id":"_design/v"}},
				{"seq":"4-a","id":"2","deleted":true,"changes":[],"doc":{"_id":"2","_rev":"3-y","_deleted":true}}
			],"last_seq":"4-a","pending":0}`))
		},
		func(w http.ResponseWriter, _ string) { w.WriteHeader(http.StatusBadGateway) }, // transient
		func(w http.ResponseWriter, _ string) {
			w.Write([]byte(`{"results":[{"seq":"5-a","id":"3","changes":[],"doc":{"_id":"3","name":"m"}}],"last_seq":"5-a"}`))
		},
		func(w http.ResponseWriter, _ string) {
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"error":"unauthorized","reason":"session expired"}`))
		},
	}}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := New(model.Source{DSN: srv.URL, Table: "db"})
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	var got []model.Event
	err := s.Stream(context.Background(), "1-a", func(_ context.Context, evs []model.Event) error {
		got = append(got, evs...)
		return nil
	})
	if !source.IsPermanent(err) {
		t.Fatalf("want permanent error after 401, got %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events: %+v", len(got), got)
	}
	if got[0].Op != model.OpUpsert || got[0].Position != "2-a" || got[0].Row["price"] != int64(10) || got[0].Row["_rev"] != "2-x" {
		t.Fatalf("event 0: %+v", got[0])
	}
	if got[1].Op != model.OpDelete || got[1].Position != "4-a" || len(got[1].Row) != 1 || got[1].Row["_id"] != "2" {
		t.Fatalf("event 1: %+v", got[1])
	}
	if got[2].Position != "5-a" {
		t.Fatalf("event 2: %+v", got[2])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := []string{"1-a", "4-a", "4-a", "5-a"}
	if strings.Join(f.sinces, ",") != strings.Join(want, ",") {
		t.Fatalf("since values %v, want %v", f.sinces, want)
	}
}

func TestStreamCancelAndLostPosition(t *testing.T) {
	f := &fakeChanges{}
	srv := httptest.NewServer(f)
	defer srv.Close()
	s := New(model.Source{DSN: srv.URL, Table: "db"})
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := s.Stream(ctx, "", func(context.Context, []model.Event) error { return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("stream ignored cancellation")
	}
	f.mu.Lock()
	first := f.sinces[0]
	f.mu.Unlock()
	if first != "now" {
		t.Fatalf("empty position must start at now, got %q", first)
	}

	f.mu.Lock()
	f.replies = append(f.replies, func(w http.ResponseWriter, _ string) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad_request","reason":"Malformed sequence supplied in 'since' parameter."}`))
	})
	f.mu.Unlock()
	if err := s.Stream(context.Background(), "garbage", nil); !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("want ErrPositionLost, got %v", err)
	}
}

func TestSnapshotPaging(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		q := r.URL.Query()
		if q.Get("startkey") == "" {
			// A full page: 999 docs plus a design doc.
			var rows []string
			rows = append(rows, `{"id":"_design/a","doc":{"_id":"_design/a"}}`)
			for i := 0; i < pageSize-1; i++ {
				rows = append(rows, `{"id":"d`+pad(i)+`","doc":{"_id":"d`+pad(i)+`","name":"x"}}`)
			}
			w.Write([]byte(`{"rows":[` + strings.Join(rows, ",") + `]}`))
			return
		}
		if q.Get("skip") != "1" || q.Get("startkey") != `"d`+pad(pageSize-2)+`"` {
			http.Error(w, "bad page query "+r.URL.RawQuery, http.StatusBadRequest)
			return
		}
		w.Write([]byte(`{"rows":[{"id":"z","doc":{"_id":"z","name":"y"}}]}`))
	}))
	defer srv.Close()
	s := New(model.Source{DSN: srv.URL, Table: "db"})
	s.base = srv.URL
	var n int
	err := s.Snapshot(context.Background(), func(_ context.Context, evs []model.Event) error {
		for _, e := range evs {
			if !e.Snapshot || e.Op != model.OpUpsert || strings.HasPrefix(e.Row["_id"].(string), "_design/") {
				t.Fatalf("bad event %+v", e)
			}
		}
		n += len(evs)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != pageSize {
		t.Fatalf("got %d docs, want %d (queries %v)", n, pageSize, queries)
	}
}

func pad(i int) string { return fmt.Sprintf("%04d", i) }
