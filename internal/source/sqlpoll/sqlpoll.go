// Package sqlpoll captures changes from any database/sql driver by polling a
// monotonic cursor column (an updated_at timestamp or a sequence bumped on
// every write). It is the fallback when log based capture is not available.
//
// Limitations:
//   - Hard deletes are invisible: a deleted row simply stops being returned.
//     Use soft deletes (a deleted flag or deleted_at column) together with
//     mapping.softDeleteColumn to propagate them.
//   - The cursor must grow with every insert and update. Rows whose cursor is
//     NULL are never streamed (they are still copied by the snapshot).
//   - Writes that commit with a cursor value lower than one already polled
//     (long transactions stamping updated_at at their start, sequences
//     allocated out of order) are missed. Prefer a cursor assigned at commit
//     time, or keep transactions short relative to PollInterval.
//   - On SQLite prefer an integer cursor: time values are bound as text and
//     compare lexically against however the column was written.
package sqlpoll

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver
	_ "modernc.org/sqlite"             // registers the "sqlite" driver

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	batchSize           = 1000
	defaultPollInterval = 2 * time.Second
)

func init() {
	source.Register(model.SourceSQLPoll, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

// Source polls one table.
type Source struct {
	cfg     model.Source
	d       dialect
	db      *sql.DB
	table   string // quoted, qualified
	cols    map[string]columnInfo
	order   []string // column names in table order
	pk      []string
	timeKey []bool // per (cursor, pk...) column: holds times
}

type columnInfo struct {
	dbType   string
	nullable bool
}

// New returns an unopened source.
func New(cfg model.Source) *Source { return &Source{cfg: cfg} }

// Open connects and validates the table, cursor column and primary key.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("sqlpoll: source.dsn is required"))
	}
	if s.cfg.Table == "" {
		return source.Permanent(errors.New("sqlpoll: source.table is required"))
	}
	// The cursor is checked where it is used, so the schema and a snapshot
	// sample can be read to help choose it.
	d, err := dialectFor(s.cfg.Driver)
	if err != nil {
		return source.Permanent(err)
	}
	s.d = d
	dsn := s.cfg.DSN
	if d.name == "mysql" {
		mc, err := mysql.ParseDSN(dsn)
		if err != nil {
			return source.Permanent(fmt.Errorf("sqlpoll: invalid mysql dsn: %w", err))
		}
		mc.ParseTime = true // scan DATETIME/TIMESTAMP as time.Time
		dsn = mc.FormatDSN()
	}
	if d.name == "sqlite" {
		dsn = sqliteDSN(dsn)
	}
	db, err := sql.Open(d.driverName, dsn)
	if err != nil {
		return source.Permanent(fmt.Errorf("sqlpoll: open %s: %w", d.driverName, err))
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return fmt.Errorf("sqlpoll: connect: %w", err)
	}
	s.db = db
	s.table = d.table(s.cfg.Schema, s.cfg.Table)

	if err := s.loadColumns(ctx); err != nil {
		return err
	}
	if _, ok := s.cols[s.cfg.CursorColumn]; s.cfg.CursorColumn != "" && !ok {
		return source.Permanent(fmt.Errorf("sqlpoll: cursor column %q does not exist in %s", s.cfg.CursorColumn, s.table))
	}
	if err := s.loadPrimaryKey(ctx); err != nil {
		return err
	}
	s.timeKey = make([]bool, 0, len(s.pk)+1)
	for _, c := range append([]string{s.cfg.CursorColumn}, s.pk...) {
		s.timeKey = append(s.timeKey, isTimeType(s.cols[c].dbType))
	}
	return nil
}

func (s *Source) loadColumns(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "SELECT * FROM "+s.table+" WHERE 1 = 0") // #nosec G202 -- table name is the configured, validated identifier
	if err != nil {
		return source.Permanent(fmt.Errorf("sqlpoll: cannot read table %s: %w", s.table, err))
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return fmt.Errorf("sqlpoll: column types: %w", err)
	}
	s.cols = make(map[string]columnInfo, len(types))
	s.order = s.order[:0]
	for _, ct := range types {
		nullable, ok := ct.Nullable()
		if !ok {
			nullable = true
		}
		s.cols[ct.Name()] = columnInfo{dbType: strings.ToUpper(ct.DatabaseTypeName()), nullable: nullable}
		s.order = append(s.order, ct.Name())
	}
	return rows.Err()
}

func (s *Source) loadPrimaryKey(ctx context.Context) error {
	q, args := s.d.primaryKeyQuery(s.cfg.Schema, s.cfg.Table)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return fmt.Errorf("sqlpoll: read primary key: %w", err)
	}
	defer rows.Close()
	s.pk = s.pk[:0]
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		s.pk = append(s.pk, name)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if len(s.pk) == 0 {
		return source.Permanent(fmt.Errorf("sqlpoll: table %s has no primary key; sqlpoll needs one to page through rows", s.table))
	}
	return nil
}

// Schema describes the table.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	isPK := map[string]bool{}
	for _, c := range s.pk {
		isPK[c] = true
	}
	out := &model.Schema{PrimaryKey: append([]string(nil), s.pk...), RowCount: -1}
	for _, name := range s.order {
		c := s.cols[name]
		out.Columns = append(out.Columns, model.Column{
			Name:       name,
			Type:       strings.ToLower(c.dbType),
			Nullable:   c.nullable && !isPK[name],
			PrimaryKey: isPK[name],
		})
	}
	q, args := s.d.rowCountQuery(s.cfg.Schema, s.cfg.Table, s.table)
	var n sql.NullInt64
	if err := s.db.QueryRowContext(ctx, q, args...).Scan(&n); err == nil && n.Valid && n.Int64 >= 0 {
		out.RowCount = n.Int64
	}
	return out, nil
}

// CurrentPosition returns the cursor and primary key of the latest row.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	p, err := s.latest(ctx)
	if err != nil {
		return "", err
	}
	return encodePosition(p), nil
}

var errNoCursor = source.Permanent(errors.New("sqlpoll: source.cursorColumn is required (a column bumped on every insert and update, e.g. updated_at or a version sequence)"))

func (s *Source) latest(ctx context.Context) (position, error) {
	if s.cfg.CursorColumn == "" {
		return position{}, errNoCursor
	}
	vals := make([]any, len(s.pk)+1)
	ptrs := make([]any, len(vals))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	err := s.db.QueryRowContext(ctx, s.d.latestQuery(s.table, s.cfg.CursorColumn, s.pk)).Scan(ptrs...)
	if errors.Is(err, sql.ErrNoRows) {
		return position{PK: []any{}}, nil
	}
	if err != nil {
		return position{}, fmt.Errorf("sqlpoll: read latest cursor: %w", err)
	}
	names := append([]string{s.cfg.CursorColumn}, s.pk...)
	for i := range vals {
		vals[i] = s.convert(names[i], vals[i])
	}
	return position{Cursor: vals[0], PK: vals[1:]}, nil
}

// Snapshot pages through the table by primary key.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	var after []any
	for {
		q, args := s.d.snapshotQuery(s.table, s.pk, after, batchSize)
		rows, err := s.query(ctx, q, args)
		if err != nil {
			return fmt.Errorf("sqlpoll: snapshot: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		evs := make([]model.Event, len(rows))
		for i, r := range rows {
			evs[i] = model.Event{Op: model.OpUpsert, Row: r, Snapshot: true}
		}
		if err := emit(ctx, evs); err != nil {
			return err
		}
		if len(rows) < batchSize {
			return nil
		}
		last := rows[len(rows)-1]
		after = make([]any, len(s.pk))
		for i, c := range s.pk {
			after[i] = last[c]
		}
	}
}

// Stream polls for rows changed after from every PollInterval.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if s.cfg.CursorColumn == "" {
		return errNoCursor
	}
	var pos position
	if from == "" {
		p, err := s.latest(ctx)
		if err != nil {
			return err
		}
		pos = p
	} else {
		p, err := decodePosition(from, func(i int) bool { return i < len(s.timeKey) && s.timeKey[i] })
		if err != nil {
			return fmt.Errorf("%w: %v", source.ErrPositionLost, err)
		}
		pos = p
	}
	ticker := time.NewTicker(s.cfg.PollInterval.Or(defaultPollInterval))
	defer ticker.Stop()
	for {
		for {
			next, n, err := s.poll(ctx, pos, emit)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			pos = next
			if n < batchSize {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// poll emits one batch after pos and returns the new position and the
// number of rows read.
func (s *Source) poll(ctx context.Context, pos position, emit source.EmitFunc) (position, int, error) {
	q, args := s.d.pollQuery(s.table, s.cfg.CursorColumn, s.pk, pos, batchSize)
	rows, err := s.query(ctx, q, args)
	if err != nil {
		return pos, 0, fmt.Errorf("sqlpoll: poll: %w", err)
	}
	if len(rows) == 0 {
		return pos, 0, nil
	}
	evs := make([]model.Event, len(rows))
	for i, r := range rows {
		p := position{Cursor: r[s.cfg.CursorColumn], PK: make([]any, len(s.pk))}
		for j, c := range s.pk {
			p.PK[j] = r[c]
		}
		ev := model.Event{Op: model.OpUpsert, Row: r, Position: encodePosition(p)}
		if t, ok := p.Cursor.(time.Time); ok {
			ev.Time = t
		}
		evs[i] = ev
		pos = p
	}
	if err := emit(ctx, evs); err != nil {
		return pos, 0, err
	}
	return pos, len(rows), nil
}

func (s *Source) query(ctx context.Context, q string, args []any) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	vals := make([]any, len(names))
	ptrs := make([]any, len(names))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make(map[string]any, len(names))
		for i, n := range names {
			row[n] = s.convert(n, vals[i])
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// convert normalises a scanned value using the column's database type.
func (s *Source) convert(col string, v any) any {
	typ := s.cols[col].dbType
	if b, ok := v.([]byte); ok {
		switch {
		case isBinaryType(typ):
			return b
		case typ == "JSON" || typ == "JSONB":
			return decodeJSON(b)
		default:
			return string(b)
		}
	}
	if str, ok := v.(string); ok && (typ == "JSON" || typ == "JSONB") {
		return decodeJSON([]byte(str))
	}
	return source.Normalize(v)
}

func decodeJSON(b []byte) any {
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return string(b)
	}
	return source.Normalize(v)
}

func isBinaryType(t string) bool {
	return t == "BYTEA" || strings.Contains(t, "BLOB") || strings.Contains(t, "BINARY")
}

func isTimeType(t string) bool {
	return strings.Contains(t, "TIMESTAMP") || strings.Contains(t, "DATE")
}

// Ack is a no-op: the position alone describes progress.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close closes the connection pool.
func (s *Source) Close() error {
	if s.db == nil {
		return nil
	}
	return s.db.Close()
}

// sqliteDSN waits for locks instead of failing with SQLITE_BUSY while the
// application writes, and keeps the polling reads from blocking writers.
func sqliteDSN(dsn string) string {
	if strings.Contains(dsn, "busy_timeout") {
		return dsn
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	if !strings.HasPrefix(dsn, "file:") {
		dsn = "file:" + dsn
	}
	return dsn + sep + "_pragma=busy_timeout(5000)"
}
