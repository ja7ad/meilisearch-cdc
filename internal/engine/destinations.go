package engine

import (
	"context"
	"errors"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// DestTask is a task writing to a destination, with its sync state.
type DestTask struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	State       model.State `json:"state"`
	IndexUID    string      `json:"indexUid"`
	LagMs       int64       `json:"lagMs"`
	Rate        float64     `json:"rate"`
	LastEventAt time.Time   `json:"lastEventAt,omitzero"`
	LastSyncAt  time.Time   `json:"lastSyncAt,omitzero"`
	Error       string      `json:"error,omitempty"`
}

// SyncSummary aggregates the tasks of a destination.
type SyncSummary struct {
	Tasks      int       `json:"tasks"`
	Running    int       `json:"running"`
	Failing    int       `json:"failing"`
	LastSyncAt time.Time `json:"lastSyncAt,omitzero"`
	MaxLagMs   int64     `json:"maxLagMs"`
	Rate       float64   `json:"rate"`
}

// DestinationView is a redacted destination with health and tasks.
type DestinationView struct {
	model.Destination
	UsedBy []DestTask  `json:"usedBy"`
	Sync   SyncSummary `json:"sync"`
	Health dest.Health `json:"health"`
}

func (e *Engine) destTasks(id string) []DestTask {
	tasks, _ := e.tasks.List("", 0)
	out := []DestTask{}
	for _, t := range tasks {
		if t.Index.DestinationID != id {
			continue
		}
		st := e.Status(t.ID)
		out = append(out, DestTask{ID: t.ID, Name: t.Name, State: st.State, IndexUID: t.Index.UID, LagMs: st.LagMs,
			Rate: st.Rate, LastEventAt: st.LastEventAt, LastSyncAt: st.LastSyncAt, Error: st.Error})
	}
	return out
}

func (e *Engine) destinationView(ctx context.Context, d model.Destination) DestinationView {
	v := DestinationView{Destination: d.Redacted(), UsedBy: e.destTasks(d.ID), Health: e.dests.Health(ctx, d.ID)}
	for _, t := range v.UsedBy {
		v.Sync.Tasks++
		if t.State.Active() {
			v.Sync.Running++
		}
		if t.State == model.StateFailed || t.State == model.StateHeld || t.State == model.StateRetrying {
			v.Sync.Failing++
		}
		if t.LastSyncAt.After(v.Sync.LastSyncAt) {
			v.Sync.LastSyncAt = t.LastSyncAt
		}
		v.Sync.MaxLagMs = max(v.Sync.MaxLagMs, t.LagMs)
		v.Sync.Rate += t.Rate
	}
	return v
}

// DestinationViews lists destinations with their health and tasks.
func (e *Engine) DestinationViews(ctx context.Context) ([]DestinationView, error) {
	all, err := e.dests.List()
	if err != nil {
		return nil, err
	}
	out := make([]DestinationView, len(all))
	done := make(chan struct{}, len(all))
	for i, d := range all {
		go func() { out[i] = e.destinationView(ctx, d); done <- struct{}{} }()
	}
	for range all {
		<-done
	}
	return out, nil
}

// DestinationView returns one destination.
func (e *Engine) DestinationView(ctx context.Context, id string) (DestinationView, error) {
	d, err := e.dests.Get(id)
	if err != nil {
		return DestinationView{}, wrapDest(err)
	}
	return e.destinationView(ctx, d), nil
}

func wrapDest(err error) error {
	switch {
	case errors.Is(err, dest.ErrNotFound):
		return errors.Join(ErrNotFound, err)
	case errors.Is(err, dest.ErrConflict), errors.Is(err, dest.ErrSingleInstance):
		return errors.Join(ErrConflict, err)
	}
	return err
}

// CreateDestination stores a destination.
func (e *Engine) CreateDestination(ctx context.Context, d model.Destination) (DestinationView, error) {
	d, err := e.dests.Create(d)
	if err != nil {
		return DestinationView{}, wrapDest(err)
	}
	e.TaskBus.Publish(TaskEvent{ID: d.ID, Action: "destination.created"})
	return e.DestinationView(ctx, d.ID)
}

// UpdateDestination replaces a destination; running tasks using it restart
// when its connection changed.
func (e *Engine) UpdateDestination(ctx context.Context, id string, d model.Destination) (DestinationView, error) {
	d, changed, err := e.dests.Update(id, d)
	if err != nil {
		return DestinationView{}, wrapDest(err)
	}
	if changed {
		for _, t := range e.destTasks(id) {
			if e.running(t.ID) {
				e.logf(t.ID, "info", "destination %q changed: restarting", d.Name)
				if _, err := e.Action(ctx, t.ID, "restart"); err != nil {
					e.logf(t.ID, "error", "restart after destination change: %v", err)
				}
			}
		}
	}
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "destination.updated"})
	return e.DestinationView(ctx, id)
}

// DeleteDestination removes an unused destination.
func (e *Engine) DeleteDestination(id string) error {
	if used := e.destTasks(id); len(used) > 0 {
		refs := make([]TaskRef, len(used))
		for i, t := range used {
			refs[i] = TaskRef{ID: t.ID, Name: t.Name, State: t.State}
		}
		return &ErrInUse{UsedBy: refs}
	}
	if err := e.dests.Delete(id); err != nil {
		return wrapDest(err)
	}
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "destination.deleted"})
	return nil
}

// DestinationHealthEvent is a completed health check of a destination.
type DestinationHealthEvent struct {
	ID     string      `json:"id"`
	Health dest.Health `json:"health"`
}

// CheckDestination checks a destination now.
func (e *Engine) CheckDestination(ctx context.Context, id string) (dest.Health, error) {
	if _, err := e.dests.Get(id); err != nil {
		return dest.Health{}, wrapDest(err)
	}
	return e.dests.Check(ctx, id), nil
}

// MonitorDestinations checks every destination at its health check
// interval until ctx is cancelled.
func (e *Engine) MonitorDestinations(ctx context.Context) { e.dests.Monitor(ctx) }

// SetDefaultDestination changes the default destination for new tasks.
func (e *Engine) SetDefaultDestination(id string) error {
	return wrapDest(e.dests.SetDefault(id))
}
