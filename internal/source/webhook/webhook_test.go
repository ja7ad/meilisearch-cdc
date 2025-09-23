package webhook

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func open(t *testing.T, taskID string) *Source {
	t.Helper()
	s, err := source.New(model.Source{Type: model.SourceWebhook}, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s.(*Source)
}

// waitStreaming waits until Push accepts events.
func waitStreaming(t *testing.T, s *Source) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		n := s.streaming
		s.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("stream did not start")
}

func TestPushStreamAck(t *testing.T) {
	s := open(t, "push-1")
	schema, err := s.Schema(context.Background())
	if err != nil || len(schema.PrimaryKey) != 1 || schema.PrimaryKey[0] != "id" || !schema.Inferred {
		t.Fatalf("schema = %+v, %v", schema, err)
	}
	start, _ := s.CurrentPosition(context.Background())
	if start != "0" {
		t.Fatalf("start position = %q", start)
	}
	if err := s.Snapshot(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan []model.Event, 10)
	done := make(chan error, 1)
	go func() {
		done <- s.Stream(ctx, start, func(ctx context.Context, evs []model.Event) error {
			got <- evs
			return nil
		})
	}()
	waitStreaming(t, s)

	type result struct {
		pos model.Position
		err error
	}
	res := make(chan result, 1)
	go func() {
		pos, err := Push(context.Background(), "push-1", []model.Event{
			{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "name": "a"}},
			{Op: model.OpDelete, Row: map[string]any{"id": int64(2)}},
		})
		res <- result{pos, err}
	}()

	var evs []model.Event
	select {
	case evs = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("no events streamed")
	}
	if len(evs) != 2 || evs[0].Position != "1" || evs[1].Position != "2" || evs[1].Op != model.OpDelete || evs[0].Time.IsZero() {
		t.Fatalf("unexpected events: %+v", evs)
	}
	// Push must still wait: only the first event is acknowledged.
	if err := s.Ack(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-res:
		t.Fatalf("push returned before full ack: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	if err := s.Ack(context.Background(), "2"); err != nil {
		t.Fatal(err)
	}
	select {
	case r := <-res:
		if r.err != nil || r.pos != "2" {
			t.Fatalf("push = %q, %v", r.pos, r.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("push did not return after ack")
	}
	if pos, _ := s.CurrentPosition(context.Background()); pos != "2" {
		t.Fatalf("current position = %q", pos)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream did not stop")
	}
	// Not streaming any more.
	if _, err := Push(context.Background(), "push-1", []model.Event{{Op: model.OpTruncate}}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
}

func TestPushNotRunning(t *testing.T) {
	if _, err := Push(context.Background(), "nope", nil); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
	s := open(t, "opened-not-streaming")
	if _, err := Push(context.Background(), "opened-not-streaming", []model.Event{{Op: model.OpTruncate}}); !errors.Is(err, ErrNotRunning) {
		t.Fatalf("want ErrNotRunning, got %v", err)
	}
	_ = s.Close()
	if lookup("opened-not-streaming") != nil {
		t.Fatal("close did not unregister")
	}
}

func TestPushContextTimeout(t *testing.T) {
	s := open(t, "timeout-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Stream(ctx, "", func(context.Context, []model.Event) error { return nil }) }()
	waitStreaming(t, s)

	pctx, pcancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer pcancel()
	start := time.Now()
	_, err := Push(pctx, "timeout-1", []model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(1)}}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline exceeded, got %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("push did not respect ctx")
	}
}

func TestCloseFailsWaitingPushers(t *testing.T) {
	s := open(t, "close-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Stream(ctx, "", func(context.Context, []model.Event) error { return nil }) }()
	waitStreaming(t, s)
	res := make(chan error, 1)
	go func() {
		_, err := Push(context.Background(), "close-1", []model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(1)}}})
		res <- err
	}()
	time.Sleep(50 * time.Millisecond)
	_ = s.Close()
	select {
	case err := <-res:
		if !errors.Is(err, ErrNotRunning) {
			t.Fatalf("want ErrNotRunning, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pusher not released by close")
	}
}

func TestBackPressureAndOrder(t *testing.T) {
	s := open(t, "order-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	var seen []model.Position
	release := make(chan struct{})
	go func() {
		_ = s.Stream(ctx, "", func(ctx context.Context, evs []model.Event) error {
			<-release // block the engine
			mu.Lock()
			for _, e := range evs {
				seen = append(seen, e.Position)
			}
			mu.Unlock()
			return s.Ack(ctx, evs[len(evs)-1].Position)
		})
	}()
	waitStreaming(t, s)
	const n = queueSize + 20
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := Push(context.Background(), "order-1", []model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(1)}}})
			errs <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != n {
		t.Fatalf("streamed %d events, want %d", len(seen), n)
	}
	for i, p := range seen {
		if p != formatPosition(uint64(i+1)) {
			t.Fatalf("event %d has position %s", i, p)
		}
	}
}

func TestEmitErrorKeepsBatch(t *testing.T) {
	s := open(t, "retry-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boom := errors.New("boom")
	done := make(chan error, 1)
	go func() {
		done <- s.Stream(ctx, "", func(context.Context, []model.Event) error { return boom })
	}()
	waitStreaming(t, s)
	res := make(chan error, 1)
	go func() {
		_, err := Push(context.Background(), "retry-1", []model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(7)}}})
		res <- err
	}()
	if err := <-done; !errors.Is(err, boom) {
		t.Fatalf("stream returned %v", err)
	}
	got := make(chan []model.Event, 1)
	go func() {
		_ = s.Stream(ctx, "", func(ctx context.Context, evs []model.Event) error {
			got <- evs
			return s.Ack(ctx, evs[len(evs)-1].Position)
		})
	}()
	select {
	case evs := <-got:
		if len(evs) != 1 || evs[0].Row["id"] != int64(7) {
			t.Fatalf("redelivered %+v", evs)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("failed batch not redelivered")
	}
	if err := <-res; err != nil {
		t.Fatal(err)
	}
}

func TestStreamResumesSequence(t *testing.T) {
	s := open(t, "resume-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan []model.Event, 1)
	go func() {
		_ = s.Stream(ctx, "41", func(ctx context.Context, evs []model.Event) error {
			got <- evs
			return s.Ack(ctx, evs[len(evs)-1].Position)
		})
	}()
	waitStreaming(t, s)
	pos, err := Push(context.Background(), "resume-1", []model.Event{{Op: model.OpTruncate}})
	if err != nil || pos != "42" {
		t.Fatalf("push = %q, %v", pos, err)
	}
	if evs := <-got; evs[0].Op != model.OpTruncate {
		t.Fatalf("got %+v", evs)
	}
	if err := s.Stream(ctx, "garbage", nil); !errors.Is(err, source.ErrPositionLost) {
		t.Fatalf("want ErrPositionLost, got %v", err)
	}
}

func TestPushValidates(t *testing.T) {
	s := open(t, "validate-1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = s.Stream(ctx, "", func(context.Context, []model.Event) error { return nil }) }()
	waitStreaming(t, s)
	if _, err := Push(context.Background(), "validate-1", []model.Event{{Op: model.OpUpsert}}); err == nil {
		t.Fatal("upsert without row accepted")
	}
	if pos, err := Push(context.Background(), "validate-1", nil); err != nil || pos != "0" {
		t.Fatalf("empty push = %q, %v", pos, err)
	}
}

func TestParsePayload(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []model.Event
	}{
		{"single", `{"op":"upsert","row":{"id":1,"name":"a","price":9.5,"n":2.0,"big":18446744073709551615,"tags":[1,"x"],"m":{"k":3}}}`,
			[]model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "name": "a", "price": 9.5, "n": int64(2),
				"big": uint64(18446744073709551615), "tags": []any{int64(1), "x"}, "m": map[string]any{"k": int64(3)}}}}},
		{"default op", ` {"row":{"id":"x"}}`, []model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": "x"}}}},
		{"array", `[{"op":"delete","row":{"id":2}},{"op":"truncate"},{"op":"UPDATE","row":{"id":3}}]`,
			[]model.Event{{Op: model.OpDelete, Row: map[string]any{"id": int64(2)}}, {Op: model.OpTruncate}, {Op: model.OpUpsert, Row: map[string]any{"id": int64(3)}}}},
		{"ndjson", "{\"op\":\"upsert\",\"row\":{\"id\":1}}\n{\"op\":\"delete\",\"row\":{\"id\":1}}\n\n",
			[]model.Event{{Op: model.OpUpsert, Row: map[string]any{"id": int64(1)}}, {Op: model.OpDelete, Row: map[string]any{"id": int64(1)}}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParsePayload(strings.NewReader(c.body))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(c.want) {
				t.Fatalf("got %d events, want %d", len(got), len(c.want))
			}
			for i := range got {
				if got[i].Op != c.want[i].Op || !equalRow(got[i].Row, c.want[i].Row) {
					t.Fatalf("event %d = %#v, want %#v", i, got[i], c.want[i])
				}
			}
		})
	}

	bad := []string{"", "   ", "[]", `{"op":"merge","row":{"id":1}}`, `{"op":"upsert"}`, `{"op":"delete","row":{}}`,
		`{"row":[1]}`, `[{"row":{"id":1}}] x`, `{"row":{"id":1}} garbage`, `not json`}
	for _, b := range bad {
		if _, err := ParsePayload(strings.NewReader(b)); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("ParsePayload(%q) = %v, want ErrInvalidPayload", b, err)
		}
	}
}

func equalRow(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !equalValue(v, b[k]) {
			return false
		}
	}
	return true
}

func equalValue(a, b any) bool {
	switch x := a.(type) {
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !equalValue(x[i], y[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		y, ok := b.(map[string]any)
		return ok && equalRow(x, y)
	}
	return a == b
}
