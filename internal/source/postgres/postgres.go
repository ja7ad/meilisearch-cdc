// Package postgres captures changes from PostgreSQL with logical replication
// (the built in pgoutput plugin).
//
// Positions are WAL LSNs in Postgres' textual form, for example "16/B374D848".
// A stream event carries the end LSN of its transaction's commit record, so
// acknowledging it confirms the whole transaction and restarting from it
// resumes with the next transaction.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// snapshotChunk is the number of rows read per snapshot query.
const snapshotChunk = 5000

func init() {
	source.Register(model.SourcePostgres, New)
}

// Source streams one table. It implements source.Source and source.Dropper.
type Source struct {
	cfg         model.Source
	schema      string
	table       string
	slot        string
	publication string
	// ownsPublication is true when the publication name was generated, so
	// Drop may remove it. A user supplied publication may serve other tables.
	ownsPublication bool

	mu   sync.Mutex // guards conn
	conn *pgx.Conn

	// columns of the table, loaded by Open.
	columns []column
	pk      []string

	ack ackState
}

type column struct {
	name     string
	typ      string // format_type(), used for casts
	oid      uint32
	nullable bool
	pk       bool
}

// New is the source.Factory for Postgres.
func New(cfg model.Source, taskID string) (source.Source, error) {
	if cfg.DSN == "" {
		return nil, source.Permanent(errors.New("postgres: dsn is required"))
	}
	if cfg.Table == "" {
		return nil, source.Permanent(errors.New("postgres: table is required"))
	}
	s := &Source{cfg: cfg, schema: cfg.Schema, table: cfg.Table, slot: cfg.Slot, publication: cfg.Publication}
	if s.schema == "" {
		s.schema = "public"
	}
	def := defaultName(taskID)
	if s.slot == "" {
		s.slot = def
	}
	if s.publication == "" {
		s.publication = def
		s.ownsPublication = true
	}
	return s, nil
}

// defaultName builds a slot or publication name from a task id: "cdc_"
// followed by the id lowercased, with anything outside [a-z0-9_] replaced
// by "_", limited to Postgres' 63 byte identifier length.
func defaultName(taskID string) string {
	var b strings.Builder
	b.WriteString("cdc_")
	for _, r := range strings.ToLower(taskID) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	name := b.String()
	if len(name) > 63 {
		name = name[:63]
	}
	return name
}

func (s *Source) ident() string { return pgx.Identifier{s.schema, s.table}.Sanitize() }

// Open connects, validates the server configuration and the table, and makes
// sure the publication exists.
func (s *Source) Open(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, s.cfg.DSN)
	if err != nil {
		return classify(fmt.Errorf("postgres: connect: %w", err))
	}
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()

	var walLevel string
	if err := conn.QueryRow(ctx, "SHOW wal_level").Scan(&walLevel); err != nil {
		return classify(fmt.Errorf("postgres: read wal_level: %w", err))
	}
	if walLevel != "logical" {
		return source.Permanent(fmt.Errorf("postgres: wal_level is %q but logical replication needs \"logical\"; "+
			"set wal_level = logical in postgresql.conf (or ALTER SYSTEM SET wal_level = logical) and restart the server", walLevel))
	}
	if err := s.loadColumns(ctx); err != nil {
		return err
	}
	return s.ensurePublication(ctx)
}

// loadColumns reads the table definition and checks it can be replicated.
func (s *Source) loadColumns(ctx context.Context) error {
	conn := s.conn
	var relID uint32
	var replIdent string
	err := conn.QueryRow(ctx, `
		SELECT c.oid, c.relreplident::text
		FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2 AND c.relkind IN ('r', 'p')`,
		s.schema, s.table).Scan(&relID, &replIdent)
	if errors.Is(err, pgx.ErrNoRows) {
		return source.Permanent(fmt.Errorf("postgres: table %s.%s does not exist (or is not visible to this user)", s.schema, s.table))
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: look up table: %w", err))
	}

	rows, err := conn.Query(ctx, `
		SELECT a.attname, format_type(a.atttypid, a.atttypmod), a.atttypid, NOT a.attnotnull,
		       COALESCE((SELECT k.ord FROM unnest(i.indkey) WITH ORDINALITY k(attnum, ord) WHERE k.attnum = a.attnum), 0)::int4
		FROM pg_attribute a
		LEFT JOIN pg_index i ON i.indrelid = a.attrelid AND i.indisprimary
		WHERE a.attrelid = $1 AND a.attnum > 0 AND NOT a.attisdropped
		ORDER BY a.attnum`, relID)
	if err != nil {
		return classify(fmt.Errorf("postgres: read columns: %w", err))
	}
	cols, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (struct {
		column
		pkPos int32
	}, error) {
		var c struct {
			column
			pkPos int32
		}
		err := r.Scan(&c.name, &c.typ, &c.oid, &c.nullable, &c.pkPos)
		c.pk = c.pkPos > 0
		return c, err
	})
	if err != nil {
		return classify(fmt.Errorf("postgres: read columns: %w", err))
	}
	s.columns = s.columns[:0]
	pk := map[int32]string{}
	for _, c := range cols {
		s.columns = append(s.columns, c.column)
		if c.pk {
			pk[c.pkPos] = c.name
		}
	}
	s.pk = s.pk[:0]
	for i := int32(1); i <= int32(len(pk)); i++ {
		s.pk = append(s.pk, pk[i])
	}

	if len(s.pk) == 0 && replIdent != "f" {
		return source.Permanent(fmt.Errorf("postgres: table %s.%s has no primary key; "+
			"add one or run ALTER TABLE %s REPLICA IDENTITY FULL so deletes can be captured", s.schema, s.table, s.ident()))
	}
	if replIdent == "n" {
		return source.Permanent(fmt.Errorf("postgres: table %s.%s has REPLICA IDENTITY NOTHING, so deletes carry no key; "+
			"run ALTER TABLE %s REPLICA IDENTITY DEFAULT (or FULL)", s.schema, s.table, s.ident()))
	}
	return nil
}

// ensurePublication creates the publication for the table, or adds the
// table to a generated publication that misses it.
func (s *Source) ensurePublication(ctx context.Context) error {
	var exists, hasTable bool
	err := s.conn.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM pg_publication WHERE pubname = $1),
		       EXISTS (SELECT 1 FROM pg_publication_tables WHERE pubname = $1 AND schemaname = $2 AND tablename = $3)`,
		s.publication, s.schema, s.table).Scan(&exists, &hasTable)
	if err != nil {
		return classify(fmt.Errorf("postgres: read publication: %w", err))
	}
	pub := pgx.Identifier{s.publication}.Sanitize()
	switch {
	case hasTable:
		return nil
	case !exists:
		_, err = s.conn.Exec(ctx, fmt.Sprintf("CREATE PUBLICATION %s FOR TABLE %s", pub, s.ident()))
	case s.ownsPublication:
		_, err = s.conn.Exec(ctx, fmt.Sprintf("ALTER PUBLICATION %s ADD TABLE %s", pub, s.ident()))
	default:
		return source.Permanent(fmt.Errorf("postgres: publication %q does not include table %s.%s; "+
			"run ALTER PUBLICATION %s ADD TABLE %s", s.publication, s.schema, s.table, pub, s.ident()))
	}
	if err != nil {
		return classify(fmt.Errorf("postgres: create publication %q (the user needs CREATE on the database and ownership of the table): %w", s.publication, err))
	}
	return nil
}

// Schema describes the table from the catalog.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	if s.conn == nil {
		return nil, errors.New("postgres: source is not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadColumns(ctx); err != nil {
		return nil, err
	}
	out := &model.Schema{PrimaryKey: append([]string(nil), s.pk...), RowCount: -1}
	for _, c := range s.columns {
		out.Columns = append(out.Columns, model.Column{Name: c.name, Type: c.typ, Nullable: c.nullable, PrimaryKey: c.pk})
	}
	var est float64
	err := s.conn.QueryRow(ctx, `
		SELECT c.reltuples FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = $2`, s.schema, s.table).Scan(&est)
	if err != nil {
		return nil, classify(fmt.Errorf("postgres: read row estimate: %w", err))
	}
	if est >= 0 { // -1 means never vacuumed or analyzed
		out.RowCount = int64(est)
	}
	return out, nil
}

// CurrentPosition creates the replication slot when missing and returns its
// consistent point, or the slot's confirmed flush LSN when it exists.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	if s.conn == nil {
		return "", errors.New("postgres: source is not open")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lsn, found, err := s.slotPosition(ctx)
	if err != nil {
		return "", err
	}
	if found {
		// The slot may lag behind (unacknowledged changes of an earlier run).
		// Callers capture a position right before a snapshot, which already
		// contains everything committed until now, so streaming starts at
		// the current WAL position; the server accepts a start after the
		// slot's confirmed position. Replaying the older changes would undo
		// newer snapshot data for a while and trip the delete guard.
		var current string
		if err := s.conn.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&current); err == nil {
			if cur, err := pglogrepl.ParseLSN(current); err == nil && cur > lsn {
				lsn = cur
			}
		}
		return model.Position(lsn.String()), nil
	}
	var created string
	err = s.conn.QueryRow(ctx, "SELECT lsn::text FROM pg_create_logical_replication_slot($1, 'pgoutput')", s.slot).Scan(&created)
	if err != nil {
		return "", classify(fmt.Errorf("postgres: create replication slot %q (the user needs the REPLICATION attribute and a free max_replication_slots entry): %w", s.slot, err))
	}
	return model.Position(created), nil
}

// slotPosition returns the slot's confirmed flush LSN. It reports
// ErrPositionLost when the slot was invalidated (WAL removed).
func (s *Source) slotPosition(ctx context.Context) (pglogrepl.LSN, bool, error) {
	var confirmed *string
	var walStatus *string
	err := s.conn.QueryRow(ctx, `
		SELECT confirmed_flush_lsn::text, wal_status FROM pg_replication_slots WHERE slot_name = $1`,
		s.slot).Scan(&confirmed, &walStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, classify(fmt.Errorf("postgres: read replication slot: %w", err))
	}
	if walStatus != nil && *walStatus == "lost" {
		return 0, true, fmt.Errorf("postgres: replication slot %q was invalidated because its WAL was removed (max_slot_wal_keep_size): %w", s.slot, source.ErrPositionLost)
	}
	if confirmed == nil {
		return 0, true, fmt.Errorf("postgres: slot %q is not a logical slot", s.slot)
	}
	lsn, err := pglogrepl.ParseLSN(*confirmed)
	return lsn, true, err
}

// Ack records that everything up to pos is applied. The stream reports it
// to the server with its next status update; when no stream is running the
// slot is advanced directly.
func (s *Source) Ack(ctx context.Context, pos model.Position) error {
	if pos == "" {
		return nil
	}
	lsn, err := ParsePosition(pos)
	if err != nil {
		return err
	}
	if s.ack.set(lsn) {
		return nil // the running stream sends it
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		s.advanceSlot(ctx, lsn)
	}
	return nil
}

// advanceSlot moves an inactive slot forward, best effort: it fails while
// a walsender still holds the slot, which then confirms the LSN itself.
func (s *Source) advanceSlot(ctx context.Context, lsn pglogrepl.LSN) {
	_, _ = s.conn.Exec(ctx, `
		SELECT pg_replication_slot_advance(slot_name, $2::text::pg_lsn) FROM pg_replication_slots
		WHERE slot_name = $1 AND NOT active AND confirmed_flush_lsn < $2::text::pg_lsn`, s.slot, lsn.String())
}

// Close releases the connection. Server side resources are kept (see Drop).
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

// Drop removes the replication slot, terminating a walsender that still
// holds it, and the publication when it was generated by this source.
func (s *Source) Drop(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, s.cfg.DSN)
	if err != nil {
		return classify(fmt.Errorf("postgres: connect: %w", err))
	}
	defer conn.Close(context.Background())

	for {
		var active bool
		var pid *int32
		err := conn.QueryRow(ctx, "SELECT active, active_pid FROM pg_replication_slots WHERE slot_name = $1", s.slot).Scan(&active, &pid)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return fmt.Errorf("postgres: read replication slot: %w", err)
		}
		if active && pid != nil {
			if _, err := conn.Exec(ctx, "SELECT pg_terminate_backend($1)", *pid); err != nil {
				return fmt.Errorf("postgres: terminate walsender %d: %w", *pid, err)
			}
		}
		_, err = conn.Exec(ctx, "SELECT pg_drop_replication_slot($1)", s.slot)
		if err == nil {
			break
		}
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55006" { // object_in_use: the walsender is still exiting
			return fmt.Errorf("postgres: drop replication slot %q: %w", s.slot, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if s.ownsPublication {
		if _, err := conn.Exec(ctx, "DROP PUBLICATION IF EXISTS "+pgx.Identifier{s.publication}.Sanitize()); err != nil {
			return fmt.Errorf("postgres: drop publication %q: %w", s.publication, err)
		}
	}
	return nil
}

// FormatPosition renders an LSN as a position.
func FormatPosition(lsn pglogrepl.LSN) model.Position { return model.Position(lsn.String()) }

// ParsePosition parses a position produced by this source.
func ParsePosition(pos model.Position) (pglogrepl.LSN, error) {
	lsn, err := pglogrepl.ParseLSN(string(pos))
	if err != nil {
		return 0, fmt.Errorf("postgres: invalid position %q (want an LSN like 16/B374D848): %w", pos, err)
	}
	return lsn, nil
}

// classify marks errors no retry can fix as permanent.
func classify(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "28P01", "28000": // invalid password, authorization
			return source.Permanent(fmt.Errorf("%w (check the user and password in the dsn, and pg_hba.conf)", err))
		case "3D000": // invalid catalog name
			return source.Permanent(fmt.Errorf("%w (check the database name in the dsn)", err))
		case "42501": // insufficient privilege
			return source.Permanent(err)
		case "42P01": // undefined table
			return source.Permanent(err)
		}
	}
	return err
}

// ackState holds the last acknowledged LSN, shared by Ack and Stream.
type ackState struct {
	mu        sync.Mutex
	lsn       pglogrepl.LSN
	streaming bool
}

// set records lsn and reports whether a stream is running to send it.
func (a *ackState) set(lsn pglogrepl.LSN) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if lsn > a.lsn {
		a.lsn = lsn
	}
	return a.streaming
}

func (a *ackState) get() pglogrepl.LSN {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lsn
}

func (a *ackState) setStreaming(on bool) {
	a.mu.Lock()
	a.streaming = on
	a.mu.Unlock()
}

// newTypeMap returns the codecs used to decode text values.
func newTypeMap() *pgtype.Map { return pgtype.NewMap() }
