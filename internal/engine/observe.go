package engine

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/events"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

const (
	sampleEvery  = 2 * time.Second
	seriesPoints = 300 // 10 minutes
)

func (e *Engine) logf(taskID, level, format string, args ...any) {
	entry := model.LogEntry{Time: time.Now().UTC(), TaskID: taskID, Level: level, Message: fmt.Sprintf(format, args...)}
	e.mu.Lock()
	ring, ok := e.logs[taskID]
	if !ok {
		ring = events.NewRing[model.LogEntry](1000)
		e.logs[taskID] = ring
	}
	e.mu.Unlock()
	ring.Push(entry)
	e.allLogs.Push(entry)
	e.LogBus.Publish(entry)
	attrs := []any{"task", taskID}
	switch level {
	case "error":
		e.log.Error(entry.Message, attrs...)
	case "warn":
		e.log.Warn(entry.Message, attrs...)
	default:
		e.log.Info(entry.Message, attrs...)
	}
}

// Logs returns the most recent log entries of a task ("" for all tasks).
func (e *Engine) Logs(taskID string, limit int) []model.LogEntry {
	if taskID == "" {
		return e.allLogs.Last(limit)
	}
	e.mu.Lock()
	ring := e.logs[taskID]
	e.mu.Unlock()
	if ring == nil {
		return []model.LogEntry{}
	}
	return ring.Last(limit)
}

// Series returns the throughput samples of a task.
func (e *Engine) Series(taskID string) []Point {
	e.mu.Lock()
	ring := e.series[taskID]
	e.mu.Unlock()
	if ring == nil {
		return []Point{}
	}
	return ring.Values()
}

// mutate changes a status under the lock and publishes it.
func (e *Engine) mutate(id string, fn func(st *model.Status)) {
	e.mu.Lock()
	st, ok := e.status[id]
	if !ok {
		st = &model.Status{TaskID: id}
		e.status[id] = st
	}
	fn(st)
	st.UpdatedAt = time.Now().UTC()
	cp := *st
	e.mu.Unlock()
	e.StatusBus.Publish(cp)
}

func (e *Engine) setState(id string, s model.State, msg string, err error) {
	e.mutate(id, func(st *model.Status) {
		st.State = s
		st.Message = msg
		if err != nil {
			st.Error = err.Error()
		} else if s != model.StateRetrying {
			st.Error = ""
		}
		if !s.Active() {
			st.Inflight, st.Rate = 0, 0
		}
	})
	if !s.Active() || s == model.StateStreaming {
		e.persistStatus(id)
	}
}

func (e *Engine) persistStatus(id string) {
	st := e.Status(id)
	if _, ok, _ := e.tasks.Get(id); ok {
		_ = e.statuses.Put(id, st)
	}
}

func (e *Engine) persistStatuses() {
	e.mu.Lock()
	all := make(map[string]model.Status, len(e.status))
	for id, st := range e.status {
		all[id] = *st
	}
	e.mu.Unlock()
	for id, st := range all {
		if _, ok, _ := e.tasks.Get(id); ok {
			_ = e.statuses.Put(id, st)
		}
	}
}

// sampler computes rates, records series and persists statuses.
func (e *Engine) sampler() {
	defer e.wg.Done()
	t := time.NewTicker(sampleEvery)
	defer t.Stop()
	last := map[string]int64{}
	persistTick := 0
	for {
		select {
		case <-e.ctx.Done():
			return
		case now := <-t.C:
			e.mu.Lock()
			for id, st := range e.status {
				total := st.Counters.Upserted + st.Counters.Deleted
				prev, seen := last[id]
				last[id] = total
				if !seen {
					prev = total
				}
				rate := float64(total-prev) / sampleEvery.Seconds()
				// Exponential smoothing keeps the dashboard steady.
				st.Rate = 0.5*rate + 0.5*st.Rate
				if st.Rate < 0.01 {
					st.Rate = 0
				}
				if _, ok := e.runners[id]; !ok {
					st.Rate = 0
				}
				// Lag is measured when a change is confirmed. A streaming task
				// with nothing received, buffered or in flight for a while is
				// caught up, so its last measurement no longer applies (a
				// stuck source is what the "stalled" alert rule is for).
				// Changes waiting longer than any confirmed lag: Meilisearch
				// is slow or down, and the lag must show it.
				if !st.PendingSince.IsZero() && isLive(st.State) {
					if waiting := now.Sub(st.PendingSince).Milliseconds(); waiting > pendingLagFloor.Milliseconds() && waiting > st.LagMs {
						st.LagMs = waiting
						e.cfg.Metrics.Lag.WithLabelValues(id).Set(float64(waiting) / 1000)
					}
				}
				if st.State == model.StateStreaming && st.LagMs > 0 && st.Inflight == 0 && st.PendingSince.IsZero() &&
					now.Sub(st.LastEventAt) > idleLagReset && now.Sub(st.LastSyncAt) > idleLagReset {
					st.LagMs = 0
					e.cfg.Metrics.Lag.WithLabelValues(id).Set(0)
				}
				ring, ok := e.series[id]
				if !ok {
					ring = events.NewRing[Point](seriesPoints)
					e.series[id] = ring
				}
				ring.Push(Point{T: now.UnixMilli(), Rate: st.Rate, LagMs: st.LagMs, Upserted: st.Counters.Upserted, Deleted: st.Counters.Deleted})
			}
			snap := make([]model.Status, 0, len(e.status))
			for _, st := range e.status {
				snap = append(snap, *st)
			}
			e.mu.Unlock()
			for _, st := range snap {
				if st.State.Active() {
					e.StatusBus.Publish(st)
				}
			}
			persistTick++
			if persistTick%5 == 0 {
				e.persistStatuses()
			}
		}
	}
}

// Overview aggregates the dashboard summary.
type Overview struct {
	Tasks struct {
		Total   int                 `json:"total"`
		ByState map[model.State]int `json:"byState"`
	} `json:"tasks"`
	Totals       model.Counters       `json:"totals"`
	Rate         float64              `json:"rate"`
	DLQ          int                  `json:"dlq"`
	Meilisearch  MeiliSummary         `json:"meilisearch"`
	Destinations []DestinationSummary `json:"destinations"`
	Sources      []model.SourceType   `json:"sources"`
}

// Overview builds the summary; Meilisearch calls are bounded by ctx.
func (e *Engine) Overview(ctx context.Context, sources []model.SourceType) Overview {
	var o Overview
	o.Tasks.ByState = map[model.State]int{}
	o.Sources = sources
	ids, _ := e.tasks.List("", 0)
	for _, t := range ids {
		st := e.Status(t.ID)
		o.Tasks.Total++
		o.Tasks.ByState[st.State]++
		c := st.Counters
		o.Totals.Upserted += c.Upserted
		o.Totals.Deleted += c.Deleted
		o.Totals.Events += c.Events
		o.Totals.Batches += c.Batches
		o.Totals.Errors += c.Errors
		o.Totals.Retries += c.Retries
		o.Totals.DLQ += c.DLQ
		o.Totals.Coalesced += c.Coalesced
		o.Rate += st.Rate
	}
	o.DLQ, _ = e.dlq.Count("")
	o.Destinations = e.DestinationHealth(ctx)
	for _, d := range o.Destinations {
		if d.IsDefault {
			o.Meilisearch = MeiliSummary{Healthy: d.Healthy, Version: d.Version, Indexes: d.Indexes, Documents: d.Documents,
				DatabaseSize: d.DatabaseSize, Error: d.Error}
		}
	}
	if len(o.Destinations) == 0 {
		o.Meilisearch.Error = dest.ErrNoDefault.Error()
	}
	return o
}

// MeiliSummary describes the Meilisearch instance.
type MeiliSummary struct {
	Healthy      bool   `json:"healthy"`
	Version      string `json:"version"`
	Indexes      int    `json:"indexes"`
	Documents    int64  `json:"documents"`
	DatabaseSize any    `json:"databaseSize"`
	Error        string `json:"error,omitempty"`
}

// DestinationSummary is the health of one destination.
type DestinationSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	dest.Health
}

// DestinationHealth returns the (briefly cached) health of every destination.
// idleLagReset is how long a streaming task must be quiet before its lag
// reads as caught up.
const idleLagReset = 10 * time.Second

// pendingLagFloor is how long changes must wait for confirmation before the
// wait counts as lag, so normal batching does not make the value jitter.
const pendingLagFloor = 5 * time.Second

// isLive reports states in which a task is expected to deliver changes.
func isLive(s model.State) bool {
	return s == model.StateStreaming || s == model.StateRetrying
}

func (e *Engine) DestinationHealth(ctx context.Context) []DestinationSummary {
	all, _ := e.dests.List()
	out := make([]DestinationSummary, len(all))
	var wg sync.WaitGroup
	for i, d := range all {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out[i] = DestinationSummary{ID: d.ID, Name: d.Name, IsDefault: d.IsDefault, Health: e.dests.Health(ctx, d.ID)}
		}()
	}
	wg.Wait()
	return out
}
