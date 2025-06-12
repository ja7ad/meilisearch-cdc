// Package sink writes transformed changes to a Meilisearch index with
// batching, coalescing, pipelined tasks, ordered acknowledgements, retries,
// poison document isolation and safety guards.
package sink

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Index is the part of meilisearch.IndexManager the writer needs.
type Index interface {
	AddDocumentsNdjson(ctx context.Context, documents []byte, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error)
	UpdateDocumentsNdjson(ctx context.Context, documents []byte, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error)
	DeleteDocuments(ctx context.Context, identifiers []string, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error)
	DeleteAllDocuments(ctx context.Context, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error)
	GetStats(ctx context.Context, param *meilisearch.StatsParams) (*meilisearch.StatsIndex, error)
}

// Tasks is the part of meilisearch.ServiceManager used to follow tasks.
type Tasks interface {
	GetTask(ctx context.Context, taskUID int64) (*meilisearch.Task, error)
}

// Config configures a Writer.
type Config struct {
	TaskID        string
	PrimaryKey    string
	WriteMode     model.WriteMode
	BatchSize     int
	FlushInterval time.Duration
	MaxInflight   int
	Retry         retry.Policy
	Safety        model.Safety
	// TaskPoll is the first polling interval for task completion; it backs
	// off up to 500ms.
	TaskPoll time.Duration
}

func (c Config) withDefaults() Config {
	if c.BatchSize <= 0 {
		c.BatchSize = 1000
	}
	if c.FlushInterval <= 0 {
		c.FlushInterval = time.Second
	}
	if c.MaxInflight <= 0 {
		c.MaxInflight = 4
	}
	if c.TaskPoll <= 0 {
		c.TaskPoll = 10 * time.Millisecond
	}
	if c.WriteMode == "" {
		c.WriteMode = model.WriteReplace
	}
	if c.Safety.MinDocsForDeleteGuard <= 0 {
		c.Safety.MinDocsForDeleteGuard = 1000
	}
	return c
}

// Ack reports a batch confirmed by Meilisearch. Acks are delivered in the
// order the changes were written.
type Ack struct {
	Position  model.Position
	Time      time.Time // newest source commit time in the batch
	Upserted  int
	Deleted   int
	Coalesced int
	DLQ       int
	Snapshot  bool
	Truncate  bool
	Duration  time.Duration
}

// Hooks receive writer notifications. All are optional and called from the
// writer goroutines, so they must be fast and concurrency safe.
type Hooks struct {
	OnAck      func(Ack)
	OnDLQ      func([]model.DLQEntry)
	OnRetry    func(attempt int, err error)
	OnInflight func(n int)
}

// Writer streams changes into one index.
type Writer struct {
	cfg   Config
	ap    *Applier
	hooks Hooks
	buf   *batch

	mu      sync.Mutex
	cond    *sync.Cond
	queue   []*flight
	closed  bool
	fatal   error
	seq     uint64
	counted struct {
		at time.Time
		n  int64
	}
}

// New returns a Writer.
func New(idx Index, tasks Tasks, cfg Config, hooks Hooks) *Writer {
	cfg = cfg.withDefaults()
	w := &Writer{cfg: cfg, hooks: hooks, buf: newBatch(cfg.WriteMode)}
	w.ap = &Applier{idx: idx, tasks: tasks, cfg: cfg, onRetry: hooks.OnRetry}
	w.cond = sync.NewCond(&w.mu)
	return w
}

// Run consumes change batches until in is closed (pending changes are
// flushed and confirmed, then Run returns nil) or ctx is cancelled
// (unconfirmed changes are dropped and will be replayed from the last ack).
func (w *Writer) Run(ctx context.Context, in <-chan []transform.Change) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)

	trackerDone := make(chan struct{})
	go func() {
		defer close(trackerDone)
		if err := w.track(ctx); err != nil {
			cancel(err)
		}
	}()
	defer func() {
		w.mu.Lock()
		w.closed = true
		w.cond.Broadcast()
		w.mu.Unlock()
		cancel(nil)
		<-trackerDone
	}()

	ticker := time.NewTicker(w.cfg.FlushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return cause(ctx)
		case <-ticker.C:
			if err := w.flush(ctx); err != nil {
				return err
			}
		case chs, ok := <-in:
			if !ok {
				if err := w.flush(ctx); err != nil {
					return err
				}
				return w.drain(ctx)
			}
			for _, c := range chs {
				if c.Op == model.OpTruncate {
					if err := w.truncate(ctx, c); err != nil {
						return err
					}
					continue
				}
				w.buf.add(c)
				if w.buf.size() >= w.cfg.BatchSize {
					if err := w.flush(ctx); err != nil {
						return err
					}
				}
			}
			// A batch without documents (only positions) still needs an ack.
			if w.buf.size() == 0 && w.buf.pos != "" {
				if err := w.flush(ctx); err != nil {
					return err
				}
			}
		}
	}
}

func cause(ctx context.Context) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	return ctx.Err()
}

// flight is a batch enqueued in Meilisearch and not yet confirmed.
type flight struct {
	seq     uint64
	upserts []transform.Doc
	replace []transform.Doc // upsert mode: documents that must be replaced
	merge   []transform.Doc // replace mode: partial rows merged into the document
	deletes []string
	ids     map[string]struct{}
	ack     Ack
	started time.Time
	tasks   []int64
	ready   chan struct{} // closed once enqueueing finished
	enqErr  error
}

func (f *flight) docIDs(pk string) map[string]struct{} {
	if f.ids != nil {
		return f.ids
	}
	f.ids = make(map[string]struct{}, len(f.upserts)+len(f.replace)+len(f.merge)+len(f.deletes))
	for _, d := range f.upserts {
		f.ids[docID(d, pk)] = struct{}{}
	}
	for _, d := range f.merge {
		f.ids[docID(d, pk)] = struct{}{}
	}
	for _, d := range f.replace {
		f.ids[docID(d, pk)] = struct{}{}
	}
	for _, id := range f.deletes {
		f.ids[id] = struct{}{}
	}
	return f.ids
}

func docID(d transform.Doc, pk string) string { return fmt.Sprint(d[pk]) }

// flush enqueues the buffered batch.
func (w *Writer) flush(ctx context.Context) error {
	if w.buf.empty() {
		return nil
	}
	b := w.buf
	w.buf = newBatch(w.cfg.WriteMode)
	f := b.toFlight()

	if len(f.deletes) > 0 {
		if err := w.guardDeletes(ctx, len(f.deletes)); err != nil {
			return err
		}
	}

	// Wait for room, then register the flight before enqueueing so the
	// tracker always sees every batch that might overlap a retried one.
	w.mu.Lock()
	for len(w.queue) >= w.cfg.MaxInflight && !w.closed && w.fatal == nil && ctx.Err() == nil {
		w.cond.Wait()
	}
	if w.fatal != nil {
		err := w.fatal
		w.mu.Unlock()
		return err
	}
	if ctx.Err() != nil || w.closed {
		w.mu.Unlock()
		return cause(ctx)
	}
	w.seq++
	f.seq = w.seq
	f.started = time.Now()
	w.queue = append(w.queue, f)
	n := len(w.queue)
	w.cond.Broadcast()
	w.mu.Unlock()
	if w.hooks.OnInflight != nil {
		w.hooks.OnInflight(n)
	}

	f.tasks, f.enqErr = w.ap.enqueue(ctx, f.upserts, f.replace, f.merge, f.deletes)
	close(f.ready)
	if f.enqErr != nil && classifyRequestError(f.enqErr) == kindFatal {
		if ctx.Err() != nil {
			return cause(ctx)
		}
		return f.enqErr
	}
	return nil
}

// guardDeletes holds the pipeline when a single flush deletes too large a
// share of the index.
func (w *Writer) guardDeletes(ctx context.Context, n int) error {
	ratio := w.cfg.Safety.MaxDeleteRatio
	if ratio <= 0 {
		return nil
	}
	if time.Since(w.counted.at) > 10*time.Second {
		st, err := w.ap.idx.GetStats(ctx, nil)
		if err != nil {
			// Not knowing the size is not a reason to stop; the next flush retries.
			return nil
		}
		w.counted.at, w.counted.n = time.Now(), st.NumberOfDocuments
	}
	total := w.counted.n
	if total < w.cfg.Safety.MinDocsForDeleteGuard {
		return nil
	}
	if float64(n)/float64(total) > ratio {
		return &ErrHold{Reason: fmt.Sprintf("a single batch would delete %d of %d documents (%.0f%% > %.0f%% limit)",
			n, total, 100*float64(n)/float64(total), 100*ratio)}
	}
	return nil
}

// truncate waits for every pending batch, then clears the index if allowed.
func (w *Writer) truncate(ctx context.Context, c transform.Change) error {
	if err := w.flush(ctx); err != nil {
		return err
	}
	if err := w.drain(ctx); err != nil {
		return err
	}
	if !w.cfg.Safety.AllowTruncate {
		return &ErrHold{Reason: "the source table was truncated; enable safety.allowTruncate or resync the task"}
	}
	start := time.Now()
	err := retry.Run(ctx, w.ap.policy(), func(ctx context.Context, _ int) error {
		info, err := w.ap.idx.DeleteAllDocuments(ctx, w.ap.deleteOpts())
		if err != nil {
			return w.ap.requestErr(err)
		}
		t, err := w.ap.wait(ctx, info.TaskUID)
		if err != nil {
			return err
		}
		if t.Status != meilisearch.TaskStatusSucceeded {
			return fmt.Errorf("delete all documents task %d %s: %s", t.UID, t.Status, t.Error.Message)
		}
		return nil
	})
	if err != nil {
		return err
	}
	w.counted.at = time.Time{}
	if w.hooks.OnAck != nil {
		w.hooks.OnAck(Ack{Position: c.Position, Time: c.Time, Truncate: true, Duration: time.Since(start)})
	}
	return nil
}

// drain waits until every enqueued batch is confirmed.
func (w *Writer) drain(ctx context.Context) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	for len(w.queue) > 0 && w.fatal == nil && ctx.Err() == nil {
		w.cond.Wait()
	}
	if w.fatal != nil {
		return w.fatal
	}
	if ctx.Err() != nil {
		return cause(ctx)
	}
	return nil
}

// track confirms flights in order.
func (w *Writer) track(ctx context.Context) error {
	// Wake waiters when the context ends.
	stop := context.AfterFunc(ctx, func() {
		w.mu.Lock()
		w.cond.Broadcast()
		w.mu.Unlock()
	})
	defer stop()

	for {
		w.mu.Lock()
		for len(w.queue) == 0 && !w.closed && ctx.Err() == nil {
			w.cond.Wait()
		}
		if len(w.queue) == 0 || ctx.Err() != nil {
			w.mu.Unlock()
			return nil
		}
		f := w.queue[0]
		w.mu.Unlock()

		select {
		case <-f.ready:
		case <-ctx.Done():
			return nil
		}
		dlq, err := w.confirm(ctx, f)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			w.mu.Lock()
			w.fatal = err
			w.cond.Broadcast()
			w.mu.Unlock()
			return err
		}
		if len(dlq) > 0 && w.hooks.OnDLQ != nil {
			w.hooks.OnDLQ(dlq)
		}

		w.mu.Lock()
		w.queue = w.queue[1:]
		n := len(w.queue)
		w.cond.Broadcast()
		w.mu.Unlock()

		f.ack.DLQ = len(dlq)
		f.ack.Duration = time.Since(f.started)
		if w.hooks.OnInflight != nil {
			w.hooks.OnInflight(n)
		}
		if w.hooks.OnAck != nil {
			w.hooks.OnAck(f.ack)
		}
	}
}

// confirm waits for a flight's tasks. When a task failed, the flight's
// documents are re-applied synchronously, skipping ids that later flights
// already carry (they hold newer data), and isolating rejected documents.
func (w *Writer) confirm(ctx context.Context, f *flight) ([]model.DLQEntry, error) {
	failed := f.enqErr != nil
	lastErr := f.enqErr
	for _, uid := range f.tasks {
		t, err := w.ap.wait(ctx, uid)
		if err != nil {
			return nil, err
		}
		if t.Status != meilisearch.TaskStatusSucceeded {
			failed = true
			lastErr = fmt.Errorf("task %d %s: %s (%s)", t.UID, t.Status, t.Error.Message, t.Error.Code)
		}
	}
	if !failed {
		return nil, nil
	}
	if w.hooks.OnRetry != nil {
		w.hooks.OnRetry(1, lastErr)
	}

	newer := map[string]struct{}{}
	w.mu.Lock()
	for _, later := range w.queue[1:] {
		for id := range later.docIDs(w.cfg.PrimaryKey) {
			newer[id] = struct{}{}
		}
	}
	w.mu.Unlock()

	keepDocs := func(docs []transform.Doc) []transform.Doc {
		out := docs[:0:0]
		for _, d := range docs {
			if _, ok := newer[docID(d, w.cfg.PrimaryKey)]; !ok {
				out = append(out, d)
			}
		}
		return out
	}
	var dels []string
	for _, id := range f.deletes {
		if _, ok := newer[id]; !ok {
			dels = append(dels, id)
		}
	}
	return w.ap.applySync(ctx, keepDocs(f.upserts), keepDocs(f.replace), keepDocs(f.merge), dels)
}

// Inflight returns the number of unconfirmed batches.
func (w *Writer) Inflight() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.queue)
}
