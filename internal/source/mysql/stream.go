package mysql

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"regexp"
	"strings"
	"time"

	gomysql "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// eventBuffer bounds how many decoded binlog events the syncer reads ahead
// of Emit.
const eventBuffer = 128

// Stream reads the binlog from the given coordinates and emits one batch per
// committed transaction touching the table.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if s.db == nil {
		return fmt.Errorf("%s: source is not open", s.flavor)
	}
	if from == "" {
		p, err := s.CurrentPosition(ctx)
		if err != nil {
			return err
		}
		from = p
	}
	file, pos, err := ParsePosition(from)
	if err != nil {
		return source.Permanent(fmt.Errorf("%s: %w", s.flavor, err))
	}
	files, err := s.binlogFiles(ctx)
	if err != nil {
		return s.classify(fmt.Errorf("%s: list binary logs (the user needs REPLICATION CLIENT): %w", s.flavor, err))
	}
	if !files[file] {
		return fmt.Errorf("%s: binlog %s was purged from the server: %w", s.flavor, file, source.ErrPositionLost)
	}

	cfg := replication.BinlogSyncerConfig{
		ServerID:             s.serverID,
		Flavor:               s.flavor,
		Host:                 s.host,
		Port:                 s.port,
		User:                 s.dsn.User,
		Password:             s.dsn.Passwd,
		ParseTime:            true,
		UseDecimal:           false,
		EventCacheCount:      eventBuffer,
		MaxReconnectAttempts: 5,
		HeartbeatPeriod:      30 * time.Second,
		ReadTimeout:          90 * time.Second,
		TLSConfig:            s.dsn.TLS,
		Logger:               slog.Default().With("component", "binlog", "table", s.database+"."+s.table),
	}
	if s.flavor == "mariadb" {
		cfg.FillZeroLogPos = true
	}
	syncer := replication.NewBinlogSyncer(cfg)
	defer syncer.Close()
	streamer, err := syncer.StartSync(gomysql.Position{Name: file, Pos: pos})
	if err != nil {
		return s.streamError(err)
	}

	st := &binlogStream{src: s, emit: emit, file: file}
	for {
		ev, err := streamer.GetEvent(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return s.streamError(err)
		}
		if err := st.handle(ctx, ev); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return err
		}
	}
}

// streamError maps replication errors: a purged binlog (1236) means the
// position is lost; missing privileges are permanent.
func (s *Source) streamError(err error) error {
	var me *gomysql.MyError
	if errors.As(err, &me) {
		switch me.Code {
		case gomysql.ER_MASTER_FATAL_ERROR_READING_BINLOG:
			return fmt.Errorf("%s: %v: %w", s.flavor, err, source.ErrPositionLost)
		case gomysql.ER_ACCESS_DENIED_ERROR, gomysql.ER_SPECIFIC_ACCESS_DENIED_ERROR, gomysql.ER_DBACCESS_DENIED_ERROR:
			return source.Permanent(fmt.Errorf("%s: replication: %w (grant REPLICATION SLAVE, REPLICATION CLIENT ON *.* to the user)", s.flavor, err))
		}
	}
	if strings.Contains(err.Error(), "1236") {
		return fmt.Errorf("%s: %v: %w", s.flavor, err, source.ErrPositionLost)
	}
	return fmt.Errorf("%s: replication: %w", s.flavor, err)
}

type binlogStream struct {
	src  *Source
	emit source.EmitFunc
	file string // current binlog file
	tx   []model.Event
	// dirty is set when DDL touched the table; columns are reloaded before
	// the next rows event is decoded.
	dirty bool
}

func (st *binlogStream) handle(ctx context.Context, ev *replication.BinlogEvent) error {
	switch e := ev.Event.(type) {
	case *replication.RotateEvent:
		st.file = string(e.NextLogName)
	case *replication.RowsEvent:
		if e.Table == nil || string(e.Table.Schema) != st.src.database || string(e.Table.Table) != st.src.table {
			return nil
		}
		return st.rows(ctx, e)
	case *replication.XIDEvent:
		return st.commit(ctx, ev.Header)
	case *replication.QueryEvent:
		return st.query(ctx, ev.Header, e)
	}
	return nil
}

func (st *binlogStream) query(ctx context.Context, h *replication.EventHeader, e *replication.QueryEvent) error {
	q := strings.TrimSpace(string(e.Query))
	switch strings.ToUpper(q) {
	case "BEGIN", "":
		return nil
	case "COMMIT": // transactions on non transactional engines
		return st.commit(ctx, h)
	}
	verb, schema, table, ok := ddlTarget(q, string(e.Schema))
	if !ok || schema != st.src.database || table != st.src.table {
		return nil
	}
	// DDL commits implicitly; anything pending belongs before it.
	if err := st.commit(ctx, h); err != nil {
		return err
	}
	st.dirty = true
	if verb == "TRUNCATE" {
		st.tx = append(st.tx, model.Event{Op: model.OpTruncate, Row: map[string]any{}})
		return st.commit(ctx, h)
	}
	return nil
}

var ddlPattern = regexp.MustCompile("(?is)^(?:/\\*.*?\\*/\\s*)*(ALTER|TRUNCATE|DROP|CREATE|RENAME)\\s+" +
	"(?:(?:ONLINE|IGNORE|TEMPORARY|OR\\s+REPLACE)\\s+)*(?:TABLE\\s+)?(?:IF\\s+(?:NOT\\s+)?EXISTS\\s+)?" +
	"((?:`(?:[^`]|``)+`|[\\w$]+)(?:\\s*\\.\\s*(?:`(?:[^`]|``)+`|[\\w$]+))?)")

// ddlTarget returns the statement verb and the first table a DDL statement
// names, qualified with defaultSchema when the statement does not say.
func ddlTarget(query, defaultSchema string) (verb, schema, table string, ok bool) {
	m := ddlPattern.FindStringSubmatch(strings.TrimSpace(query))
	if m == nil {
		return "", "", "", false
	}
	verb = strings.ToUpper(m[1])
	parts := splitQualified(m[2])
	switch len(parts) {
	case 1:
		return verb, defaultSchema, parts[0], true
	case 2:
		return verb, parts[0], parts[1], true
	}
	return "", "", "", false
}

func splitQualified(s string) []string {
	var parts []string
	for s != "" {
		s = strings.TrimSpace(s)
		var part string
		if strings.HasPrefix(s, "`") {
			end := 1
			for end < len(s) {
				if s[end] == '`' {
					if end+1 < len(s) && s[end+1] == '`' {
						end += 2
						continue
					}
					break
				}
				end++
			}
			part = strings.ReplaceAll(s[1:min(end, len(s))], "``", "`")
			s = s[min(end+1, len(s)):]
		} else {
			i := strings.IndexByte(s, '.')
			if i < 0 {
				i = len(s)
			}
			part = strings.TrimSpace(s[:i])
			s = s[i:]
		}
		parts = append(parts, part)
		s = strings.TrimPrefix(strings.TrimSpace(s), ".")
	}
	return parts
}

// rows decodes a rows event into the current transaction.
func (st *binlogStream) rows(ctx context.Context, e *replication.RowsEvent) error {
	cols, err := st.columnsFor(ctx, e.Table)
	if err != nil {
		return err
	}
	_, pk := st.src.snapshotColumns()
	switch e.Type() {
	case replication.EnumRowsEventTypeInsert:
		for _, r := range e.Rows {
			row, err := rowFromValues(cols, r, true)
			if err != nil {
				return fmt.Errorf("%s: %w", st.src.flavor, err)
			}
			st.tx = append(st.tx, model.Event{Op: model.OpUpsert, Row: row})
		}
	case replication.EnumRowsEventTypeUpdate:
		for i := 0; i+1 < len(e.Rows); i += 2 {
			before, err := rowFromValues(cols, e.Rows[i], true)
			if err != nil {
				return fmt.Errorf("%s: %w", st.src.flavor, err)
			}
			after, err := rowFromValues(cols, e.Rows[i+1], true)
			if err != nil {
				return fmt.Errorf("%s: %w", st.src.flavor, err)
			}
			if key, changed := keyChange(pk, before, after); changed {
				st.tx = append(st.tx, model.Event{Op: model.OpDelete, Row: key})
			}
			st.tx = append(st.tx, model.Event{Op: model.OpUpsert, Row: after})
		}
	case replication.EnumRowsEventTypeDelete:
		for _, r := range e.Rows {
			row, err := rowFromValues(cols, r, true)
			if err != nil {
				return fmt.Errorf("%s: %w", st.src.flavor, err)
			}
			st.tx = append(st.tx, model.Event{Op: model.OpDelete, Row: row})
		}
	}
	return nil
}

// columnsFor returns the columns in binlog order. Column names logged by the
// server (binlog_row_metadata=FULL) are preferred, since information_schema
// shows the current definition rather than the one at write time.
func (st *binlogStream) columnsFor(ctx context.Context, tm *replication.TableMapEvent) ([]column, error) {
	for attempt := 0; attempt < 2; attempt++ {
		if st.dirty || attempt > 0 {
			if err := st.src.loadColumns(ctx); err != nil {
				return nil, err
			}
			st.dirty = false
		}
		cols, _ := st.src.snapshotColumns()
		if names := tm.ColumnNameString(); len(names) == int(tm.ColumnCount) {
			byName := make(map[string]column, len(cols))
			for _, c := range cols {
				byName[c.name] = c
			}
			out := make([]column, len(names))
			complete := true
			for i, n := range names {
				c, ok := byName[n]
				if !ok {
					complete = false
					break
				}
				out[i] = c
			}
			if complete {
				return out, nil
			}
			continue
		}
		if len(cols) == int(tm.ColumnCount) {
			return cols, nil
		}
	}
	return nil, fmt.Errorf("%s: binlog has %d columns for %s.%s but the table has a different definition now; resync the task",
		st.src.flavor, tm.ColumnCount, st.src.database, st.src.table)
}

// commit stamps the buffered transaction with the coordinates after the
// event that ended it and emits it.
func (st *binlogStream) commit(ctx context.Context, h *replication.EventHeader) error {
	if len(st.tx) == 0 {
		return nil
	}
	pos := FormatPosition(st.file, h.LogPos)
	at := time.Unix(int64(h.Timestamp), 0).UTC()
	for i := range st.tx {
		st.tx[i].Position = pos
		st.tx[i].Time = at
	}
	events := st.tx
	st.tx = nil
	return st.emit(ctx, events)
}

// keyChange reports whether an update changed the primary key and returns
// the old key.
func keyChange(pk []string, before, after map[string]any) (map[string]any, bool) {
	if len(pk) == 0 {
		return nil, false
	}
	key := make(map[string]any, len(pk))
	changed := false
	for _, k := range pk {
		key[k] = before[k]
		if !reflect.DeepEqual(before[k], after[k]) {
			changed = true
		}
	}
	return key, changed
}
