// Package memory is an in-process source backed by a Go map. It powers
// engine tests and demos; it behaves like a log based connector (ordered
// change log, positions, resume).
package memory

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// Type is the source type of memory tables.
const Type model.SourceType = "memory"

func init() {
	model.RegisterSourceType(Type)
	source.Register(Type, func(cfg model.Source, _ string) (source.Source, error) {
		return &Source{table: Table(cfg.Table)}, nil
	})
}

type change struct {
	ev  model.Event
	seq int64
}

// Tab is a named in-memory table with a change log.
type Tab struct {
	mu      sync.Mutex
	rows    map[string]map[string]any
	log     []change
	seq     int64
	notify  chan struct{}
	columns []model.Column
	// FailOpen makes Open fail with this error (for tests).
	FailOpen error
}

var (
	tablesMu sync.Mutex
	tables   = map[string]*Tab{}
)

// Table returns (creating) the named table.
func Table(name string) *Tab {
	tablesMu.Lock()
	defer tablesMu.Unlock()
	t, ok := tables[name]
	if !ok {
		t = &Tab{rows: map[string]map[string]any{}, notify: make(chan struct{})}
		tables[name] = t
	}
	return t
}

// Reset drops every table.
func Reset() {
	tablesMu.Lock()
	tables = map[string]*Tab{}
	tablesMu.Unlock()
}

func (t *Tab) append(op model.Op, row map[string]any) {
	t.seq++
	t.log = append(t.log, change{seq: t.seq, ev: model.Event{Op: op, Row: row, Time: time.Now(), Position: model.Position(strconv.FormatInt(t.seq, 10))}})
	close(t.notify)
	t.notify = make(chan struct{})
}

// Upsert inserts or updates a row keyed by row["id"].
func (t *Tab) Upsert(row map[string]any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	id := fmt.Sprint(row["id"])
	cp := make(map[string]any, len(row))
	for k, v := range row {
		cp[k] = v
	}
	t.rows[id] = cp
	t.append(model.OpUpsert, cp)
}

// Delete removes the row with the given id.
func (t *Tab) Delete(id any) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.rows, fmt.Sprint(id))
	t.append(model.OpDelete, map[string]any{"id": id})
}

// Truncate removes every row.
func (t *Tab) Truncate() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.rows = map[string]map[string]any{}
	t.append(model.OpTruncate, nil)
}

// SetColumns overrides the reported schema columns.
func (t *Tab) SetColumns(cols []model.Column) {
	t.mu.Lock()
	t.columns = cols
	t.mu.Unlock()
}

// Len returns the row count.
func (t *Tab) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.rows)
}

// Source reads a Tab.
type Source struct {
	table *Tab
	mu    sync.Mutex
	acked model.Position
}

func (s *Source) Open(context.Context) error { return s.table.FailOpen }

func (s *Source) Schema(context.Context) (*model.Schema, error) {
	t := s.table
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.columns != nil {
		return &model.Schema{Columns: t.columns, PrimaryKey: []string{"id"}, RowCount: int64(len(t.rows))}, nil
	}
	seen := map[string]bool{"id": true}
	cols := []model.Column{{Name: "id", Type: "int", PrimaryKey: true}}
	for _, r := range t.rows {
		for k, v := range r {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, model.Column{Name: k, Type: fmt.Sprintf("%T", v), Nullable: true})
			}
		}
	}
	sort.Slice(cols[1:], func(i, j int) bool { return cols[i+1].Name < cols[j+1].Name })
	return &model.Schema{Columns: cols, PrimaryKey: []string{"id"}, Inferred: true, RowCount: int64(len(t.rows))}, nil
}

func (s *Source) CurrentPosition(context.Context) (model.Position, error) {
	s.table.mu.Lock()
	defer s.table.mu.Unlock()
	return model.Position(strconv.FormatInt(s.table.seq, 10)), nil
}

func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	t := s.table
	t.mu.Lock()
	ids := make([]string, 0, len(t.rows))
	for id := range t.rows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	rows := make([]model.Event, 0, len(ids))
	for _, id := range ids {
		rows = append(rows, model.Event{Op: model.OpUpsert, Row: t.rows[id], Snapshot: true})
	}
	t.mu.Unlock()
	for i := 0; i < len(rows); i += 500 {
		if err := emit(ctx, rows[i:min(i+500, len(rows))]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	var next int64
	if from != "" {
		n, err := strconv.ParseInt(string(from), 10, 64)
		if err != nil {
			return source.Permanent(err)
		}
		next = n
	}
	for {
		t := s.table
		t.mu.Lock()
		var batch []model.Event
		for _, c := range t.log {
			if c.seq > next {
				batch = append(batch, c.ev)
			}
		}
		wait := t.notify
		t.mu.Unlock()
		if len(batch) > 0 {
			if err := emit(ctx, batch); err != nil {
				return err
			}
			n, _ := strconv.ParseInt(string(batch[len(batch)-1].Position), 10, 64)
			next = n
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wait:
		}
	}
}

func (s *Source) Ack(_ context.Context, pos model.Position) error {
	s.mu.Lock()
	s.acked = pos
	s.mu.Unlock()
	return nil
}

func (s *Source) Close() error { return nil }
