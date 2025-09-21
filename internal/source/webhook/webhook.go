// Package webhook is a push source: applications send their changes to the
// service over HTTP instead of the service reading a database.
//
// The HTTP layer only needs two functions: ParsePayload turns a request body
// into events and Push hands them to the running source of a task. Push
// blocks until the engine has acknowledged the pushed events, so a
// successful response means the documents are durably in Meilisearch.
//
// Positions are a per-task sequence number in decimal ("0", "1", "2"...),
// one per pushed event. They do not survive restarts: queued events that
// were not acknowledged are lost when the process stops, and their pushers
// get an error (ErrNotRunning or a context error) and must retry. After a
// restart the sequence continues from the position the engine resumes from,
// so positions stay monotonic for a task.
package webhook

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// ErrNotRunning is returned by Push when no source of the task is
// streaming (the task is stopped, starting, restarting or in snapshot only
// mode). Pushers should retry later.
var ErrNotRunning = errors.New("webhook: the task is not running")

const (
	// queueSize bounds the number of pushes waiting for the stream; further
	// pushers block (back pressure).
	queueSize = 64
	// maxBatch bounds the events emitted at once.
	maxBatch = 1000
)

func init() {
	source.Register(model.SourceWebhook, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(taskID)
	})
}

// registry maps task ids to their open sources.
var (
	regMu    sync.Mutex
	registry = map[string]*Source{}
)

func lookup(taskID string) *Source {
	regMu.Lock()
	defer regMu.Unlock()
	return registry[taskID]
}

// Push enqueues events for the running source of taskID and blocks until the
// engine has acknowledged a position at or after the last of them, or ctx
// ends. It returns the position of the last event.
//
// It returns ErrNotRunning when no source of the task is streaming, and
// also (wrapped) when the source stops before acknowledging. Events are
// assigned their positions by Push; Position fields set by the caller are
// overwritten. Pushes of one task are applied in the order Push is called.
func Push(ctx context.Context, taskID string, events []model.Event) (model.Position, error) {
	s := lookup(taskID)
	if s == nil {
		return "", ErrNotRunning
	}
	return s.push(ctx, events)
}

type item struct {
	events []model.Event
}

// Source receives pushed events for one task. It implements source.Source.
type Source struct {
	taskID string
	queue  chan item

	// pushMu serialises enqueueing so sequence numbers follow queue order.
	pushMu sync.Mutex

	mu        sync.Mutex
	seq       uint64        // last assigned sequence number
	acked     uint64        // last acknowledged sequence number
	ackCh     chan struct{} // closed and replaced on every ack
	streaming int
	closed    chan struct{}
	isClosed  bool
	// pending holds a batch whose emit failed; the next Stream sends it first.
	pending []model.Event
}

// New returns an unopened source for taskID.
func New(taskID string) (*Source, error) {
	if taskID == "" {
		return nil, source.Permanent(errors.New("webhook: a task id is required"))
	}
	return &Source{
		taskID: taskID,
		queue:  make(chan item, queueSize),
		ackCh:  make(chan struct{}),
		closed: make(chan struct{}),
	}, nil
}

// Open registers the source so Push can find it. A source opened later for
// the same task replaces this one.
func (s *Source) Open(context.Context) error {
	regMu.Lock()
	registry[s.taskID] = s
	regMu.Unlock()
	return nil
}

// Schema returns the conventional key "id". The pushed rows decide the
// actual fields, so the schema is marked inferred.
func (s *Source) Schema(context.Context) (*model.Schema, error) {
	return &model.Schema{
		Columns:    []model.Column{{Name: "id", Type: "any", PrimaryKey: true}},
		PrimaryKey: []string{"id"},
		Inferred:   true,
		RowCount:   -1,
	}, nil
}

// CurrentPosition returns the last assigned sequence number.
func (s *Source) CurrentPosition(context.Context) (model.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return formatPosition(s.seq), nil
}

// Snapshot is a no-op: there is no table to copy.
func (s *Source) Snapshot(context.Context, source.EmitFunc) error { return nil }

// Stream emits pushed events until ctx ends. While it runs Push accepts
// events for the task.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if from != "" {
		n, err := parsePosition(from)
		if err != nil {
			return err
		}
		// Continue the sequence after a restart so positions stay monotonic.
		s.pushMu.Lock()
		s.mu.Lock()
		if n > s.seq {
			s.seq = n
		}
		if n > s.acked {
			s.acked = n
		}
		s.mu.Unlock()
		s.pushMu.Unlock()
	}

	s.mu.Lock()
	if s.isClosed {
		s.mu.Unlock()
		return errors.New("webhook: source is closed")
	}
	s.streaming++
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.streaming--
		s.mu.Unlock()
	}()

	send := func(batch []model.Event) error {
		if err := emit(ctx, batch); err != nil {
			s.mu.Lock()
			s.pending = batch
			s.mu.Unlock()
			return err
		}
		return nil
	}
	if len(pending) > 0 {
		if err := send(pending); err != nil {
			return err
		}
	}
	for {
		var batch []model.Event
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.closed:
			return errors.New("webhook: source is closed")
		case it := <-s.queue:
			batch = append(batch, it.events...)
		}
	drain:
		for len(batch) < maxBatch {
			select {
			case it := <-s.queue:
				batch = append(batch, it.events...)
			default:
				break drain
			}
		}
		if err := send(batch); err != nil {
			return err
		}
	}
}

// Ack records the acknowledged sequence number and wakes waiting pushers.
func (s *Source) Ack(_ context.Context, pos model.Position) error {
	if pos == "" {
		return nil
	}
	n, err := parsePosition(pos)
	if err != nil {
		return err
	}
	s.mu.Lock()
	if n > s.acked {
		s.acked = n
		close(s.ackCh)
		s.ackCh = make(chan struct{})
	}
	s.mu.Unlock()
	return nil
}

// Close unregisters the source and fails pushers still waiting.
func (s *Source) Close() error {
	regMu.Lock()
	if registry[s.taskID] == s {
		delete(registry, s.taskID)
	}
	regMu.Unlock()
	s.mu.Lock()
	if !s.isClosed {
		s.isClosed = true
		close(s.closed)
	}
	s.mu.Unlock()
	return nil
}

func (s *Source) push(ctx context.Context, events []model.Event) (model.Position, error) {
	for i, e := range events {
		switch e.Op {
		case model.OpUpsert, model.OpDelete:
			if len(e.Row) == 0 {
				return "", fmt.Errorf("webhook: event %d (%s) has no row", i, e.Op)
			}
		case model.OpTruncate:
		default:
			return "", fmt.Errorf("webhook: event %d has an invalid operation %d", i, e.Op)
		}
	}

	s.pushMu.Lock()
	s.mu.Lock()
	if s.streaming == 0 || s.isClosed {
		s.mu.Unlock()
		s.pushMu.Unlock()
		return "", ErrNotRunning
	}
	last := s.seq
	s.mu.Unlock()
	if len(events) == 0 {
		s.pushMu.Unlock()
		return formatPosition(last), nil
	}

	now := time.Now().UTC()
	evs := make([]model.Event, len(events))
	for i, e := range events {
		last++
		e.Position = formatPosition(last)
		e.Snapshot = false
		if e.Time.IsZero() {
			e.Time = now
		}
		evs[i] = e
	}
	select {
	case s.queue <- item{events: evs}:
		s.mu.Lock()
		s.seq = last
		s.mu.Unlock()
		s.pushMu.Unlock()
	case <-ctx.Done():
		s.pushMu.Unlock()
		return "", ctx.Err()
	case <-s.closed:
		s.pushMu.Unlock()
		return "", ErrNotRunning
	}

	pos := formatPosition(last)
	for {
		s.mu.Lock()
		if s.acked >= last {
			s.mu.Unlock()
			return pos, nil
		}
		ch := s.ackCh
		s.mu.Unlock()
		select {
		case <-ch:
		case <-ctx.Done():
			return pos, ctx.Err()
		case <-s.closed:
			return pos, fmt.Errorf("%w: the source stopped before the events were acknowledged", ErrNotRunning)
		}
	}
}

func formatPosition(n uint64) model.Position { return model.Position(strconv.FormatUint(n, 10)) }

func parsePosition(pos model.Position) (uint64, error) {
	n, err := strconv.ParseUint(string(pos), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("webhook: invalid position %q (want a sequence number): %w", pos, source.ErrPositionLost)
	}
	return n, nil
}
