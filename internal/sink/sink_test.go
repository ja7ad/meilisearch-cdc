package sink

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

type recorder struct {
	mu   sync.Mutex
	acks []Ack
	dlq  []model.DLQEntry
}

func (r *recorder) hooks() Hooks {
	return Hooks{
		OnAck: func(a Ack) { r.mu.Lock(); r.acks = append(r.acks, a); r.mu.Unlock() },
		OnDLQ: func(e []model.DLQEntry) { r.mu.Lock(); r.dlq = append(r.dlq, e...); r.mu.Unlock() },
	}
}

func (r *recorder) positions() []model.Position {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []model.Position
	for _, a := range r.acks {
		if a.Position != "" {
			out = append(out, a.Position)
		}
	}
	return out
}

func cfg() Config {
	return Config{
		TaskID: "t1", PrimaryKey: "id", BatchSize: 100, FlushInterval: 20 * time.Millisecond, MaxInflight: 4,
		Retry:    retry.Policy{MaxAttempts: 5, Initial: time.Millisecond, Max: 5 * time.Millisecond},
		TaskPoll: time.Millisecond,
	}
}

func up(id int, fields map[string]any, pos int) transform.Change {
	doc := transform.Doc{"id": strconv.Itoa(id)}
	for k, v := range fields {
		doc[k] = v
	}
	return transform.Change{Op: model.OpUpsert, ID: strconv.Itoa(id), Doc: doc, Position: model.Position(fmt.Sprintf("%06d", pos)), Time: time.Now()}
}

func del(id, pos int) transform.Change {
	return transform.Change{Op: model.OpDelete, ID: strconv.Itoa(id), Position: model.Position(fmt.Sprintf("%06d", pos))}
}

// run feeds batches to a writer and closes the input (graceful drain).
func run(t *testing.T, f *fakeMeili, c Config, rec *recorder, batches ...[]transform.Change) error {
	t.Helper()
	w := New(f, f, c, rec.hooks())
	in := make(chan []transform.Change)
	errc := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { errc <- w.Run(ctx, in) }()
	for _, b := range batches {
		select {
		case in <- b:
		case err := <-errc:
			return err
		}
	}
	close(in)
	return <-errc
}

func TestCoalescingAndOrderedAcks(t *testing.T) {
	f := newFake("id")
	defer f.close()
	rec := &recorder{}
	c := cfg()
	c.BatchSize = 3
	var batches [][]transform.Change
	pos := 0
	for i := 0; i < 50; i++ {
		pos++
		batches = append(batches, []transform.Change{up(i%10, map[string]any{"v": i}, pos)})
	}
	pos++
	batches = append(batches, []transform.Change{del(3, pos)})
	require.NoError(t, run(t, f, c, rec, batches...))

	docs := f.snapshot()
	assert.Len(t, docs, 9)
	assert.NotContains(t, docs, "3")
	assert.Equal(t, 49, docs["9"]["v"])
	ps := rec.positions()
	require.NotEmpty(t, ps)
	for i := 1; i < len(ps); i++ {
		assert.Less(t, ps[i-1], ps[i], "acks must be ordered")
	}
	assert.Equal(t, model.Position(fmt.Sprintf("%06d", pos)), ps[len(ps)-1])
	for _, m := range f.metadata {
		assert.Equal(t, "cdc:t1", m)
	}
}

func TestCoalesceWithinBatch(t *testing.T) {
	f := newFake("id")
	defer f.close()
	rec := &recorder{}
	c := cfg()
	c.FlushInterval = time.Hour
	require.NoError(t, run(t, f, c, rec, []transform.Change{
		up(1, map[string]any{"v": 1}, 1), up(1, map[string]any{"v": 2}, 2), del(1, 3), up(1, map[string]any{"v": 4}, 4), up(2, nil, 5),
	}))
	assert.Equal(t, 4, f.snapshot()["1"]["v"])
	require.Len(t, rec.acks, 1)
	assert.Equal(t, 3, rec.acks[0].Coalesced)
	assert.Equal(t, 2, rec.acks[0].Upserted)
	assert.Equal(t, 1, f.enqueues, "one request for the whole batch")
}

func TestUpsertModeDeleteThenInsertReplaces(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.docs["1"] = map[string]any{"id": "1", "stale": true}
	rec := &recorder{}
	c := cfg()
	c.WriteMode = model.WriteUpsert
	c.FlushInterval = time.Hour
	require.NoError(t, run(t, f, c, rec, []transform.Change{del(1, 1), up(1, map[string]any{"v": 1}, 2)}))
	assert.Equal(t, map[string]any{"id": "1", "v": 1}, f.snapshot()["1"])

	// Plain partial updates merge.
	require.NoError(t, run(t, f, c, rec, []transform.Change{up(1, map[string]any{"w": 2}, 3)}))
	assert.Equal(t, map[string]any{"id": "1", "v": 1, "w": 2}, f.snapshot()["1"])
}

func TestPoisonDocumentGoesToDLQ(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.reject = func(d map[string]any) string {
		if d["bad"] == true {
			return string(meilisearch.APIErrCodeInvalidDocumentGeoField)
		}
		return ""
	}
	rec := &recorder{}
	var changes []transform.Change
	for i := 0; i < 40; i++ {
		changes = append(changes, up(i, map[string]any{"bad": i == 17 || i == 31}, i+1))
	}
	require.NoError(t, run(t, f, cfg(), rec, changes))
	docs := f.snapshot()
	assert.Len(t, docs, 38)
	require.Len(t, rec.dlq, 2)
	ids := []string{rec.dlq[0].DocID, rec.dlq[1].DocID}
	assert.ElementsMatch(t, []string{"17", "31"}, ids)
	assert.Equal(t, "invalid_document_geo_field", rec.dlq[0].Code)
	assert.Equal(t, "t1", rec.dlq[0].TaskID)
	assert.NotNil(t, rec.dlq[0].Document)
	assert.Equal(t, model.Position("000040"), rec.positions()[len(rec.positions())-1], "the stream moves on")
}

func TestTransientEnqueueErrorsAreRetried(t *testing.T) {
	f := newFake("id")
	defer f.close()
	fails := 2
	f.enqueueErr = func(int) error {
		if fails > 0 {
			fails--
			return &meilisearch.Error{StatusCode: 503}
		}
		return nil
	}
	rec := &recorder{}
	retries := 0
	c := cfg()
	w := New(f, f, c, Hooks{OnAck: rec.hooks().OnAck, OnRetry: func(int, error) { retries++ }})
	in := make(chan []transform.Change, 1)
	in <- []transform.Change{up(1, nil, 1)}
	close(in)
	require.NoError(t, w.Run(context.Background(), in))
	assert.Len(t, f.snapshot(), 1)
	assert.Equal(t, 2, retries)
}

func TestPermanentErrorStopsWriter(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.enqueueErr = func(int) error {
		return &meilisearch.Error{StatusCode: 401, APIError: meilisearch.APIErrorDetails{Code: "invalid_api_key"}}
	}
	err := run(t, f, cfg(), &recorder{}, []transform.Change{up(1, nil, 1)})
	require.Error(t, err)
	assert.False(t, IsHold(err))
	assert.Empty(t, f.snapshot())
}

func TestPayloadTooLargeSplits(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.enqueueErr = func(n int) error {
		if n > 8 {
			return &meilisearch.Error{StatusCode: 413, APIError: meilisearch.APIErrorDetails{Code: meilisearch.APIErrCodePayloadTooLarge}}
		}
		return nil
	}
	var changes []transform.Change
	for i := 0; i < 50; i++ {
		changes = append(changes, up(i, nil, i+1))
	}
	require.NoError(t, run(t, f, cfg(), &recorder{}, changes))
	assert.Len(t, f.snapshot(), 50)
}

func TestFailedTaskIsReappliedWithoutOverwritingNewerData(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.failNext = 1 // the first task fails as a whole
	f.processWait = 30 * time.Millisecond
	rec := &recorder{}
	c := cfg()
	c.BatchSize = 2
	c.FlushInterval = time.Hour
	require.NoError(t, run(t, f, c, rec,
		[]transform.Change{up(1, map[string]any{"v": "old"}, 1), up(2, map[string]any{"v": "old"}, 2)},
		[]transform.Change{up(1, map[string]any{"v": "new"}, 3), up(3, nil, 4)},
	))
	docs := f.snapshot()
	assert.Equal(t, "new", docs["1"]["v"], "the replay must skip ids carried by later batches")
	assert.Equal(t, "old", docs["2"]["v"])
	assert.Contains(t, docs, "3")
	assert.Equal(t, []model.Position{"000002", "000004"}, rec.positions())
}

func TestMassDeleteGuardHolds(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.seed(2000)
	c := cfg()
	c.BatchSize = 5000
	c.FlushInterval = time.Hour
	c.Safety = model.Safety{MaxDeleteRatio: 0.5, MinDocsForDeleteGuard: 1000}
	var changes []transform.Change
	for i := 0; i < 1500; i++ {
		changes = append(changes, del(i, i+1))
	}
	err := run(t, f, c, &recorder{}, changes)
	require.Error(t, err)
	assert.True(t, IsHold(err))
	assert.Len(t, f.snapshot(), 2000, "nothing was deleted")

	// Below the ratio deletes go through.
	require.NoError(t, run(t, f, c, &recorder{}, changes[:100]))
	assert.Len(t, f.snapshot(), 1900)
}

func TestTruncate(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.seed(10)
	c := cfg()
	trunc := transform.Change{Op: model.OpTruncate, Position: "000009"}
	err := run(t, f, c, &recorder{}, []transform.Change{up(100, nil, 1), trunc})
	assert.True(t, IsHold(err))
	assert.Contains(t, f.snapshot(), "100", "changes before the truncate were applied")
	assert.Len(t, f.snapshot(), 11)

	c.Safety.AllowTruncate = true
	rec := &recorder{}
	require.NoError(t, run(t, f, c, rec, []transform.Change{trunc, up(5, nil, 10)}))
	assert.Equal(t, []string{"5"}, keys(f.snapshot()))
	assert.True(t, rec.acks[0].Truncate)
}

func TestCancelDoesNotAck(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.processWait = 200 * time.Millisecond
	rec := &recorder{}
	c := cfg()
	c.FlushInterval = time.Millisecond
	w := New(f, f, c, rec.hooks())
	in := make(chan []transform.Change, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Run(ctx, in) }()
	in <- []transform.Change{up(1, nil, 1)}
	time.Sleep(50 * time.Millisecond)
	cancel()
	assert.ErrorIs(t, <-done, context.Canceled)
	assert.Empty(t, rec.positions())
}

func TestPositionOnlyChangesAreAcked(t *testing.T) {
	f := newFake("id")
	defer f.close()
	rec := &recorder{}
	require.NoError(t, run(t, f, cfg(), rec, []transform.Change{{Position: "000007"}}))
	assert.Equal(t, []model.Position{"000007"}, rec.positions())
	assert.Zero(t, f.enqueues)
}

func TestApplierDeletesBisect(t *testing.T) {
	f := newFake("id")
	defer f.close()
	f.seed(5)
	bad := &meilisearch.Error{StatusCode: 400, APIError: meilisearch.APIErrorDetails{Code: meilisearch.APIErrCodeInvalidDocumentID}}
	a := NewApplier(f, f, cfg())
	dlq, err := a.ApplySync(context.Background(), nil, nil, []string{"1", "2"})
	require.NoError(t, err)
	assert.Empty(t, dlq)
	assert.Len(t, f.snapshot(), 3)

	f.enqueueErr = func(n int) error { return bad }
	dlq, err = a.ApplySync(context.Background(), nil, nil, []string{"3", "4"})
	require.NoError(t, err)
	assert.Len(t, dlq, 2)
	assert.Equal(t, "delete", dlq[0].Op)
}

func TestClassify(t *testing.T) {
	assert.Equal(t, kindRetry, classifyRequestError(fmt.Errorf("dial tcp: refused")))
	assert.Equal(t, kindRetry, classifyRequestError(&meilisearch.Error{StatusCode: 0}))
	assert.Equal(t, kindRetry, classifyRequestError(&meilisearch.Error{StatusCode: 429}))
	assert.Equal(t, kindRetry, classifyRequestError(&meilisearch.Error{StatusCode: 502}))
	assert.Equal(t, kindTooLarge, classifyRequestError(&meilisearch.Error{StatusCode: 413}))
	assert.Equal(t, kindFatal, classifyRequestError(&meilisearch.Error{StatusCode: 403}))
	assert.Equal(t, kindFatal, classifyRequestError(context.Canceled))
	assert.Equal(t, kindDocument, classifyTaskError("invalid_document_id", ""))
	assert.Equal(t, kindDocument, classifyTaskError("invalid_vectors_type", ""))
	assert.Equal(t, kindFatal, classifyTaskError("index_primary_key_already_exists", ""))
	assert.Equal(t, kindRetry, classifyTaskError("internal", "an internal error occurred: io error"))
	assert.Equal(t, kindTooLarge, classifyTaskError("payload_too_large", ""))
	// Seen on Meilisearch 1.56 for a document whose _vectors is not a map.
	assert.Equal(t, kindDocument, classifyTaskError("internal", "Index `x`: invalid type: string \"bad\", expected a map at line 1 column 5"))
	assert.Equal(t, kindDocument, classifyTaskError("internal", "Index `x`: invalid type: null, expected a map at line 1 column 4"))
}

func keys(m map[string]map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
