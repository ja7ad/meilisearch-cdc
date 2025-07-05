package mysql

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func quoteIdent(s string) string { return "`" + strings.ReplaceAll(s, "`", "``") + "`" }

// Snapshot copies the table with keyset pagination over the primary key,
// one short query per chunk. Changes made during the copy are replayed by
// the stream from the position captured before. Tables without a primary
// key are read with a single query.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	if s.db == nil {
		return fmt.Errorf("%s: source is not open", s.flavor)
	}
	cols, pk := s.snapshotColumns()
	names := make([]string, len(cols))
	for i, c := range cols {
		names[i] = quoteIdent(c.name)
	}
	base := fmt.Sprintf("SELECT %s FROM %s.%s", strings.Join(names, ", "), quoteIdent(s.database), quoteIdent(s.table))
	if len(pk) == 0 {
		return s.snapshotScan(ctx, base, cols, emit)
	}

	pkCols := make([]string, len(pk))
	pkIndex := make([]int, len(pk))
	for i, name := range pk {
		pkCols[i] = quoteIdent(name)
		for j, c := range cols {
			if c.name == name {
				pkIndex[i] = j
			}
		}
	}
	marks := strings.TrimSuffix(strings.Repeat("?, ", len(pk)), ", ")
	order := fmt.Sprintf(" ORDER BY %s LIMIT %d", strings.Join(pkCols, ", "), snapshotChunk)
	first := base + order
	next := fmt.Sprintf("%s WHERE (%s) > (%s)%s", base, strings.Join(pkCols, ", "), marks, order)

	var last []any // raw driver values of the last key
	for {
		query := first
		if last != nil {
			query = next
		}
		rows, err := s.db.QueryContext(ctx, query, last...)
		if err != nil {
			return s.classify(fmt.Errorf("%s: snapshot query: %w", s.flavor, err))
		}
		var events []model.Event
		var key []any
		for rows.Next() {
			raw := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range raw {
				ptrs[i] = &raw[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return fmt.Errorf("%s: snapshot scan: %w", s.flavor, err)
			}
			row, err := rowFromValues(cols, raw, false)
			if err != nil {
				rows.Close()
				return err
			}
			events = append(events, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
			key = key[:0]
			for _, idx := range pkIndex {
				v := raw[idx]
				if b, ok := v.([]byte); ok {
					v = bytes.Clone(b)
				}
				key = append(key, v)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return s.classify(fmt.Errorf("%s: snapshot query: %w", s.flavor, err))
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
		last = key
	}
}

func (s *Source) snapshotScan(ctx context.Context, query string, cols []column, emit source.EmitFunc) error {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return s.classify(fmt.Errorf("%s: snapshot query: %w", s.flavor, err))
	}
	defer rows.Close()
	batch := make([]model.Event, 0, snapshotChunk)
	for rows.Next() {
		raw := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range raw {
			ptrs[i] = &raw[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("%s: snapshot scan: %w", s.flavor, err)
		}
		row, err := rowFromValues(cols, raw, false)
		if err != nil {
			return err
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
		return s.classify(fmt.Errorf("%s: snapshot query: %w", s.flavor, err))
	}
	if len(batch) > 0 {
		return emit(ctx, batch)
	}
	return nil
}

// rowFromValues converts values ordered like cols into a row.
func rowFromValues(cols []column, vals []any, fromBinlog bool) (map[string]any, error) {
	row := make(map[string]any, len(cols))
	for i, c := range cols {
		if i >= len(vals) {
			break
		}
		v, err := convert(c, vals[i], fromBinlog)
		if err != nil {
			return nil, fmt.Errorf("column %s: %w", c.name, err)
		}
		row[c.name] = v
	}
	return row, nil
}
