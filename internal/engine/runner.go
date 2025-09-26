package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/sink"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// runner supervises one task: it runs the pipeline and restarts it with
// backoff after transient failures.
type runner struct {
	e    *Engine
	task model.Task

	ctx     context.Context // cancelled to request a graceful stop
	cancel  context.CancelFunc
	kill    context.CancelFunc // aborts in-flight writes when draining takes too long
	killCtx context.Context
	done    chan struct{}
}

func newRunner(e *Engine, t model.Task, base context.Context) *runner {
	killCtx, kill := context.WithCancel(base)
	ctx, cancel := context.WithCancel(killCtx)
	return &runner{e: e, task: t, ctx: ctx, cancel: cancel, kill: kill, killCtx: killCtx, done: make(chan struct{})}
}

// stop asks the runner to stop and waits. Pending batches are drained
// for up to StopTimeout, then aborted (they will be replayed).
func (r *runner) stop(ctx context.Context) {
	r.cancel()
	timer := time.NewTimer(r.e.cfg.StopTimeout)
	defer timer.Stop()
	select {
	case <-r.done:
		return
	case <-timer.C:
	case <-ctx.Done():
	}
	r.kill()
	<-r.done
}

func (r *runner) logf(level, format string, args ...any) { r.e.logf(r.task.ID, level, format, args...) }

func (r *runner) loop() {
	defer close(r.done)
	defer r.kill()
	id := r.task.ID
	r.e.cfg.Metrics.Running.WithLabelValues(id).Set(1)
	r.e.mutate(id, func(st *model.Status) { st.StartedAt = time.Now().UTC(); st.Attempt = 0 })
	policy := retry.Policy{Initial: time.Second, Max: time.Minute}
	attempt := 0
	for {
		started := time.Now()
		err := r.runOnce()
		if r.ctx.Err() != nil {
			// Stop requested; the caller sets the final state.
			return
		}
		switch {
		case err == nil:
			r.e.setState(id, model.StateCompleted, "snapshot completed", nil)
			r.logf("info", "snapshot completed")
			_, _ = r.e.setDesired(id, model.StateStopped)
			return
		case sink.IsHold(err):
			r.e.setState(id, model.StateHeld, "held by a safety guard; review and resume or resync", err)
			r.logf("error", "%v", err)
			return
		case errors.Is(err, source.ErrPositionLost):
			r.logf("warn", "source position lost (%v); starting a full resync", err)
			_ = r.e.checkpoints.Update(id, func(cp *Checkpoint, _ bool) error {
				*cp = Checkpoint{Resync: true, UpdatedAt: time.Now().UTC()}
				return nil
			})
			continue
		case source.IsPermanent(err), errors.Is(err, indexmgr.ErrPrimaryKeyMismatch), retry.IsPermanent(err):
			r.e.setState(id, model.StateFailed, "configuration error; fix the task and start it again", err)
			r.e.cfg.Metrics.Errors.WithLabelValues(id).Inc()
			r.e.mutate(id, func(st *model.Status) { st.Counters.Errors++ })
			r.logf("error", "%v", err)
			return
		}
		if time.Since(started) > 2*time.Minute {
			attempt = 0 // it ran healthy for a while; start backoff over
		}
		attempt++
		wait := policy.Backoff(attempt)
		r.e.cfg.Metrics.Errors.WithLabelValues(id).Inc()
		r.e.mutate(id, func(st *model.Status) { st.Counters.Errors++; st.Attempt = attempt })
		r.e.setState(id, model.StateRetrying, fmt.Sprintf("retrying in %s (attempt %d)", wait.Round(100*time.Millisecond), attempt), err)
		r.logf("error", "pipeline failed: %v; retrying in %s", err, wait.Round(100*time.Millisecond))
		if retry.Sleep(r.ctx, wait) != nil {
			return
		}
	}
}

// pipeline holds the per-run objects.
type pipeline struct {
	r      *runner
	src    source.Source
	tr     *transform.Transformer
	schema *model.Schema
	tg     *dest.Target

	mu        sync.Mutex
	cp        Checkpoint
	lastSave  time.Time
	streaming bool
	snapDone  atomic.Int64
}

func (r *runner) runOnce() error {
	t := r.task
	id := t.ID
	r.e.setState(id, model.StateStarting, "connecting to the source", nil)
	r.e.mutate(id, func(st *model.Status) { st.PendingSince = time.Time{} }) // unconfirmed changes are read again

	plain, err := r.e.Reveal(t)
	if err != nil {
		return source.Permanent(err)
	}
	src, err := source.New(plain.Source, id)
	if err != nil {
		return source.Permanent(err)
	}
	defer src.Close()
	if err := src.Open(r.ctx); err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	schema, err := src.Schema(r.ctx)
	if err != nil {
		return fmt.Errorf("read source schema: %w", err)
	}
	tr, err := transform.New(t.Mapping, schema.PrimaryKey)
	if err != nil {
		return source.Permanent(err)
	}

	tg, err := r.e.target(t)
	if err != nil {
		return source.Permanent(err)
	}
	r.e.setState(id, model.StateStarting, "preparing the index on "+tg.Name, nil)
	if _, err := tg.Indexes.Ensure(r.ctx, t.Index.UID, tr.PrimaryKeyField()); err != nil {
		return err
	}
	if t.Index.ManageSettings && len(t.Index.Settings) > 0 {
		changes, err := tg.Indexes.Diff(r.ctx, t.Index.UID, t.Index.Settings)
		if err != nil {
			return retry.Permanent(fmt.Errorf("index settings: %w", err))
		}
		if len(changes) > 0 {
			r.logf("info", "applying %d changed index settings", len(changes))
			if _, err := tg.Indexes.ApplySettings(r.ctx, t.Index.UID, t.Index.Settings); err != nil {
				return fmt.Errorf("apply settings: %w", err)
			}
		}
	}

	p := &pipeline{r: r, src: src, tr: tr, schema: schema, tg: tg}
	p.cp, _, _ = r.e.checkpoints.Get(id)

	// Pushed sources have no table to copy: a snapshot would rebuild the
	// index from nothing.
	needSnapshot := t.Sync.Mode != model.ModeStream && t.Source.Type != model.SourceWebhook && (!p.cp.SnapshotDone || p.cp.Resync)
	if needSnapshot {
		if err := p.snapshot(); err != nil {
			return err
		}
		if t.Sync.Mode == model.ModeSnapshot {
			return nil
		}
	}
	return p.stream()
}

// saveCheckpoint persists cp; throttled unless force is set.
func (p *pipeline) saveCheckpoint(force bool) error {
	p.mu.Lock()
	if !force && time.Since(p.lastSave) < 500*time.Millisecond {
		p.mu.Unlock()
		return nil
	}
	p.cp.UpdatedAt = time.Now().UTC()
	cp := p.cp
	p.lastSave = time.Now()
	p.mu.Unlock()
	p.r.e.mutate(p.r.task.ID, func(st *model.Status) { st.Checkpoint = string(cp.Position) })
	return p.r.e.checkpoints.Put(p.r.task.ID, cp)
}

func (p *pipeline) writerConfig(index string) sink.Config {
	t := p.r.task
	return sink.Config{
		TaskID:        t.ID,
		PrimaryKey:    p.tr.PrimaryKeyField(),
		WriteMode:     t.Index.WriteMode,
		BatchSize:     t.Sync.BatchSize,
		FlushInterval: t.Sync.FlushInterval.D(),
		MaxInflight:   t.Sync.MaxInflight,
		Retry: retry.Policy{
			MaxAttempts: t.Sync.Retry.MaxAttempts,
			Initial:     t.Sync.Retry.InitialBackoff.D(),
			Max:         t.Sync.Retry.MaxBackoff.D(),
		},
		Safety: t.Sync.Safety,
	}
}

func (p *pipeline) hooks() sink.Hooks {
	e, id := p.r.e, p.r.task.ID
	m := e.cfg.Metrics
	return sink.Hooks{
		OnAck: func(a sink.Ack) {
			m.Documents.WithLabelValues(id, "upserted").Add(float64(a.Upserted))
			m.Documents.WithLabelValues(id, "deleted").Add(float64(a.Deleted))
			m.Batches.WithLabelValues(id).Inc()
			m.BatchDuration.WithLabelValues(id).Observe(a.Duration.Seconds())
			var lag int64
			if !a.Time.IsZero() && !a.Snapshot {
				lag = time.Since(a.Time).Milliseconds()
				if lag < 0 {
					lag = 0
				}
				m.Lag.WithLabelValues(id).Set(float64(lag) / 1000)
			}
			e.mutate(id, func(st *model.Status) {
				st.LastSyncAt = time.Now().UTC()
				// Later changes may still be buffered; the next one received
				// starts a new pending window, so a stuck write shows as lag.
				st.PendingSince = time.Time{}
				st.Counters.Upserted += int64(a.Upserted)
				st.Counters.Deleted += int64(a.Deleted)
				if a.Upserted+a.Deleted > 0 || a.Truncate {
					st.Counters.Batches++
				}
				st.Counters.Coalesced += int64(a.Coalesced)
				if !a.Snapshot {
					st.LagMs = lag
				}
				if a.Snapshot {
					st.Snapshot.Done = p.snapDone.Add(int64(a.Upserted))
				}
			})
			if a.Truncate {
				p.r.logf("warn", "source table truncated: index cleared")
			}
			p.mu.Lock()
			streaming := p.streaming
			if streaming && a.Position != "" {
				p.cp.Position = a.Position
			}
			p.mu.Unlock()
			if streaming && a.Position != "" {
				if err := p.saveCheckpoint(false); err != nil {
					p.r.logf("error", "save checkpoint: %v", err)
				}
				if err := p.src.Ack(context.WithoutCancel(p.r.ctx), a.Position); err != nil {
					p.r.logf("warn", "source ack: %v", err)
				}
			}
		},
		OnDLQ: func(entries []model.DLQEntry) {
			e.addDLQ(id, entries)
			p.r.logf("warn", "%d document(s) rejected by Meilisearch and moved to the dead letter queue", len(entries))
		},
		OnRetry: func(attempt int, err error) {
			m.Retries.WithLabelValues(id).Inc()
			e.mutate(id, func(st *model.Status) { st.Counters.Retries++ })
			p.r.logf("warn", "meilisearch write retry %d: %v", attempt, err)
		},
		OnInflight: func(n int) {
			m.Inflight.WithLabelValues(id).Set(float64(n))
			e.mutate(id, func(st *model.Status) { st.Inflight = n })
		},
	}
}

// run connects a producer (snapshot or stream) to a writer on index.
// When the stop signal arrives the producer stops first and the writer
// drains what it already has, so a graceful stop loses no work.
func (p *pipeline) run(index string, produce func(ctx context.Context, emit source.EmitFunc) error) error {
	e := p.r.e
	id := p.r.task.ID
	ch := make(chan []transform.Change, 4)
	w := sink.New(p.tg.Client.Index(index), p.tg.Client, p.writerConfig(index), p.hooks())

	// The writer outlives the stop signal (r.ctx) but not a kill.
	wctx, wcancel := context.WithCancelCause(p.r.killCtx)
	defer wcancel(nil)
	werr := make(chan error, 1)
	go func() {
		err := w.Run(wctx, ch)
		if err != nil {
			wcancel(err)
		}
		werr <- err
	}()

	// The producer stops on a stop signal or a writer failure.
	pctx, pcancel := context.WithCancel(p.r.ctx)
	defer pcancel()
	stopOnWriter := context.AfterFunc(wctx, pcancel)
	defer stopOnWriter()

	emit := func(ctx context.Context, evs []model.Event) error {
		out := make([]transform.Change, 0, len(evs))
		var bad []model.DLQEntry
		for _, ev := range evs {
			if ev.Op == model.OpProgress {
				// Not a change: only moves the checkpoint and the lag.
				if ev.Position != "" {
					out = append(out, transform.Change{Position: ev.Position, Time: ev.Time})
				}
				continue
			}
			e.cfg.Metrics.Events.WithLabelValues(id, ev.Op.String()).Inc()
			c, err := p.tr.Apply(ev)
			if err != nil {
				bad = append(bad, model.DLQEntry{TaskID: id, Op: "transform", Document: rowDoc(ev.Row), Error: err.Error(), Attempts: 1, CreatedAt: time.Now().UTC()})
				// Keep the position moving even if the row is skipped.
				if ev.Position != "" {
					out = append(out, transform.Change{Position: ev.Position, Time: ev.Time})
				}
				continue
			}
			out = append(out, c)
		}
		if len(bad) > 0 {
			e.addDLQ(id, bad)
		}
		if changes := len(evs) - countOp(evs, model.OpProgress); changes > 0 {
			e.mutate(id, func(st *model.Status) {
				st.Counters.Events += int64(changes)
				st.LastEventAt = time.Now().UTC()
				if st.PendingSince.IsZero() {
					st.PendingSince = st.LastEventAt
				}
			})
		}
		if len(out) == 0 {
			return nil
		}
		select {
		case ch <- out:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		case <-wctx.Done():
			return context.Cause(wctx)
		}
	}

	perr := produce(pctx, emit)
	close(ch)
	if perr != nil && pctx.Err() == nil {
		// The producer failed on its own: abandon unconfirmed writes, they
		// are replayed from the checkpoint.
		wcancel(perr)
		<-werr //nolint:errcheck // the writer error is superseded by perr
		return perr
	}
	err := <-werr
	if err != nil {
		if errors.Is(err, context.Canceled) && p.r.ctx.Err() != nil {
			return nil
		}
		return err
	}
	return nil
}

func rowDoc(row map[string]any) map[string]any {
	doc := make(map[string]any, len(row))
	for k, v := range row {
		switch t := v.(type) {
		case time.Time:
			doc[k] = t.UTC().Format(time.RFC3339Nano)
		case []byte:
			doc[k] = string(t)
		default:
			doc[k] = v
		}
	}
	return doc
}

// snapshot copies the table, into a shadow index when a zero-downtime
// rebuild applies, then promotes it.
func (p *pipeline) snapshot() error {
	r := p.r
	t := r.task
	id := t.ID
	ctx := r.ctx

	// Capture where streaming must resume before reading any row. A start
	// captured by an interrupted snapshot (or resync) is reused; otherwise
	// it is taken now. The last acknowledged position is not used: the
	// snapshot already holds those changes, and replaying them would bring
	// back deletes and older values.
	start := p.cp.StartPosition
	if start == "" {
		pos, err := p.src.CurrentPosition(ctx)
		if err != nil {
			return fmt.Errorf("capture source position: %w", err)
		}
		start = pos
	}
	p.mu.Lock()
	p.cp.StartPosition = start
	p.cp.SnapshotDone = false
	p.mu.Unlock()
	if err := p.saveCheckpoint(true); err != nil {
		return err
	}

	target := t.Index.UID
	shadow := ""
	count, err := p.tg.Indexes.DocumentCount(ctx, t.Index.UID)
	if err != nil {
		return err
	}
	if t.Sync.Safety.ZeroDowntimeResync && count > 0 {
		if t.Sync.Safety.DumpBeforeResync {
			if info, err := p.tg.Client.CreateDump(ctx); err == nil {
				r.logf("info", "dump requested before resync (task %d)", info.TaskUID)
			} else {
				r.logf("warn", "dump before resync failed: %v", err)
			}
		}
		shadow, err = p.tg.Indexes.CreateShadow(ctx, t.Index.UID, p.tr.PrimaryKeyField(), t.Index.Settings)
		if err != nil {
			if shadow != "" {
				_ = p.tg.Indexes.Drop(context.WithoutCancel(ctx), shadow)
			}
			return err
		}
		target = shadow
		r.logf("info", "rebuilding into shadow index %s; searches keep using %s", shadow, t.Index.UID)
	} else if p.cp.Resync && count > 0 {
		// In-place resync: documents of rows deleted meanwhile would linger,
		// so start from an empty index.
		r.logf("warn", "in-place resync: clearing %s (enable zero-downtime resync to avoid the gap)", t.Index.UID)
		info, err := p.tg.Client.Index(t.Index.UID).DeleteAllDocuments(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := p.tg.Indexes.Wait(ctx, info.TaskUID); err != nil {
			return err
		}
	}

	state := model.StateSnapshotting
	if p.cp.Resync {
		state = model.StateResyncing
	}
	total := p.schema.RowCount
	p.snapDone.Store(0)
	r.e.mutate(id, func(st *model.Status) { st.Snapshot = model.Progress{Done: 0, Total: total} })
	r.e.setState(id, state, fmt.Sprintf("copying ~%d rows into %s", max(total, 0), target), nil)
	r.logf("info", "snapshot started into %s", target)

	err = p.run(target, func(ctx context.Context, emit source.EmitFunc) error {
		return p.src.Snapshot(ctx, emit)
	})
	if err == nil && r.ctx.Err() != nil {
		err = r.ctx.Err()
	}
	if err != nil {
		if shadow != "" {
			_ = p.tg.Indexes.Drop(context.WithoutCancel(ctx), shadow)
		}
		return err
	}
	if shadow != "" {
		if err := p.tg.Indexes.WaitIdle(ctx, shadow); err != nil {
			return err
		}
		if err := p.tg.Indexes.Promote(ctx, t.Index.UID, shadow); err != nil {
			_ = p.tg.Indexes.Drop(context.WithoutCancel(ctx), shadow)
			return fmt.Errorf("promote shadow index: %w", err)
		}
		r.logf("info", "shadow index swapped in: %s is live", t.Index.UID)
	}
	done := p.snapDone.Load()
	r.e.mutate(id, func(st *model.Status) {
		st.Snapshot.Done = done
		if st.Snapshot.Total < done {
			st.Snapshot.Total = done
		}
	})
	p.mu.Lock()
	p.cp.SnapshotDone = true
	p.cp.Resync = false
	p.cp.Position = start
	p.mu.Unlock()
	r.logf("info", "snapshot finished: %d documents", done)
	return p.saveCheckpoint(true)
}

func (p *pipeline) stream() error {
	r := p.r
	from := p.cp.Position
	if from == "" {
		pos, err := p.src.CurrentPosition(r.ctx)
		if err != nil {
			return fmt.Errorf("capture source position: %w", err)
		}
		from = pos
		p.mu.Lock()
		p.cp.Position = pos
		p.cp.SnapshotDone = true
		p.mu.Unlock()
		if err := p.saveCheckpoint(true); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.streaming = true
	p.mu.Unlock()
	r.e.setState(r.task.ID, model.StateStreaming, "streaming changes", nil)
	r.logf("info", "streaming from %s", displayPos(from))
	err := p.run(r.task.Index.UID, func(ctx context.Context, emit source.EmitFunc) error {
		return p.src.Stream(ctx, from, emit)
	})
	if serr := p.saveCheckpoint(true); serr != nil && err == nil {
		err = serr
	}
	return err
}

func displayPos(p model.Position) string {
	if p == "" {
		return "now"
	}
	if len(p) > 48 {
		return string(p[:45]) + "..."
	}
	return string(p)
}

func countOp(evs []model.Event, op model.Op) int {
	n := 0
	for _, ev := range evs {
		if ev.Op == op {
			n++
		}
	}
	return n
}
