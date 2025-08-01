// Package sqlserver captures changes from SQL Server's native change data
// capture (CDC) change tables.
//
// The database and the table must have CDC enabled (by a sysadmin / db_owner):
//
//	USE [db]; EXEC sys.sp_cdc_enable_db;
//	EXEC sys.sp_cdc_enable_table @source_schema = N'dbo', @source_name = N'items', @role_name = NULL;
//
// and SQL Server Agent must run, since its capture job fills the change
// tables (on Linux containers: MSSQL_AGENT_ENABLED=true). The source reads
// the capture instance "{schema}_{table}" when it exists, otherwise the
// newest capture instance of the table; cfg.Slot names another one.
//
// Positions are "lsnhex:seqvalhex": the __$start_lsn and __$seqval of the
// last delivered change row, 20 lowercase hex digits each, for example
// "0000002b000004880021:0000002b000004880020". Stream resumes with the rows
// strictly after it. A position older than the capture instance's minimum
// LSN (rows removed by the cleanup job after the retention period) reports
// source.ErrPositionLost. Note that a task idle for longer than the
// retention period can also see this, because its stored position stops
// moving while the low water mark advances; the engine then resyncs.
//
// Change rows are polled every 500ms, which adds to the capture job's own
// latency (a few seconds by default). Event.Time is the commit time from
// cdc.lsn_time_mapping, which SQL Server records in server local time; it is
// reported as UTC.
package sqlserver

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	snapshotChunk = 5000
	changesLimit  = 5000
	pollInterval  = 500 * time.Millisecond
)

func init() {
	source.Register(model.SourceSQLServer, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

type column struct {
	name     string
	typ      string
	nullable bool
	pk       bool
}

// Source streams one table. It implements source.Source.
type Source struct {
	cfg      model.Source
	schema   string
	table    string
	instance string

	db      *sql.DB
	columns []column // captured columns, in table order
	pk      []string
}

// New returns an unopened source.
func New(cfg model.Source) *Source {
	s := &Source{cfg: cfg, schema: cfg.Schema, table: cfg.Table}
	if s.schema == "" {
		s.schema = "dbo"
	}
	return s
}

func (s *Source) fullName() string { return quote(s.schema) + "." + quote(s.table) }

// Open connects and checks CDC is enabled for the database and the table.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("sqlserver: source.dsn is required (sqlserver://user:password@host:1433?database=db)"))
	}
	if s.table == "" {
		return source.Permanent(errors.New("sqlserver: source.table is required"))
	}
	dsn, err := withDatabase(s.cfg.DSN, s.cfg.Database)
	if err != nil {
		return source.Permanent(err)
	}
	db, err := sql.Open("sqlserver", dsn)
	if err != nil {
		return source.Permanent(fmt.Errorf("sqlserver: invalid dsn: %w", err))
	}
	s.db = db
	if err := db.PingContext(ctx); err != nil {
		return classify(fmt.Errorf("sqlserver: connect: %w", err))
	}

	var dbName string
	var cdcEnabled bool
	err = db.QueryRowContext(ctx, "SELECT name, is_cdc_enabled FROM sys.databases WHERE name = DB_NAME()").Scan(&dbName, &cdcEnabled)
	if err != nil {
		return classify(fmt.Errorf("sqlserver: read database settings: %w", err))
	}
	if dbName == "master" || dbName == "tempdb" || dbName == "model" || dbName == "msdb" {
		return source.Permanent(fmt.Errorf("sqlserver: connected to the system database %q; put the database in the dsn (?database=db) or source.database", dbName))
	}
	if !cdcEnabled {
		return source.Permanent(fmt.Errorf("sqlserver: change data capture is not enabled for database %q; as sysadmin run: USE %s; EXEC sys.sp_cdc_enable_db;", dbName, quote(dbName)))
	}

	var objectID sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT OBJECT_ID(@p1, 'U')", s.fullName()).Scan(&objectID); err != nil {
		return classify(fmt.Errorf("sqlserver: look up table: %w", err))
	}
	if !objectID.Valid {
		return source.Permanent(fmt.Errorf("sqlserver: table %s.%s does not exist in database %q (or is not visible to this user)", s.schema, s.table, dbName))
	}
	if err := s.findInstance(ctx, objectID.Int64); err != nil {
		return err
	}
	if err := s.loadColumns(ctx, objectID.Int64); err != nil {
		return err
	}
	return s.checkAgent(ctx)
}

func (s *Source) enableTableHint() string {
	return fmt.Sprintf("EXEC sys.sp_cdc_enable_table @source_schema = N'%s', @source_name = N'%s', @role_name = NULL;",
		strings.ReplaceAll(s.schema, "'", "''"), strings.ReplaceAll(s.table, "'", "''"))
}

// findInstance picks the capture instance of the table.
func (s *Source) findInstance(ctx context.Context, objectID int64) error {
	rows, err := s.db.QueryContext(ctx, "SELECT capture_instance FROM cdc.change_tables WHERE source_object_id = @p1 ORDER BY create_date DESC", objectID)
	if err != nil {
		return classify(fmt.Errorf("sqlserver: read cdc.change_tables (the user needs SELECT on the cdc schema): %w", err))
	}
	defer rows.Close()
	var instances []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return err
		}
		instances = append(instances, name)
	}
	if err := rows.Err(); err != nil {
		return classify(err)
	}
	if len(instances) == 0 {
		return source.Permanent(fmt.Errorf("sqlserver: table %s.%s is not tracked by change data capture; run: %s", s.schema, s.table, s.enableTableHint()))
	}
	want := s.cfg.Slot
	if want == "" {
		want = s.schema + "_" + s.table
	}
	for _, n := range instances {
		if strings.EqualFold(n, want) {
			s.instance = n
			return nil
		}
	}
	if s.cfg.Slot != "" {
		return source.Permanent(fmt.Errorf("sqlserver: capture instance %q does not exist for table %s.%s (found %s)", s.cfg.Slot, s.schema, s.table, strings.Join(instances, ", ")))
	}
	s.instance = instances[0]
	return nil
}

// loadColumns reads the captured columns and the primary key.
func (s *Source) loadColumns(ctx context.Context, objectID int64) error {
	rows, err := s.db.QueryContext(ctx, `
		SELECT c.name, TYPE_NAME(c.user_type_id), c.is_nullable,
		       CAST(CASE WHEN ic.key_ordinal IS NULL THEN 0 ELSE ic.key_ordinal END AS int),
		       CAST(CASE WHEN cc.column_name IS NULL THEN 0 ELSE 1 END AS bit)
		FROM sys.columns c
		LEFT JOIN sys.indexes i ON i.object_id = c.object_id AND i.is_primary_key = 1
		LEFT JOIN sys.index_columns ic ON ic.object_id = c.object_id AND ic.index_id = i.index_id AND ic.column_id = c.column_id
		LEFT JOIN cdc.captured_columns cc ON cc.object_id = OBJECT_ID(@p2) AND cc.column_name = c.name
		WHERE c.object_id = @p1
		ORDER BY c.column_id`, objectID, "cdc."+quote(s.instance+"_CT"))
	if err != nil {
		return classify(fmt.Errorf("sqlserver: read columns: %w", err))
	}
	defer rows.Close()
	pk := map[int]string{}
	s.columns = s.columns[:0]
	for rows.Next() {
		var c column
		var keyOrd int
		var captured bool
		if err := rows.Scan(&c.name, &c.typ, &c.nullable, &keyOrd, &captured); err != nil {
			return fmt.Errorf("sqlserver: read columns: %w", err)
		}
		if keyOrd > 0 {
			c.pk = true
			pk[keyOrd] = c.name
			if !captured {
				return source.Permanent(fmt.Errorf("sqlserver: capture instance %q does not capture primary key column %q; recreate it with all key columns", s.instance, c.name))
			}
		}
		if captured {
			s.columns = append(s.columns, c)
		}
	}
	if err := rows.Err(); err != nil {
		return classify(fmt.Errorf("sqlserver: read columns: %w", err))
	}
	if len(pk) == 0 {
		return source.Permanent(fmt.Errorf("sqlserver: table %s.%s has no primary key; add one so rows can be identified", s.schema, s.table))
	}
	s.pk = s.pk[:0]
	for i := 1; i <= len(pk); i++ {
		s.pk = append(s.pk, pk[i])
	}
	return nil
}

// checkAgent fails when SQL Server Agent is known to be stopped, since no
// change would ever be captured. Servers without the DMV (Azure SQL
// Database) or users without VIEW SERVER STATE skip the check.
func (s *Source) checkAgent(ctx context.Context) error {
	var status string
	err := s.db.QueryRowContext(ctx, "SELECT status_desc FROM sys.dm_server_services WHERE servicename LIKE 'SQL Server Agent%'").Scan(&status)
	if err == nil && !strings.EqualFold(status, "Running") {
		return source.Permanent(fmt.Errorf("sqlserver: SQL Server Agent is %s, so the CDC capture job does not run; start it "+
			"(Linux containers: MSSQL_AGENT_ENABLED=true)", strings.ToLower(status)))
	}
	return nil
}

func (s *Source) columnNames() []string {
	out := make([]string, len(s.columns))
	for i, c := range s.columns {
		out[i] = c.name
	}
	return out
}

// Schema describes the captured columns.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	if s.db == nil {
		return nil, errors.New("sqlserver: source is not open")
	}
	out := &model.Schema{PrimaryKey: append([]string(nil), s.pk...), RowCount: -1}
	for _, c := range s.columns {
		out.Columns = append(out.Columns, model.Column{Name: c.name, Type: c.typ, Nullable: c.nullable, PrimaryKey: c.pk})
	}
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, "SELECT SUM(p.rows) FROM sys.partitions p WHERE p.object_id = OBJECT_ID(@p1) AND p.index_id IN (0, 1)", s.fullName()).Scan(&n)
	if err == nil && n.Valid {
		out.RowCount = n.Int64
	}
	return out, nil
}

// lsnBounds returns the database's maximum captured LSN and the capture
// instance's minimum available LSN.
func (s *Source) lsnBounds(ctx context.Context) (maxLSN, minLSN []byte, err error) {
	err = s.db.QueryRowContext(ctx, "SELECT sys.fn_cdc_get_max_lsn(), sys.fn_cdc_get_min_lsn(@p1)", s.instance).Scan(&maxLSN, &minLSN)
	if err != nil {
		return nil, nil, classify(fmt.Errorf("sqlserver: read lsn bounds: %w", err))
	}
	if len(minLSN) != lsnSize || bytes.Equal(minLSN, zeroLSN) {
		return nil, nil, source.Permanent(fmt.Errorf("sqlserver: capture instance %q no longer exists (CDC was disabled for the table?); %s", s.instance, s.enableTableHint()))
	}
	return maxLSN, minLSN, nil
}

// CurrentPosition returns the position after every change captured so far.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	if s.db == nil {
		return "", errors.New("sqlserver: source is not open")
	}
	maxLSN, minLSN, err := s.lsnBounds(ctx)
	if err != nil {
		return "", err
	}
	return startPosition(maxLSN, minLSN).format(), nil
}

// Snapshot copies the captured columns with keyset pagination over the
// primary key; rows changed meanwhile are replayed by the stream.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	if s.db == nil {
		return errors.New("sqlserver: source is not open")
	}
	cols := s.columnNames()
	pkIdx := make([]int, len(s.pk))
	for i, k := range s.pk {
		for j, c := range cols {
			if c == k {
				pkIdx[i] = j
			}
		}
	}
	first, next := buildSnapshotQueries(s.schema, s.table, cols, s.pk, snapshotChunk)
	var last []any
	for {
		query := first
		if last != nil {
			query = next
		}
		rows, err := s.db.QueryContext(ctx, query, last...)
		if err != nil {
			return classify(fmt.Errorf("sqlserver: snapshot query: %w", err))
		}
		types, err := rows.ColumnTypes()
		if err != nil {
			rows.Close()
			return fmt.Errorf("sqlserver: snapshot query: %w", err)
		}
		events := make([]model.Event, 0, snapshotChunk)
		var lastRaw []any
		for rows.Next() {
			vals, err := scanRow(rows, len(cols))
			if err != nil {
				rows.Close()
				return fmt.Errorf("sqlserver: snapshot row: %w", err)
			}
			row := make(map[string]any, len(cols))
			for i, c := range cols {
				row[c] = convertValue(types[i].DatabaseTypeName(), vals[i])
			}
			events = append(events, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
			lastRaw = vals
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return classify(fmt.Errorf("sqlserver: snapshot query: %w", err))
		}
		if len(events) > 0 {
			if err := emit(ctx, events); err != nil {
				return err
			}
		}
		if len(events) < snapshotChunk {
			return nil
		}
		last = make([]any, len(pkIdx))
		for i, idx := range pkIdx {
			last[i] = keyParam(types[idx].DatabaseTypeName(), lastRaw[idx])
		}
	}
}

// Stream polls the change table and emits the rows after from, one page of
// whole transactions at a time.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if s.db == nil {
		return errors.New("sqlserver: source is not open")
	}
	if from == "" {
		p, err := s.CurrentPosition(ctx)
		if err != nil {
			return err
		}
		from = p
	}
	cursor, err := parsePosition(from)
	if err != nil {
		return err
	}
	cols := s.columnNames()
	query := buildChangesQuery(s.instance, cols, changesLimit)
	for {
		maxLSN, minLSN, err := s.lsnBounds(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
		if bytes.Compare(cursor.lsn, minLSN) < 0 {
			return fmt.Errorf("sqlserver: position %s is older than the minimum available lsn %x of capture instance %q (removed by the cleanup job): %w",
				cursor.format(), minLSN, s.instance, source.ErrPositionLost)
		}
		full := false
		// Only rows up to the max LSN are complete; skip the query when the
		// cursor is already past every captured row.
		if len(maxLSN) == lsnSize && cursor.less(position{lsn: maxLSN, seq: maxSeq}) {
			events, next, isFull, err := s.readChanges(ctx, query, cols, cursor, maxLSN)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return err
			}
			if len(events) > 0 {
				if err := emit(ctx, events); err != nil {
					return err
				}
			}
			cursor, full = next, isFull
		}
		if full {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

// readChanges reads one page of change rows after cursor.
func (s *Source) readChanges(ctx context.Context, query string, cols []string, cursor position, maxLSN []byte) ([]model.Event, position, bool, error) {
	rows, err := s.db.QueryContext(ctx, query, cursor.lsn, cursor.seq, maxLSN)
	if err != nil {
		return nil, cursor, false, classify(fmt.Errorf("sqlserver: read change table: %w", err))
	}
	defer rows.Close()
	types, err := rows.ColumnTypes()
	if err != nil {
		return nil, cursor, false, fmt.Errorf("sqlserver: read change table: %w", err)
	}
	var meta []changeRow
	var times []time.Time
	var data [][]any
	for rows.Next() {
		vals, err := scanRow(rows, 4+len(cols))
		if err != nil {
			return nil, cursor, false, fmt.Errorf("sqlserver: read change row: %w", err)
		}
		lsn, _ := vals[0].([]byte)
		seq, _ := vals[1].([]byte)
		op, _ := vals[2].(int64)
		t, _ := vals[3].(time.Time)
		meta = append(meta, changeRow{lsn: lsn, seq: seq, op: int(op)})
		times = append(times, t)
		data = append(data, vals[4:])
	}
	if err := rows.Err(); err != nil {
		return nil, cursor, false, classify(fmt.Errorf("sqlserver: read change table: %w", err))
	}
	full := len(meta) >= changesLimit
	keep, positions := plan(meta, cursor, full)
	events := make([]model.Event, 0, keep)
	for i := 0; i < keep; i++ {
		ev := model.Event{Position: positions[i].format()}
		if !times[i].IsZero() {
			ev.Time = time.Date(times[i].Year(), times[i].Month(), times[i].Day(), times[i].Hour(), times[i].Minute(), times[i].Second(), times[i].Nanosecond(), time.UTC)
		}
		switch meta[i].op {
		case 1:
			ev.Op = model.OpDelete
			ev.Row = make(map[string]any, len(s.pk))
			for j, c := range cols {
				for _, k := range s.pk {
					if c == k {
						ev.Row[c] = convertValue(types[4+j].DatabaseTypeName(), data[i][j])
					}
				}
			}
		case 2, 4:
			ev.Op = model.OpUpsert
			ev.Row = make(map[string]any, len(cols))
			for j, c := range cols {
				ev.Row[c] = convertValue(types[4+j].DatabaseTypeName(), data[i][j])
			}
		default:
			continue
		}
		events = append(events, ev)
	}
	next := cursor
	if keep > 0 {
		next = positions[keep-1]
	}
	return events, next, full, nil
}

// Ack is a no-op: the change tables are cleaned up by SQL Server's
// retention based cleanup job, not by consumers.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close closes the connection pool.
func (s *Source) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// withDatabase sets the database of a DSN when db is not empty.
func withDatabase(dsn, db string) (string, error) {
	if db == "" {
		return dsn, nil
	}
	if strings.HasPrefix(strings.ToLower(dsn), "sqlserver://") {
		u, err := url.Parse(dsn)
		if err != nil {
			return "", fmt.Errorf("sqlserver: invalid dsn: %w", err)
		}
		q := u.Query()
		q.Set("database", db)
		u.RawQuery = q.Encode()
		return u.String(), nil
	}
	return strings.TrimRight(dsn, "; ") + ";database=" + db, nil
}

// classify marks errors no retry can fix as permanent.
func classify(err error) error {
	var me mssql.Error
	if errors.As(err, &me) {
		switch me.Number {
		case 18456: // login failed
			return source.Permanent(fmt.Errorf("%w (check the user and password in the dsn)", err))
		case 4060: // cannot open database
			return source.Permanent(fmt.Errorf("%w (check the database name and that the login has a user in it)", err))
		case 229, 230, 262, 297, 300: // permission denied
			return source.Permanent(fmt.Errorf("%w (the user needs SELECT on the table and the cdc schema)", err))
		case 208: // invalid object name
			return source.Permanent(err)
		}
	}
	return err
}
