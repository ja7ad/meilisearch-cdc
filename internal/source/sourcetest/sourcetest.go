// Package sourcetest is a conformance suite every connector runs in its
// integration tests, so all sources behave the same way for the engine.
package sourcetest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// Harness adapts a concrete database to the suite. The table must have a key
// column (KeyColumn, default "id") and the columns "name" (text) and
// "price" (number).
type Harness struct {
	// NewSource returns a new, unopened connector for the same table and task
	// id each time it is called (it is called again to test resuming).
	NewSource func(t *testing.T) source.Source
	Insert    func(t *testing.T, id int, name string, price float64)
	Update    func(t *testing.T, id int, name string)
	Delete    func(t *testing.T, id int)
	KeyColumn string
	// SkipResume disables the resume step for sources that cannot resume.
	SkipResume bool
	// SkipDelete skips the hard delete check for sources that cannot see
	// deletes (polling); Delete is still called.
	SkipDelete bool
	// Timeout bounds every wait (default 60s).
	Timeout time.Duration
}

type collector struct {
	mu     sync.Mutex
	events []model.Event
}

func (c *collector) emit(_ context.Context, evs []model.Event) error {
	c.mu.Lock()
	c.events = append(c.events, evs...)
	c.mu.Unlock()
	return nil
}

func (c *collector) snapshot() []model.Event {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]model.Event(nil), c.events...)
}

func key(v any) string { return fmt.Sprint(v) }

// Run executes the suite.
func Run(t *testing.T, h Harness) {
	t.Helper()
	if h.KeyColumn == "" {
		h.KeyColumn = "id"
	}
	if h.Timeout == 0 {
		h.Timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*h.Timeout)
	defer cancel()

	for i := 1; i <= 3; i++ {
		h.Insert(t, i, fmt.Sprintf("item-%d", i), float64(i)*10)
	}

	src := h.NewSource(t)
	if err := src.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	schema, err := src.Schema(ctx)
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	if !hasColumn(schema, h.KeyColumn) || !hasColumn(schema, "name") {
		t.Fatalf("schema misses columns: %+v", schema.Columns)
	}
	if len(schema.PrimaryKey) == 0 {
		t.Fatalf("schema has no primary key")
	}
	start, err := src.CurrentPosition(ctx)
	if err != nil {
		t.Fatalf("current position: %v", err)
	}

	snap := &collector{}
	if err := src.Snapshot(ctx, snap.emit); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	rows := snap.snapshot()
	if len(rows) != 3 {
		t.Fatalf("snapshot returned %d rows, want 3", len(rows))
	}
	for _, e := range rows {
		if e.Op != model.OpUpsert || !e.Snapshot {
			t.Fatalf("snapshot event must be a snapshot upsert: %+v", e)
		}
		if e.Row["name"] == nil {
			t.Fatalf("snapshot row misses name: %+v", e.Row)
		}
	}

	stream := &collector{}
	sctx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- src.Stream(sctx, start, stream.emit) }()

	// Give log based sources a moment to attach before writing.
	time.Sleep(500 * time.Millisecond)
	h.Insert(t, 4, "item-4", 40)
	h.Update(t, 1, "renamed-1")
	h.Delete(t, 2)

	var last model.Position
	waitFor(t, h.Timeout, "stream insert/update/delete", func() bool {
		evs := stream.snapshot()
		var ins, upd, del bool
		for _, e := range evs {
			id := key(e.Row[h.KeyColumn])
			switch {
			case e.Op == model.OpUpsert && id == "4":
				ins = true
			case e.Op == model.OpUpsert && id == "1" && e.Row["name"] == "renamed-1":
				upd = true
			case e.Op == model.OpDelete && id == "2":
				del = true
			case h.SkipDelete:
				del = true
			}
			if e.Position != "" {
				last = e.Position
			}
		}
		return ins && upd && del
	})
	if last == "" {
		t.Fatalf("stream events carry no position")
	}
	for _, e := range stream.snapshot() {
		if e.Snapshot {
			t.Fatalf("stream event marked as snapshot: %+v", e)
		}
	}
	if err := src.Ack(ctx, last); err != nil {
		t.Fatalf("ack: %v", err)
	}
	stop()
	select {
	case err := <-done:
		if err != nil && ctx.Err() == nil && sctx.Err() == nil {
			t.Fatalf("stream returned: %v", err)
		}
	case <-time.After(h.Timeout):
		t.Fatalf("stream did not stop after cancel")
	}
	if err := src.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	if h.SkipResume {
		return
	}

	// Changes made while the task is down must be delivered after resume.
	h.Insert(t, 5, "item-5", 50)
	src2 := h.NewSource(t)
	if err := src2.Open(ctx); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer src2.Close()
	resumed := &collector{}
	rctx, rstop := context.WithCancel(ctx)
	defer rstop()
	go func() { _ = src2.Stream(rctx, last, resumed.emit) }()
	waitFor(t, h.Timeout, "resumed insert", func() bool {
		for _, e := range resumed.snapshot() {
			if e.Op == model.OpUpsert && key(e.Row[h.KeyColumn]) == "5" {
				return true
			}
		}
		return false
	})
	if d, ok := src2.(source.Dropper); ok {
		rstop()
		time.Sleep(200 * time.Millisecond)
		if err := d.Drop(context.Background()); err != nil {
			t.Fatalf("drop: %v", err)
		}
	}
}

func hasColumn(s *model.Schema, name string) bool {
	for _, c := range s.Columns {
		if c.Name == name {
			return true
		}
	}
	return false
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
