// Package source defines the contract every change data capture connector implements.
package source

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// EmitFunc receives events in commit order. Returning an error stops the
// connector, which returns that error. Emit applies back pressure: the
// connector must not read further while Emit is blocked.
type EmitFunc func(ctx context.Context, events []model.Event) error

// Source is a change data capture connector for one table or collection.
//
// The engine drives a source like this:
//
//	Open -> Schema -> CurrentPosition -> Snapshot -> Stream(from) ... Ack(pos) ... Close
//
// CurrentPosition is captured before the snapshot, so streaming from it after
// the snapshot replays every change made during the copy (writes are
// idempotent, so replays are harmless).
type Source interface {
	// Open connects and validates the configuration (permissions, wal_level,
	// binlog format, replica set...). It must be called first.
	Open(ctx context.Context) error
	// Schema describes the table.
	Schema(ctx context.Context) (*model.Schema, error)
	// CurrentPosition returns the position from which Stream would continue
	// right now. Log based sources create their slot or cursor here.
	CurrentPosition(ctx context.Context) (model.Position, error)
	// Snapshot reads every row, emitting OpUpsert events with Snapshot=true
	// in chunks. It does not emit positions.
	Snapshot(ctx context.Context, emit EmitFunc) error
	// Stream emits changes from the given position until ctx is cancelled
	// or an error occurs. An empty position means "from now".
	Stream(ctx context.Context, from model.Position, emit EmitFunc) error
	// Ack tells the source every event up to pos is durably applied, so it
	// can release resources (advance a replication slot, commit offsets).
	Ack(ctx context.Context, pos model.Position) error
	// Close releases connections. Drop removes server side resources
	// (replication slots, publications) when the task is deleted.
	Close() error
}

// Dropper is implemented by sources that create server side resources.
type Dropper interface {
	Drop(ctx context.Context) error
}

// Factory builds a source from a task configuration.
type Factory func(cfg model.Source, taskID string) (Source, error)

var (
	mu        sync.RWMutex
	factories = map[model.SourceType]Factory{}
)

// Register makes a connector available. It is called from connector init functions.
func Register(t model.SourceType, f Factory) {
	mu.Lock()
	defer mu.Unlock()
	factories[t] = f
}

// New builds the connector for cfg.Type.
func New(cfg model.Source, taskID string) (Source, error) {
	mu.RLock()
	f, ok := factories[cfg.Type]
	mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("source type %q is not registered", cfg.Type)
	}
	return f(cfg, taskID)
}

// Registered returns the registered connector types, sorted.
func Registered() []model.SourceType {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]model.SourceType, 0, len(factories))
	for t := range factories {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// ErrPositionLost means the source can no longer resume from the stored
// position (purged binlog, expired stream shard, dropped slot). The engine
// reacts with a full resync.
var ErrPositionLost = errors.New("source position is no longer available")

// PermanentError wraps an error that retrying cannot fix (bad credentials,
// missing table, wrong server configuration).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// Permanent wraps err as a PermanentError.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Err: err}
}

// IsPermanent reports whether err is a PermanentError.
func IsPermanent(err error) bool {
	var p *PermanentError
	return errors.As(err, &p)
}
