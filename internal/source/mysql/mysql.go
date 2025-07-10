// Package mysql captures changes from MySQL and MariaDB by reading the binary
// log as a replica (go-mysql's BinlogSyncer).
//
// Positions are binlog coordinates "<file>:<offset>", for example
// "mysql-bin.000003:1234". A stream event carries the coordinates right
// after its transaction's commit (XID) event, so acknowledging it confirms
// the whole transaction and restarting from it resumes with the next one.
package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"hash/fnv"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	mysqldrv "github.com/go-sql-driver/mysql"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// snapshotChunk is the number of rows read per snapshot query.
const snapshotChunk = 5000

func init() {
	source.Register(model.SourceMySQL, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg, taskID, "mysql")
	})
	source.Register(model.SourceMariaDB, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg, taskID, "mariadb")
	})
}

// Source streams one table of a MySQL or MariaDB server.
type Source struct {
	flavor   string // "mysql" or "mariadb"
	dsn      *mysqldrv.Config
	database string
	table    string
	serverID uint32
	host     string
	port     uint16

	db *sql.DB

	mu      sync.Mutex // guards columns
	columns []column
	pk      []string
}

// New builds a source; flavor is "mysql" or "mariadb".
func New(cfg model.Source, taskID, flavor string) (*Source, error) {
	if cfg.DSN == "" {
		return nil, source.Permanent(fmt.Errorf("%s: dsn is required", flavor))
	}
	if cfg.Table == "" {
		return nil, source.Permanent(fmt.Errorf("%s: table is required", flavor))
	}
	dc, err := mysqldrv.ParseDSN(cfg.DSN)
	if err != nil {
		return nil, source.Permanent(fmt.Errorf("%s: parse dsn (want user:pass@tcp(host:port)/db): %w", flavor, err))
	}
	s := &Source{flavor: flavor, dsn: dc, database: cfg.Database, table: cfg.Table, serverID: cfg.ServerID}
	if s.database == "" {
		s.database = dc.DBName
	}
	if s.database == "" {
		return nil, source.Permanent(fmt.Errorf("%s: no database: set source.database or put it in the dsn", flavor))
	}
	if s.serverID == 0 {
		s.serverID = serverIDFor(taskID)
	}
	if dc.Net != "tcp" && dc.Net != "tcp6" && dc.Net != "" {
		return nil, source.Permanent(fmt.Errorf("%s: binlog replication needs a tcp address, got %q", flavor, dc.Net))
	}
	host, port, err := net.SplitHostPort(dc.Addr)
	if err != nil {
		host, port = dc.Addr, "3306"
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, source.Permanent(fmt.Errorf("%s: invalid port %q", flavor, port))
	}
	s.host, s.port = host, uint16(p)
	return s, nil
}

// serverIDFor derives a stable replica server id in [1000, 2^31) from the
// task id, so two tasks on one server do not kick each other off.
func serverIDFor(taskID string) uint32 {
	h := fnv.New32a()
	h.Write([]byte(taskID))
	const lo, hi = 1000, 1 << 31
	return lo + h.Sum32()%(hi-lo)
}

// Open connects and validates the binlog configuration and the table.
func (s *Source) Open(ctx context.Context) error {
	c := s.dsn.Clone()
	c.DBName = s.database
	c.ParseTime = true
	c.Loc = time.UTC
	if c.Params == nil {
		c.Params = map[string]string{}
	}
	c.Params["time_zone"] = "'+00:00'" // TIMESTAMP values in UTC, like the binlog
	conn, err := mysqldrv.NewConnector(c)
	if err != nil {
		return source.Permanent(fmt.Errorf("%s: %w", s.flavor, err))
	}
	s.db = sql.OpenDB(conn)
	s.db.SetMaxOpenConns(4)
	if err := s.db.PingContext(ctx); err != nil {
		return s.classify(fmt.Errorf("%s: connect: %w", s.flavor, err))
	}

	var logBin, format, image string
	err = s.db.QueryRowContext(ctx, "SELECT @@GLOBAL.log_bin, @@GLOBAL.binlog_format, @@GLOBAL.binlog_row_image").Scan(&logBin, &format, &image)
	if err != nil {
		return s.classify(fmt.Errorf("%s: read binlog settings: %w", s.flavor, err))
	}
	if logBin != "1" && !strings.EqualFold(logBin, "ON") {
		return source.Permanent(fmt.Errorf("%s: binary logging is disabled; start the server with --log-bin (and --server-id)", s.flavor))
	}
	if !strings.EqualFold(format, "ROW") {
		return source.Permanent(fmt.Errorf("%s: binlog_format is %s but change capture needs ROW; run SET GLOBAL binlog_format = 'ROW' and persist it in the server config", s.flavor, format))
	}
	if !strings.EqualFold(image, "FULL") {
		return source.Permanent(fmt.Errorf("%s: binlog_row_image is %s but change capture needs FULL; run SET GLOBAL binlog_row_image = 'FULL' and persist it in the server config", s.flavor, image))
	}
	if s.flavor == "mysql" {
		var opts string
		if err := s.db.QueryRowContext(ctx, "SELECT @@GLOBAL.binlog_row_value_options").Scan(&opts); err == nil && strings.Contains(strings.ToUpper(opts), "PARTIAL_JSON") {
			return source.Permanent(fmt.Errorf("mysql: binlog_row_value_options = PARTIAL_JSON logs JSON diffs; set it to '' so full values are logged"))
		}
	}
	return s.loadColumns(ctx)
}

var jsonCheck = regexp.MustCompile("(?i)json_valid\\(`([^`]+)`\\)")

// loadColumns reads the table definition from information_schema.
func (s *Source) loadColumns(ctx context.Context) error {
	jsonCols := map[string]bool{}
	if s.flavor == "mariadb" {
		rows, err := s.db.QueryContext(ctx, `SELECT CHECK_CLAUSE FROM information_schema.CHECK_CONSTRAINTS
			WHERE CONSTRAINT_SCHEMA = ? AND TABLE_NAME = ?`, s.database, s.table)
		if err == nil {
			for rows.Next() {
				var clause string
				if rows.Scan(&clause) == nil {
					for _, m := range jsonCheck.FindAllStringSubmatch(clause, -1) {
						jsonCols[m[1]] = true
					}
				}
			}
			rows.Close()
		}
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT COLUMN_NAME, DATA_TYPE, COLUMN_TYPE, IS_NULLABLE = 'YES', COLUMN_KEY = 'PRI'
		FROM information_schema.COLUMNS
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?
		ORDER BY ORDINAL_POSITION`, s.database, s.table)
	if err != nil {
		return s.classify(fmt.Errorf("%s: read columns: %w", s.flavor, err))
	}
	defer rows.Close()
	var cols []column
	for rows.Next() {
		var name, dataType, columnType string
		var nullable, pk bool
		if err := rows.Scan(&name, &dataType, &columnType, &nullable, &pk); err != nil {
			return fmt.Errorf("%s: read columns: %w", s.flavor, err)
		}
		cols = append(cols, newColumn(name, dataType, columnType, nullable, pk, jsonCols[name]))
	}
	if err := rows.Err(); err != nil {
		return s.classify(fmt.Errorf("%s: read columns: %w", s.flavor, err))
	}
	if len(cols) == 0 {
		return source.Permanent(fmt.Errorf("%s: table %s.%s does not exist (or the user cannot see it)", s.flavor, s.database, s.table))
	}

	krows, err := s.db.QueryContext(ctx, `
		SELECT COLUMN_NAME FROM information_schema.KEY_COLUMN_USAGE
		WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ? AND CONSTRAINT_NAME = 'PRIMARY'
		ORDER BY ORDINAL_POSITION`, s.database, s.table)
	if err != nil {
		return s.classify(fmt.Errorf("%s: read primary key: %w", s.flavor, err))
	}
	defer krows.Close()
	var pk []string
	for krows.Next() {
		var name string
		if err := krows.Scan(&name); err != nil {
			return err
		}
		pk = append(pk, name)
	}
	if err := krows.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	s.columns, s.pk = cols, pk
	s.mu.Unlock()
	return nil
}

func (s *Source) snapshotColumns() ([]column, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.columns, s.pk
}

// Schema describes the table.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	if s.db == nil {
		return nil, fmt.Errorf("%s: source is not open", s.flavor)
	}
	if err := s.loadColumns(ctx); err != nil {
		return nil, err
	}
	cols, pk := s.snapshotColumns()
	out := &model.Schema{PrimaryKey: append([]string(nil), pk...), RowCount: -1}
	for _, c := range cols {
		out.Columns = append(out.Columns, model.Column{Name: c.name, Type: c.columnType, Nullable: c.nullable, PrimaryKey: c.pk})
	}
	var n sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT TABLE_ROWS FROM information_schema.TABLES WHERE TABLE_SCHEMA = ? AND TABLE_NAME = ?`,
		s.database, s.table).Scan(&n)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, s.classify(fmt.Errorf("%s: read row estimate: %w", s.flavor, err))
	}
	if n.Valid {
		out.RowCount = n.Int64
	}
	return out, nil
}

// CurrentPosition returns the server's current binlog coordinates.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	if s.db == nil {
		return "", fmt.Errorf("%s: source is not open", s.flavor)
	}
	var lastErr error
	// SHOW MASTER STATUS was removed in MySQL 8.4; MariaDB names it SHOW BINLOG STATUS.
	for _, q := range []string{"SHOW BINARY LOG STATUS", "SHOW MASTER STATUS", "SHOW BINLOG STATUS"} {
		file, pos, err := s.queryStatus(ctx, q)
		if err != nil {
			lastErr = err
			continue
		}
		if file == "" {
			return "", source.Permanent(fmt.Errorf("%s: %s returned no binlog; enable binary logging with --log-bin", s.flavor, q))
		}
		return FormatPosition(file, pos), nil
	}
	return "", s.classify(fmt.Errorf("%s: read binlog position (the user needs REPLICATION CLIENT): %w", s.flavor, lastErr))
}

func (s *Source) queryStatus(ctx context.Context, q string) (string, uint32, error) {
	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return "", 0, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return "", 0, err
	}
	if !rows.Next() {
		return "", 0, rows.Err()
	}
	vals := make([]sql.RawBytes, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return "", 0, err
	}
	pos, err := strconv.ParseUint(string(vals[1]), 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("parse binlog offset %q: %w", vals[1], err)
	}
	return string(vals[0]), uint32(pos), nil
}

// binlogFiles lists the binlog files the server still has.
func (s *Source) binlogFiles(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, "SHOW BINARY LOGS")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for rows.Next() {
		vals := make([]sql.RawBytes, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		out[string(vals[0])] = true
	}
	return out, rows.Err()
}

// Ack is a no-op: binlog positions are tracked by the engine, the server
// keeps no per-replica state.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close releases the connection pool.
func (s *Source) Close() error {
	if s.db == nil {
		return nil
	}
	err := s.db.Close()
	s.db = nil
	return err
}

// FormatPosition renders binlog coordinates as a position.
func FormatPosition(file string, pos uint32) model.Position {
	return model.Position(file + ":" + strconv.FormatUint(uint64(pos), 10))
}

// ParsePosition parses a position produced by FormatPosition.
func ParsePosition(p model.Position) (string, uint32, error) {
	i := strings.LastIndexByte(string(p), ':')
	if i <= 0 {
		return "", 0, fmt.Errorf("invalid binlog position %q (want file:offset)", p)
	}
	pos, err := strconv.ParseUint(string(p[i+1:]), 10, 32)
	if err != nil {
		return "", 0, fmt.Errorf("invalid binlog position %q (want file:offset): %w", p, err)
	}
	return string(p[:i]), uint32(pos), nil
}

// MySQL error numbers that no retry fixes.
var permanentCodes = map[uint16]string{
	1044: "check the database privileges of the user",
	1045: "check the user and password in the dsn",
	1049: "check the database name",
	1146: "check the table name",
	1142: "grant SELECT on the table",
	1227: "grant REPLICATION SLAVE, REPLICATION CLIENT ON *.*",
	1236: "",
}

// classify marks errors no retry can fix as permanent.
func (s *Source) classify(err error) error {
	var me *mysqldrv.MySQLError
	if errors.As(err, &me) {
		if hint, ok := permanentCodes[me.Number]; ok && me.Number != 1236 {
			return source.Permanent(fmt.Errorf("%w (%s)", err, hint))
		}
	}
	return err
}
