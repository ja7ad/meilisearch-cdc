package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// Snapshot copies the table with keyset pagination over the primary key.
// Every chunk is its own short query, so no long transaction holds back
// vacuum; rows changed during the copy are replayed by the stream from the
// position captured before the snapshot.
//
// Tables without a primary key (allowed with REPLICA IDENTITY FULL) are read
// with a single query instead.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	if s.conn == nil {
		return errors.New("postgres: source is not open")
	}
	if len(s.pk) == 0 {
		return s.snapshotScan(ctx, emit)
	}

	quoted := make([]string, len(s.columns))
	for i, c := range s.columns {
		quoted[i] = pgx.Identifier{c.name}.Sanitize()
	}
	pkCols := make([]string, len(s.pk))
	pkCasts := make([]string, len(s.pk))
	pkIndex := make([]int, len(s.pk))
	for i, name := range s.pk {
		pkCols[i] = pgx.Identifier{name}.Sanitize()
		for j, c := range s.columns {
			if c.name == name {
				pkIndex[i] = j
				pkCasts[i] = fmt.Sprintf("$%d::text::%s", i+1, c.typ)
			}
		}
	}
	base := fmt.Sprintf("SELECT %s FROM %s", strings.Join(quoted, ", "), s.ident())
	order := fmt.Sprintf(" ORDER BY %s LIMIT %d", strings.Join(pkCols, ", "), snapshotChunk)
	first := base + order
	next := fmt.Sprintf("%s WHERE (%s) > (%s)%s", base, strings.Join(pkCols, ", "), strings.Join(pkCasts, ", "), order)

	var last []any // text values of the last key, nil before the first page
	for {
		query, args := first, []any{pgx.QueryResultFormats{pgx.TextFormatCode}}
		if last != nil {
			query = next
			args = append(args, last...)
		}
		events, raw, err := s.readChunk(ctx, query, args, pkIndex)
		if err != nil {
			return err
		}
		if len(events) == 0 {
			return nil
		}
		if err := emit(ctx, events); err != nil {
			return err
		}
		if len(events) < snapshotChunk {
			return nil
		}
		last = raw
	}
}

// readChunk runs one page query and returns its events plus the text values
// of the key columns of the last row.
func (s *Source) readChunk(ctx context.Context, query string, args []any, pkIndex []int) ([]model.Event, []any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, classify(fmt.Errorf("postgres: snapshot query: %w", err))
	}
	defer rows.Close()
	m := s.conn.TypeMap()
	fields := rows.FieldDescriptions()
	var events []model.Event
	var lastKey []any
	for rows.Next() {
		raw := rows.RawValues()
		row := make(map[string]any, len(fields))
		for i, f := range fields {
			v, err := decodeText(m, f.DataTypeOID, raw[i])
			if err != nil {
				return nil, nil, fmt.Errorf("postgres: column %s: %w", f.Name, err)
			}
			row[f.Name] = v
		}
		events = append(events, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
		lastKey = lastKey[:0]
		for _, idx := range pkIndex {
			lastKey = append(lastKey, string(raw[idx]))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, nil, classify(fmt.Errorf("postgres: snapshot query: %w", err))
	}
	return events, lastKey, nil
}

// snapshotScan reads a table without a primary key in one pass.
func (s *Source) snapshotScan(ctx context.Context, emit source.EmitFunc) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows, err := s.conn.Query(ctx, "SELECT * FROM "+s.ident(), pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return classify(fmt.Errorf("postgres: snapshot query: %w", err))
	}
	defer rows.Close()
	m := s.conn.TypeMap()
	fields := rows.FieldDescriptions()
	batch := make([]model.Event, 0, snapshotChunk)
	for rows.Next() {
		raw := rows.RawValues()
		row := make(map[string]any, len(fields))
		for i, f := range fields {
			v, err := decodeText(m, f.DataTypeOID, raw[i])
			if err != nil {
				return fmt.Errorf("postgres: column %s: %w", f.Name, err)
			}
			row[f.Name] = v
		}
		batch = append(batch, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
		if len(batch) == snapshotChunk {
			if err := emit(ctx, batch); err != nil {
				return err
			}
			batch = make([]model.Event, 0, snapshotChunk)
		}
	}
	if err := rows.Err(); err != nil {
		return classify(fmt.Errorf("postgres: snapshot query: %w", err))
	}
	if len(batch) > 0 {
		return emit(ctx, batch)
	}
	return nil
}
