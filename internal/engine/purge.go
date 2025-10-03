package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/events"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// Purging clears stored data in bulk for the operator CLI. Every method
// leaves the engine consistent while it runs: affected tasks are stopped
// first and in-memory state is dropped along with the stored records.

// PurgeDLQ deletes every dead letter of every task.
func (e *Engine) PurgeDLQ() (int, error) { return e.dlq.Clear() }

// PurgeDiffs cancels running diffs and deletes every diff report.
func (e *Engine) PurgeDiffs() (int, error) {
	e.mu.Lock()
	for _, cancel := range e.diffRun {
		cancel()
	}
	e.mu.Unlock()
	// A cancelled diff stores its final report on the way out.
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		e.mu.Lock()
		busy := len(e.diffRun)
		e.mu.Unlock()
		if busy == 0 {
			break
		}
	}
	return e.diffs.Clear()
}

// PurgeLogs forgets the task activity log. It lives in memory only, so
// there is nothing to purge while the service is stopped.
func (e *Engine) PurgeLogs() int {
	e.mu.Lock()
	rings := e.logs
	e.logs = map[string]*events.Ring[model.LogEntry]{}
	e.mu.Unlock()
	n := e.allLogs.Clear()
	for _, r := range rings {
		r.Clear()
	}
	return n
}

// PurgeCheckpoints deletes every checkpoint, so each task snapshots again
// on its next start. Running tasks are stopped first and started again.
func (e *Engine) PurgeCheckpoints(ctx context.Context) (int, error) {
	tasks, err := e.tasks.List("", 0)
	if err != nil {
		return 0, err
	}
	var restart []string
	for _, t := range tasks {
		if e.running(t.ID) {
			e.stopRunner(ctx, t.ID)
			restart = append(restart, t.ID)
		}
	}
	n, err := e.checkpoints.Clear()
	for _, t := range tasks {
		e.logf(t.ID, "warn", "checkpoint purged: the next start runs a full snapshot")
	}
	for _, id := range restart {
		if t, gerr := e.Get(id); gerr == nil {
			e.spawn(t)
		}
	}
	return n, err
}

// PurgeTasks stops and deletes every task with its status, checkpoint,
// dead letters and diff report. With deleteIndexes each task's index is
// deleted on its destination too; a task whose index cannot be deleted is
// kept and reported. Source side resources (replication slots) stay.
func (e *Engine) PurgeTasks(ctx context.Context, deleteIndexes bool) (int, error) {
	if _, err := e.PurgeDiffs(); err != nil {
		return 0, err
	}
	tasks, err := e.tasks.List("", 0)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, t := range tasks {
		uid := ""
		if deleteIndexes {
			uid = t.Index.UID
		}
		if err := e.Delete(ctx, t.ID, false, uid); err != nil {
			errs = append(errs, fmt.Errorf("task %s (%s): %w", t.ID, t.Name, err))
			continue
		}
		n++
	}
	if len(errs) > 0 {
		return n, errors.Join(errs...)
	}
	// Records left behind by tasks deleted outside the engine.
	for _, clear := range []func() (int, error){e.statuses.Clear, e.checkpoints.Clear, e.dlq.Clear, e.diffs.Clear} {
		if _, err := clear(); err != nil {
			return n, err
		}
	}
	e.PurgeLogs()
	return n, nil
}

// TaskCount returns the number of tasks.
func (e *Engine) TaskCount() (int, error) { return e.tasks.Count("") }

// PurgeConnectors deletes every connector. It refuses while tasks exist.
func (e *Engine) PurgeConnectors() (int, error) {
	if n, err := e.TaskCount(); err != nil {
		return 0, err
	} else if n > 0 {
		return 0, fmt.Errorf("%w: %d tasks exist; purge tasks too", ErrConflict, n)
	}
	n, err := e.connectors.Clear()
	if err != nil {
		return n, err
	}
	_, err = e.connTests.Clear()
	e.TaskBus.Publish(TaskEvent{Action: "connector.deleted"})
	return n, err
}

// PurgeDestinations deletes every destination. It refuses while tasks
// exist. In single instance mode the MEILI_URL destination is recreated by
// the caller (it comes from the environment, not from stored data).
func (e *Engine) PurgeDestinations() (int, error) {
	if n, err := e.TaskCount(); err != nil {
		return 0, err
	} else if n > 0 {
		return 0, fmt.Errorf("%w: %d tasks exist; purge tasks too", ErrConflict, n)
	}
	n, err := e.dests.Purge()
	e.TaskBus.Publish(TaskEvent{Action: "destination.deleted"})
	return n, err
}
