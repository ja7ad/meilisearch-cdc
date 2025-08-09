// Package cockroachdb captures changes from a CockroachDB table with a core
// (sinkless) changefeed, which needs no enterprise license and no sink.
//
// Positions are CockroachDB HLC timestamps in their decimal form, for example
// "1700000000123456789.0000000001" (wall time nanoseconds, then the logical
// counter). Changefeed rows are only ordered per key, so a row's own
// "updated" timestamp is not safe to resume from. Every stream event
// therefore carries the latest resolved timestamp seen so far (or the
// starting cursor): every change at or before it has already been
// delivered, so restarting from it replays at most a few changes, which is
// harmless. The engine keeps the position monotonic.
//
// Rows are read as JSON (row_to_json for the snapshot, the changefeed's
// JSON envelope for the stream) so both carry identical values. Numbers
// become int64 or float64; TIMESTAMP and TIMESTAMPTZ columns become
// time.Time; other values stay as CockroachDB renders them in JSON.
package cockroachdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const snapshotChunk = 5000

func init() {
	source.Register(model.SourceCockroachDB, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

type column struct {
	name     string
	typ      string // crdb_sql_type, used for casts
	nullable bool
	pk       bool
}

// Source streams one table. It implements source.Source.
type Source struct {
	cfg    model.Source
	schema string
	table  string

	mu      sync.Mutex // guards conn
	conn    *pgx.Conn
	columns []column
	pk      []string
	types   map[string]string

	// snapshotAt is the last position returned by CurrentPosition; the
	// snapshot reads AS OF SYSTEM TIME it for a consistent copy.
	snapshotAt string
}

// New returns an unopened source.
func New(cfg model.Source) *Source {
	s := &Source{cfg: cfg, schema: cfg.Schema, table: cfg.Table}
	if s.schema == "" {
		s.schema = "public"
	}
	return s
}

func (s *Source) ident() string { return pgx.Identifier{s.schema, s.table}.Sanitize() }

func (s *Source) connect(ctx context.Context) (*pgx.Conn, error) {
	cc, err := pgx.ParseConfig(s.cfg.DSN)
	if err != nil {
		return nil, source.Permanent(fmt.Errorf("cockroachdb: invalid dsn: %w", err))
	}
	if s.cfg.Database != "" {
		cc.Database = s.cfg.Database
	}
	conn, err := pgx.ConnectConfig(ctx, cc)
	if err != nil {
		return nil, classify(fmt.Errorf("cockroachdb: connect: %w", err))
	}
	return conn, nil
}

// Open connects, makes sure rangefeeds are enabled and loads the table.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("cockroachdb: source.dsn is required (postgresql://root@host:26257/db?sslmode=disable)"))
	}
	if s.table == "" {
		return source.Permanent(errors.New("cockroachdb: source.table is required"))
	}
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	var enabled bool
	err = conn.QueryRow(ctx, "SHOW CLUSTER SETTING kv.rangefeed.enabled").Scan(&enabled)
	if err != nil {
		return classify(fmt.Errorf("cockroachdb: read kv.rangefeed.enabled: %w", err))
	}
	if !enabled {
		if _, err := conn.Exec(ctx, "SET CLUSTER SETTING kv.rangefeed.enabled = true"); err != nil {
			return source.Permanent(fmt.Errorf("cockroachdb: changefeeds need rangefeeds, but kv.rangefeed.enabled is false "+
				"and this user cannot change it (%v); run SET CLUSTER SETTING kv.rangefeed.enabled = true as an admin", err))
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadColumns(ctx)
}

func (s *Source) loadColumns(ctx context.Context) error {
	rows, err := s.conn.Query(ctx, `
		SELECT column_name, crdb_sql_type, is_nullable = 'YES', is_hidden = 'YES'
		FROM information_schema.columns
		WHERE table_catalog = current_database() AND table_schema = $1 AND table_name = $2
		ORDER BY ordinal_position`, s.schema, s.table)
	if err != nil {
		return classify(fmt.Errorf("cockroachdb: read columns: %w", err))
	}
	type col struct {
		column
		hidden bool
	}
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (col, error) {
		var c col
		err := r.Scan(&c.name, &c.typ, &c.nullable, &c.hidden)
		return c, err
	})
	if err != nil {
		return classify(fmt.Errorf("cockroachdb: read columns: %w", err))
	}
	if len(cols) == 0 {
		return source.Permanent(fmt.Errorf("cockroachdb: table %s.%s does not exist in the current database (or is not visible to this user)", s.schema, s.table))
	}
	rows, err = s.conn.Query(ctx, `
		SELECT k.column_name
		FROM information_schema.table_constraints c
		JOIN information_schema.key_column_usage k
		  ON k.constraint_catalog = c.constraint_catalog AND k.constraint_schema = c.constraint_schema
		 AND k.constraint_name = c.constraint_name AND k.table_name = c.table_name
		WHERE c.table_catalog = current_database() AND c.table_schema = $1 AND c.table_name = $2
		  AND c.constraint_type = 'PRIMARY KEY'
		ORDER BY k.ordinal_position`, s.schema, s.table)
	if err != nil {
		return classify(fmt.Errorf("cockroachdb: read primary key: %w", err))
	}
	pk, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return classify(fmt.Errorf("cockroachdb: read primary key: %w", err))
	}
	isPK := map[string]bool{}
	for _, p := range pk {
		isPK[p] = true
	}
	s.columns, s.types = nil, map[string]string{}
	for _, c := range cols {
		if c.hidden {
			if isPK[c.name] {
				return source.Permanent(fmt.Errorf("cockroachdb: table %s.%s has no primary key (it uses the hidden %s column); "+
					"add one with ALTER TABLE %s ALTER PRIMARY KEY USING COLUMNS (...)", s.schema, s.table, c.name, s.ident()))
			}
			continue
		}
		c.pk = isPK[c.name]
		s.columns = append(s.columns, c.column)
		s.types[c.name] = c.typ
	}
	if len(pk) == 0 {
		return source.Permanent(fmt.Errorf("cockroachdb: table %s.%s has no primary key", s.schema, s.table))
	}
	s.pk = pk
	return nil
}

// Schema describes the table from information_schema.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, errors.New("cockroachdb: source is not open")
	}
	if err := s.loadColumns(ctx); err != nil {
		return nil, err
	}
	out := &model.Schema{PrimaryKey: append([]string(nil), s.pk...), RowCount: -1}
	for _, c := range s.columns {
		out.Columns = append(out.Columns, model.Column{Name: c.name, Type: c.typ, Nullable: c.nullable, PrimaryKey: c.pk})
	}
	var n *int64
	err := s.conn.QueryRow(ctx, `
		SELECT s.estimated_row_count FROM crdb_internal.table_row_statistics s
		JOIN crdb_internal.tables t ON t.table_id = s.table_id
		WHERE t.database_name = current_database() AND t.schema_name = $1 AND t.name = $2`, s.schema, s.table).Scan(&n)
	if err == nil && n != nil {
		out.RowCount = *n
	}
	return out, nil
}

// CurrentPosition returns the cluster's current HLC timestamp.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return "", errors.New("cockroachdb: source is not open")
	}
	var ts string
	if err := s.conn.QueryRow(ctx, "SELECT cluster_logical_timestamp()::STRING").Scan(&ts); err != nil {
		return "", classify(fmt.Errorf("cockroachdb: read cluster timestamp: %w", err))
	}
	s.snapshotAt = ts
	return model.Position(ts), nil
}

// Snapshot copies the table with keyset pagination over the primary key,
// reading AS OF SYSTEM TIME the position returned by CurrentPosition (when
// it was called) so the copy is consistent and does not block writers.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return errors.New("cockroachdb: source is not open")
	}
	first, next := buildSnapshotQueries(s.ident(), s.pk, s.types, s.snapshotAt, snapshotChunk)
	s.mu.Unlock()
	var last []any
	for {
		query, args := first, []any(nil)
		if last != nil {
			query, args = next, last
		}
		events, lastKey, err := s.readPage(ctx, query, args)
		if err != nil {
			return err
		}
		if len(events) > 0 {
			if err := emit(ctx, events); err != nil {
				return err
			}
		}
		if len(events) < snapshotChunk {
			return nil
		}
		last = lastKey
	}
}

// readPage runs one snapshot page query and returns its events and the
// text of the last row's key columns.
func (s *Source) readPage(ctx context.Context, query string, args []any) ([]model.Event, []any, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil, nil, errors.New("cockroachdb: source is closed")
	}
	rows, err := s.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, nil, classify(fmt.Errorf("cockroachdb: snapshot query: %w", err))
	}
	defer rows.Close()
	events := make([]model.Event, 0, snapshotChunk)
	keyText := make([]string, len(s.pk))
	dest := make([]any, len(s.pk)+1)
	for i := range keyText {
		dest[i] = &keyText[i]
	}
	var doc string
	dest[len(s.pk)] = &doc
	for rows.Next() {
		if err := rows.Scan(dest...); err != nil {
			return nil, nil, fmt.Errorf("cockroachdb: snapshot row: %w", err)
		}
		row, err := decodeRow([]byte(doc), s.types)
		if err != nil {
			return nil, nil, err
		}
		events = append(events, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, classify(fmt.Errorf("cockroachdb: snapshot query: %w", err))
	}
	lastKey := make([]any, len(keyText))
	for i, k := range keyText {
		lastKey[i] = k
	}
	return events, lastKey, nil
}

// buildSnapshotQueries returns the first page query and the query for the
// following pages, which takes the previous last key as text arguments.
// Every row is selected as its key columns (as text) and row_to_json.
func buildSnapshotQueries(ident string, pk []string, types map[string]string, asOf string, limit int) (first, next string) {
	keySel := make([]string, len(pk))
	keyCols := make([]string, len(pk))
	keyArgs := make([]string, len(pk))
	for i, k := range pk {
		q := pgx.Identifier{k}.Sanitize()
		keyCols[i] = "t." + q
		keySel[i] = "t." + q + "::STRING"
		keyArgs[i] = fmt.Sprintf("$%d::STRING::%s", i+1, types[k])
	}
	from := ident + " AS t"
	if asOf != "" {
		if _, _, err := parseHLC(asOf); err == nil {
			from += " AS OF SYSTEM TIME '" + asOf + "'"
		}
	}
	base := fmt.Sprintf("SELECT %s, row_to_json(t)::STRING FROM %s", strings.Join(keySel, ", "), from)
	order := fmt.Sprintf(" ORDER BY %s LIMIT %d", strings.Join(keyCols, ", "), limit)
	first = base + order
	next = fmt.Sprintf("%s WHERE (%s) > (%s)%s", base, strings.Join(keyCols, ", "), strings.Join(keyArgs, ", "), order)
	return first, next
}

// changefeedStatements returns the sinkless changefeed statements to try,
// newest syntax first.
func changefeedStatements(ident, cursor string) []string {
	opts := fmt.Sprintf("updated, resolved = '1s', format = json, cursor = '%s'", cursor)
	return []string{
		"CREATE CHANGEFEED FOR TABLE " + ident + " WITH " + opts + ", min_checkpoint_frequency = '1s'",
		"CREATE CHANGEFEED FOR TABLE " + ident + " WITH " + opts,
		"EXPERIMENTAL CHANGEFEED FOR " + ident + " WITH " + opts,
	}
}

// message is the JSON value of a changefeed row.
type message struct {
	After    json.RawMessage `json:"after"`
	Updated  string          `json:"updated"`
	Resolved string          `json:"resolved"`
}

// Stream runs a sinkless changefeed starting after from (now when empty).
// The changefeed holds a dedicated connection for its lifetime.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	s.mu.Lock()
	if s.conn == nil {
		s.mu.Unlock()
		return errors.New("cockroachdb: source is not open")
	}
	pk := append([]string(nil), s.pk...)
	types := s.types
	s.mu.Unlock()

	if from == "" {
		p, err := s.CurrentPosition(ctx)
		if err != nil {
			return err
		}
		from = p
	}
	if _, _, err := parseHLC(string(from)); err != nil {
		return fmt.Errorf("cockroachdb: invalid position %q: %w", from, source.ErrPositionLost)
	}

	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()

	var rows pgx.Rows
	for _, stmt := range changefeedStatements(s.ident(), string(from)) {
		rows, err = conn.Query(ctx, stmt, pgx.QueryExecModeSimpleProtocol)
		if err == nil {
			// Errors in the statement surface on the first Next.
			if rows.Next() {
				break
			}
			rows.Close()
			err = rows.Err()
			if err == nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return errors.New("cockroachdb: changefeed ended before sending anything")
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !isSyntaxOrOption(err) {
			return classify(fmt.Errorf("cockroachdb: start changefeed: %w", err))
		}
		rows = nil
	}
	if rows == nil {
		return classify(fmt.Errorf("cockroachdb: start changefeed: %w", err))
	}
	defer rows.Close()

	safe := string(from)
	for {
		var table *string
		var key, value []byte
		if err := rows.Scan(&table, &key, &value); err != nil {
			return fmt.Errorf("cockroachdb: read changefeed row: %w", err)
		}
		ev, resolved, err := decodeMessage(table, key, value, pk, types)
		if err != nil {
			return err
		}
		if resolved != "" {
			if hlcLess(safe, resolved) {
				safe = resolved
				// Everything up to the resolved timestamp is delivered:
				// checkpoint it so an idle table neither replays old
				// changes on restart nor reports a growing lag.
				if err := emit(ctx, []model.Event{{Op: model.OpProgress, Position: model.Position(safe), Time: hlcTime(safe)}}); err != nil {
					return err
				}
			}
		} else if ev != nil {
			ev.Position = model.Position(safe)
			if err := emit(ctx, []model.Event{*ev}); err != nil {
				return err
			}
		}
		if !rows.Next() {
			break
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := rows.Err(); err != nil {
		return classify(fmt.Errorf("cockroachdb: changefeed: %w", err))
	}
	return errors.New("cockroachdb: changefeed ended unexpectedly")
}

// decodeMessage converts one changefeed row. Resolved timestamp rows return
// only resolved.
func decodeMessage(table *string, key, value []byte, pk []string, types map[string]string) (*model.Event, string, error) {
	var m message
	if err := json.Unmarshal(value, &m); err != nil {
		return nil, "", fmt.Errorf("cockroachdb: decode changefeed value %q: %w", value, err)
	}
	if m.Resolved != "" && (table == nil || *table == "") {
		return nil, m.Resolved, nil
	}
	ev := &model.Event{Time: hlcTime(m.Updated)}
	after := bytes.TrimSpace(m.After)
	if len(after) == 0 || bytes.Equal(after, []byte("null")) {
		row, err := decodeKey(key, pk, types)
		if err != nil {
			return nil, "", err
		}
		ev.Op, ev.Row = model.OpDelete, row
		return ev, "", nil
	}
	row, err := decodeRow(after, types)
	if err != nil {
		return nil, "", err
	}
	ev.Op, ev.Row = model.OpUpsert, row
	return ev, "", nil
}

// decodeKey maps a changefeed key (a JSON array in primary key order) to
// the key columns.
func decodeKey(key []byte, pk []string, types map[string]string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(key))
	dec.UseNumber()
	var vals []any
	if err := dec.Decode(&vals); err != nil {
		return nil, fmt.Errorf("cockroachdb: decode changefeed key %q: %w", key, err)
	}
	if len(vals) != len(pk) {
		return nil, fmt.Errorf("cockroachdb: changefeed key %s has %d values but the primary key has %d columns", key, len(vals), len(pk))
	}
	row := make(map[string]any, len(pk))
	for i, k := range pk {
		row[k] = convertValue(types[k], vals[i])
	}
	return row, nil
}

// decodeRow decodes a JSON object row and converts typed columns.
func decodeRow(doc []byte, types map[string]string) (map[string]any, error) {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.UseNumber()
	var raw map[string]any
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("cockroachdb: decode row %q: %w", doc, err)
	}
	row := make(map[string]any, len(raw))
	for k, v := range raw {
		row[k] = convertValue(types[k], v)
	}
	return row, nil
}

var timeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05.999999999",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999-07",
	"2006-01-02 15:04:05.999999999",
}

// convertValue normalises a JSON value of a column with CockroachDB type typ.
func convertValue(typ string, v any) any {
	if s, ok := v.(string); ok {
		base := strings.ToUpper(typ)
		if i := strings.IndexByte(base, '('); i >= 0 {
			base = base[:i]
		}
		if base == "TIMESTAMP" || base == "TIMESTAMPTZ" {
			for _, l := range timeLayouts {
				if t, err := time.Parse(l, s); err == nil {
					return t.UTC()
				}
			}
		}
		return s
	}
	return normalizeJSON(v)
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return s
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeJSON(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeJSON(e)
		}
		return out
	}
	return source.Normalize(v)
}

// parseHLC parses "wall.logical" (logical optional).
func parseHLC(s string) (wall int64, logical int64, err error) {
	w, l, hasL := strings.Cut(s, ".")
	wall, err = strconv.ParseInt(w, 10, 64)
	if err != nil || wall < 0 {
		return 0, 0, fmt.Errorf("invalid HLC timestamp %q", s)
	}
	if hasL {
		if l == "" {
			return 0, 0, fmt.Errorf("invalid HLC timestamp %q", s)
		}
		logical, err = strconv.ParseInt(l, 10, 64)
		if err != nil || logical < 0 {
			return 0, 0, fmt.Errorf("invalid HLC timestamp %q", s)
		}
	}
	return wall, logical, nil
}

// hlcLess compares two HLC timestamps; unparsable values sort first.
func hlcLess(a, b string) bool {
	aw, al, aerr := parseHLC(a)
	bw, bl, berr := parseHLC(b)
	switch {
	case berr != nil:
		return false
	case aerr != nil:
		return true
	}
	return aw < bw || (aw == bw && al < bl)
}

func hlcTime(s string) time.Time {
	w, _, err := parseHLC(s)
	if err != nil || w == 0 {
		return time.Time{}
	}
	return time.Unix(0, w).UTC()
}

// Ack is a no-op: the cursor alone describes progress.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close releases the connection.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.conn.Close(ctx)
	s.conn = nil
	return err
}

func isSyntaxOrOption(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "42601" || pgErr.Code == "0A000") {
		return true
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unknown option")
}

// classify maps errors to permanent errors and ErrPositionLost.
func classify(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "gc threshold") || strings.Contains(msg, "gc ttl") || strings.Contains(msg, "must be after replica gc") {
		return fmt.Errorf("%w (the position is older than the table's gc.ttlseconds): %w", err, source.ErrPositionLost)
	}
	if strings.Contains(msg, "do not cover requested time") || strings.Contains(msg, "failed to resolve targets") {
		// The table did not exist (or its descriptor history is gone) at
		// the cursor; Open already checked it exists now.
		return fmt.Errorf("%w (the position predates the table or its retained history): %w", err, source.ErrPositionLost)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000":
			return source.Permanent(fmt.Errorf("%w (check the user and password or certificates in the dsn)", err))
		case "3D000":
			return source.Permanent(fmt.Errorf("%w (check the database name)", err))
		case "42501":
			return source.Permanent(fmt.Errorf("%w (the user needs SELECT and CHANGEFEED privileges on the table)", err))
		case "42P01":
			return source.Permanent(err)
		}
	}
	return err
}
