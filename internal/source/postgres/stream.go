package postgres

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pglogrepl"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// statusInterval is how often a standby status update is sent. It must stay
// well below the server's wal_sender_timeout (60s by default).
const statusInterval = 10 * time.Second

// Stream decodes pgoutput messages from the slot and emits one batch per
// committed transaction touching the table.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if s.conn == nil {
		return errors.New("postgres: source is not open")
	}
	start, err := s.startLSN(ctx, from)
	if err != nil {
		return err
	}

	rcfg, err := pgconn.ParseConfig(s.cfg.DSN)
	if err != nil {
		return source.Permanent(fmt.Errorf("postgres: parse dsn: %w", err))
	}
	rcfg.RuntimeParams["replication"] = "database"
	conn, err := pgconn.ConnectConfig(ctx, rcfg)
	if err != nil {
		return classify(fmt.Errorf("postgres: replication connect (the user needs the REPLICATION attribute): %w", err))
	}

	err = pglogrepl.StartReplication(ctx, conn, s.slot, start, pglogrepl.StartReplicationOptions{
		Mode: pglogrepl.LogicalReplication,
		PluginArgs: []string{
			"proto_version '1'",
			"publication_names " + quoteLiteral(s.publication),
		},
	})
	if err != nil {
		_ = conn.Close(context.Background())
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "42704" { // undefined_object: the slot is gone
			return fmt.Errorf("postgres: replication slot %q no longer exists: %w", s.slot, source.ErrPositionLost)
		}
		return classify(fmt.Errorf("postgres: start replication on slot %q: %w", s.slot, err))
	}

	s.ack.set(start)
	s.ack.setStreaming(true)
	st := &stream{src: s, conn: conn, emit: emit, types: newTypeMap(), relations: map[uint32]*pglogrepl.RelationMessage{}}
	err = st.run(ctx)
	s.ack.setStreaming(false)

	// Confirm what was acknowledged, then release the slot. The status update
	// goes over the replication connection; if that fails the slot is advanced
	// with SQL once the walsender has exited.
	fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = st.sendStatus(fctx)
	_ = conn.Close(fctx)
	s.finalAdvance(fctx)

	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

// startLSN resolves the position to start from and checks the slot still
// exists, creating it when streaming "from now".
func (s *Source) startLSN(ctx context.Context, from model.Position) (pglogrepl.LSN, error) {
	s.mu.Lock()
	confirmed, found, err := s.slotPosition(ctx)
	s.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if from != "" {
		if !found {
			return 0, fmt.Errorf("postgres: replication slot %q no longer exists: %w", s.slot, source.ErrPositionLost)
		}
		lsn, err := ParsePosition(from)
		if err != nil {
			return 0, source.Permanent(err)
		}
		// The server never goes back before the confirmed position.
		return max(lsn, confirmed), nil
	}
	if found {
		return confirmed, nil
	}
	pos, err := s.CurrentPosition(ctx)
	if err != nil {
		return 0, err
	}
	return ParsePosition(pos)
}

// finalAdvance makes sure the slot reflects the last ack once the stream is
// gone. The walsender exits asynchronously, so it retries briefly.
func (s *Source) finalAdvance(ctx context.Context) {
	lsn := s.ack.get()
	if lsn == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return
	}
	for range 10 {
		var done bool
		err := s.conn.QueryRow(ctx, `
			SELECT NOT active OR confirmed_flush_lsn >= $2::text::pg_lsn FROM pg_replication_slots WHERE slot_name = $1`,
			s.slot, lsn.String()).Scan(&done)
		if err != nil {
			return
		}
		if done {
			s.advanceSlot(ctx, lsn)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// stream is the state of one replication session.
type stream struct {
	src       *Source
	conn      *pgconn.PgConn
	emit      source.EmitFunc
	types     *pgtype.Map
	relations map[uint32]*pglogrepl.RelationMessage

	inTx       bool
	tx         []model.Event
	commitTime time.Time
	// lastEmitted is the commit LSN of the last emitted transaction.
	lastEmitted pglogrepl.LSN
	// idle is a WAL position the server reported while every emitted
	// transaction was acknowledged and none was open: nothing of ours
	// precedes it unconfirmed, so it can be confirmed too. This lets the slot
	// advance while only other tables change.
	idle pglogrepl.LSN

	statusMu sync.Mutex // serialises status updates from the emit keeper
}

func (st *stream) run(ctx context.Context) error {
	next := time.Now().Add(statusInterval)
	for {
		if !time.Now().Before(next) {
			if err := st.sendStatus(ctx); err != nil {
				return err
			}
			next = time.Now().Add(statusInterval)
		}
		rctx, cancel := context.WithDeadline(ctx, next)
		msg, err := st.conn.ReceiveMessage(rctx)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if pgconn.Timeout(err) {
				continue
			}
			return fmt.Errorf("postgres: receive: %w", err)
		}

		switch msg := msg.(type) {
		case *pgproto3.CopyData:
			if len(msg.Data) == 0 {
				continue
			}
			switch msg.Data[0] {
			case pglogrepl.PrimaryKeepaliveMessageByteID:
				pkm, err := pglogrepl.ParsePrimaryKeepaliveMessage(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("postgres: parse keepalive: %w", err)
				}
				if !st.inTx && st.src.ack.get() >= st.lastEmitted && pkm.ServerWALEnd > st.idle {
					st.idle = pkm.ServerWALEnd
				}
				if pkm.ReplyRequested {
					next = time.Time{}
				}
			case pglogrepl.XLogDataByteID:
				xld, err := pglogrepl.ParseXLogData(msg.Data[1:])
				if err != nil {
					return fmt.Errorf("postgres: parse xlog data: %w", err)
				}
				if err := st.handle(ctx, xld.WALData); err != nil {
					return err
				}
			}
		case *pgproto3.ErrorResponse:
			return fmt.Errorf("postgres: replication: %w", pgconn.ErrorResponseToPgError(msg))
		case *pgproto3.CopyDone:
			return errors.New("postgres: server ended the replication stream")
		}
	}
}

// sendStatus reports the acknowledged position as written, flushed and
// applied, which lets the server advance the slot's confirmed_flush_lsn.
func (st *stream) sendStatus(ctx context.Context) error {
	st.statusMu.Lock()
	defer st.statusMu.Unlock()
	lsn := max(st.src.ack.get(), st.idle)
	if lsn == 0 {
		return nil
	}
	err := pglogrepl.SendStandbyStatusUpdate(ctx, st.conn, pglogrepl.StandbyStatusUpdate{
		WALWritePosition: lsn,
		WALFlushPosition: lsn,
		WALApplyPosition: lsn,
	})
	if err != nil {
		return fmt.Errorf("postgres: send standby status: %w", err)
	}
	return nil
}

func (st *stream) handle(ctx context.Context, data []byte) error {
	msg, err := pglogrepl.Parse(data)
	if err != nil {
		return fmt.Errorf("postgres: parse pgoutput message: %w", err)
	}
	switch m := msg.(type) {
	case *pglogrepl.RelationMessage:
		st.relations[m.RelationID] = m
	case *pglogrepl.BeginMessage:
		st.inTx = true
		st.tx = nil
		st.commitTime = m.CommitTime
	case *pglogrepl.InsertMessage:
		rel := st.relation(m.RelationID)
		if rel == nil {
			return nil
		}
		row, err := st.tupleRow(rel, m.Tuple, false)
		if err != nil {
			return err
		}
		st.add(model.OpUpsert, row)
	case *pglogrepl.UpdateMessage:
		rel := st.relation(m.RelationID)
		if rel == nil {
			return nil
		}
		row, err := st.tupleRow(rel, m.NewTuple, false)
		if err != nil {
			return err
		}
		partial := len(row) < len(rel.Columns) // unchanged TOAST values were not sent
		if m.OldTuple != nil {
			old, err := st.tupleRow(rel, m.OldTuple, m.OldTupleType == pglogrepl.UpdateMessageTupleTypeKey)
			if err != nil {
				return err
			}
			if key, changed := st.keyChange(old, row); changed {
				st.add(model.OpDelete, key)
			}
			if partial && m.OldTupleType == pglogrepl.UpdateMessageTupleTypeOld {
				// REPLICA IDENTITY FULL: the old row has the unchanged values.
				for k, v := range old {
					if _, ok := row[k]; !ok {
						row[k] = v
					}
				}
				partial = len(row) < len(rel.Columns)
			}
		}
		st.tx = append(st.tx, model.Event{Op: model.OpUpsert, Row: row, Partial: partial})
	case *pglogrepl.DeleteMessage:
		rel := st.relation(m.RelationID)
		if rel == nil {
			return nil
		}
		row, err := st.tupleRow(rel, m.OldTuple, m.OldTupleType == pglogrepl.DeleteMessageTupleTypeKey)
		if err != nil {
			return err
		}
		st.add(model.OpDelete, row)
	case *pglogrepl.TruncateMessage:
		for _, id := range m.RelationIDs {
			if st.relation(id) != nil {
				st.add(model.OpTruncate, map[string]any{})
				break
			}
		}
	case *pglogrepl.CommitMessage:
		st.inTx = false
		if len(st.tx) == 0 {
			return nil
		}
		pos := FormatPosition(m.TransactionEndLSN)
		for i := range st.tx {
			st.tx[i].Position = pos
			st.tx[i].Time = st.commitTime
		}
		events := st.tx
		st.tx = nil
		if err := st.emitKeepingAlive(ctx, events); err != nil {
			return err
		}
		st.lastEmitted = m.TransactionEndLSN
	}
	return nil
}

// emitKeepingAlive calls emit while a helper keeps sending status updates,
// so a slow consumer does not trip wal_sender_timeout. The main loop does
// not touch the connection meanwhile.
func (st *stream) emitKeepingAlive(ctx context.Context, events []model.Event) error {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Go(func() {
		t := time.NewTicker(statusInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				_ = st.sendStatus(ctx)
			}
		}
	})
	err := st.emit(ctx, events)
	close(done)
	wg.Wait()
	return err
}

func (st *stream) add(op model.Op, row map[string]any) {
	st.tx = append(st.tx, model.Event{Op: op, Row: row})
}

// relation returns the relation when it is the captured table.
func (st *stream) relation(id uint32) *pglogrepl.RelationMessage {
	rel, ok := st.relations[id]
	if !ok || rel.Namespace != st.src.schema || rel.RelationName != st.src.table {
		return nil
	}
	return rel
}

// tupleRow decodes a tuple. Unchanged TOASTed values are omitted. For key
// tuples only the replica identity columns are kept.
func (st *stream) tupleRow(rel *pglogrepl.RelationMessage, tuple *pglogrepl.TupleData, keyOnly bool) (map[string]any, error) {
	row := make(map[string]any, len(tuple.Columns))
	for i, col := range tuple.Columns {
		if i >= len(rel.Columns) {
			break
		}
		rc := rel.Columns[i]
		if keyOnly && rc.Flags&1 == 0 {
			continue
		}
		switch col.DataType {
		case pglogrepl.TupleDataTypeNull:
			row[rc.Name] = nil
		case pglogrepl.TupleDataTypeToast:
			// unchanged TOAST value: not sent, leave the column out
		case pglogrepl.TupleDataTypeText:
			v, err := decodeText(st.types, rc.DataType, col.Data)
			if err != nil {
				return nil, fmt.Errorf("postgres: column %s: %w", rc.Name, err)
			}
			row[rc.Name] = v
		case pglogrepl.TupleDataTypeBinary:
			row[rc.Name] = col.Data
		}
	}
	return row, nil
}

// keyChange reports whether an update moved the row to another primary key,
// and returns the old key.
func (st *stream) keyChange(old, row map[string]any) (map[string]any, bool) {
	pk := st.src.pk
	if len(pk) == 0 {
		return nil, false
	}
	key := make(map[string]any, len(pk))
	changed := false
	for _, k := range pk {
		ov, ok := old[k]
		if !ok {
			return nil, false
		}
		key[k] = ov
		if nv, ok := row[k]; ok && !reflect.DeepEqual(ov, nv) {
			changed = true
		}
	}
	return key, changed
}

// quoteLiteral quotes s as an SQL string literal.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
