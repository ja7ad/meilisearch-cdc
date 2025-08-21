// Package redisstream consumes a Redis stream of change entries.
//
// Producers append one entry per change with XADD. Entry fields:
//
//	op    upsert (default), delete or truncate
//	id    the document key; copied into the row as "id" unless the row has one
//	data  optional JSON object holding the whole row
//
// When data is absent every other field except op becomes a row column (as
// a string, since Redis field values are strings). For example:
//
//	XADD items * op upsert id 42 data '{"name":"shoe","price":9.5}'
//	XADD items * id 42 name shoe price 9.5
//	XADD items * op delete id 42
//
// Positions are stream entry ids ("1700000000000-0"); "0-0" is the start of
// the stream. The source reads with XREAD (no consumer group, so cfg.GroupID
// is ignored and Ack is a no-op; the stream is never trimmed by this source).
//
// A stream is a log, not a table: Snapshot replays the whole stream from the
// beginning and keeps the latest image per key (like a compacted log),
// emitting the rows still alive at the end. That image is held in memory.
package redisstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	keyField   = "id"
	readCount  = 1000
	sampleSize = 100
	// blockFor bounds each XREAD, so Stream notices cancellation promptly.
	blockFor = 500 * time.Millisecond
)

func init() {
	source.Register(model.SourceRedis, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

// Source reads one stream. It implements source.Source.
type Source struct {
	cfg    model.Source
	key    string
	client *redis.Client
}

// New returns an unopened source.
func New(cfg model.Source) *Source { return &Source{cfg: cfg, key: cfg.Table} }

// Open connects and checks the key is a stream (or does not exist yet).
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("redis: source.dsn is required (redis://user:password@host:6379/0)"))
	}
	if s.key == "" {
		return source.Permanent(errors.New("redis: source.table (the stream key) is required"))
	}
	opts, err := redis.ParseURL(s.cfg.DSN)
	if err != nil {
		return source.Permanent(fmt.Errorf("redis: invalid dsn: %w", err))
	}
	s.client = redis.NewClient(opts)
	if err := s.client.Ping(ctx).Err(); err != nil {
		return classify(fmt.Errorf("redis: ping: %w", err))
	}
	typ, err := s.client.Type(ctx, s.key).Result()
	if err != nil {
		return classify(fmt.Errorf("redis: type %s: %w", s.key, err))
	}
	if typ != "stream" && typ != "none" {
		return source.Permanent(fmt.Errorf("redis: key %q is a %s, not a stream", s.key, typ))
	}
	return nil
}

// Schema infers the columns from the last 100 entries. RowCount is unknown.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	msgs, err := s.client.XRevRangeN(ctx, s.key, "+", "-", sampleSize).Result()
	if err != nil {
		return nil, classify(fmt.Errorf("redis: sample %s: %w", s.key, err))
	}
	var rows []map[string]any
	for _, m := range msgs {
		ch, err := parseEntry(m)
		if err == nil && ch.row != nil {
			rows = append(rows, ch.row)
		}
	}
	return &model.Schema{Columns: inferColumns(rows), PrimaryKey: []string{keyField}, Inferred: true, RowCount: -1}, nil
}

// CurrentPosition returns the id of the last entry, "0-0" for an empty or
// missing stream.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	msgs, err := s.client.XRevRangeN(ctx, s.key, "+", "-", 1).Result()
	if err != nil {
		return "", classify(fmt.Errorf("redis: read last entry of %s: %w", s.key, err))
	}
	if len(msgs) == 0 {
		return "0-0", nil
	}
	return model.Position(msgs[0].ID), nil
}

// Snapshot replays the stream and emits the latest image of every live key.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	live := map[string]map[string]any{}
	start := "-"
	for {
		msgs, err := s.client.XRangeN(ctx, s.key, start, "+", readCount).Result()
		if err != nil {
			return classify(fmt.Errorf("redis: snapshot %s: %w", s.key, err))
		}
		for _, m := range msgs {
			ch, err := parseEntry(m)
			if err != nil {
				return err
			}
			switch ch.op {
			case model.OpTruncate:
				clear(live)
			case model.OpDelete:
				delete(live, ch.key)
			default:
				live[ch.key] = ch.row
			}
		}
		if len(msgs) < readCount {
			break
		}
		start = "(" + msgs[len(msgs)-1].ID
	}
	keys := make([]string, 0, len(live))
	for k := range live {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	batch := make([]model.Event, 0, readCount)
	for _, k := range keys {
		batch = append(batch, model.Event{Op: model.OpUpsert, Row: live[k], Snapshot: true})
		if len(batch) == readCount {
			if err := emit(ctx, batch); err != nil {
				return err
			}
			batch = make([]model.Event, 0, readCount)
		}
	}
	if len(batch) > 0 {
		return emit(ctx, batch)
	}
	return nil
}

// Stream emits entries after from. The empty position starts at the
// current end of the stream.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	if from == "" {
		p, err := s.CurrentPosition(ctx)
		if err != nil {
			return err
		}
		from = p
	}
	last, err := parseID(string(from))
	if err != nil {
		return fmt.Errorf("redis: invalid position %q: %w", from, source.ErrPositionLost)
	}
	if err := s.checkPosition(ctx, last); err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		res, err := s.client.XRead(ctx, &redis.XReadArgs{
			Streams: []string{s.key, last.String()},
			Count:   readCount,
			Block:   blockFor,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue // timed out without entries
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return classify(fmt.Errorf("redis: xread %s: %w", s.key, err))
		}
		var batch []model.Event
		for _, st := range res {
			for _, m := range st.Messages {
				ch, err := parseEntry(m)
				if err != nil {
					return err
				}
				id, _ := parseID(m.ID)
				ev := model.Event{Op: ch.op, Row: ch.row, Position: model.Position(m.ID), Time: time.UnixMilli(int64(id.ms)).UTC()}
				batch = append(batch, ev)
				last = id
			}
		}
		if len(batch) > 0 {
			if err := emit(ctx, batch); err != nil {
				return err
			}
		}
	}
}

// checkPosition reports ErrPositionLost when entries after pos were trimmed
// or deleted, or the stream was recreated.
func (s *Source) checkPosition(ctx context.Context, pos streamID) error {
	info, err := s.client.XInfoStream(ctx, s.key).Result()
	if err != nil {
		if strings.Contains(err.Error(), "no such key") {
			if pos.isZero() {
				return nil
			}
			return fmt.Errorf("redis: stream %q no longer exists but the position is %s: %w", s.key, pos, source.ErrPositionLost)
		}
		return classify(fmt.Errorf("redis: xinfo stream %s: %w", s.key, err))
	}
	return positionLost(pos, info)
}

func positionLost(pos streamID, info *redis.XInfoStream) error {
	if lastGen, err := parseID(info.LastGeneratedID); err == nil && lastGen.less(pos) {
		return fmt.Errorf("redis: position %s is after the last generated id %s (the stream was recreated): %w", pos, lastGen, source.ErrPositionLost)
	}
	// Redis >= 7 tracks the highest id removed with XDEL.
	if maxDel, err := parseID(info.MaxDeletedEntryID); err == nil && pos.less(maxDel) {
		return fmt.Errorf("redis: entries after position %s were deleted (up to %s): %w", pos, maxDel, source.ErrPositionLost)
	}
	// EntriesAdded (Redis >= 7) minus Length counts removed entries; zero
	// means nothing was ever trimmed, so any position is still complete.
	if info.EntriesAdded > 0 && info.EntriesAdded <= info.Length {
		return nil
	}
	if info.Length == 0 {
		if lastGen, err := parseID(info.LastGeneratedID); err == nil && pos.less(lastGen) {
			return fmt.Errorf("redis: entries after position %s were trimmed (the stream is empty, last id %s): %w", pos, lastGen, source.ErrPositionLost)
		}
		return nil
	}
	first, err := parseID(info.FirstEntry.ID)
	if err != nil || !pos.less(first) {
		return nil
	}
	if pos.isZero() {
		if info.EntriesAdded > info.Length {
			return fmt.Errorf("redis: the stream was trimmed (first entry %s) so it cannot be replayed from the start: %w", first, source.ErrPositionLost)
		}
		return nil // older server: cannot tell
	}
	// The position was trimmed itself. The entries between it and the first
	// remaining entry may have been trimmed too; only a directly adjacent id
	// proves nothing is missing.
	if pos.ms == first.ms && pos.seq+1 == first.seq {
		return nil
	}
	return fmt.Errorf("redis: position %s is older than the first entry %s (the stream was trimmed): %w", pos, first, source.ErrPositionLost)
}

// Ack is a no-op: XREAD keeps no server side state.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close disconnects.
func (s *Source) Close() error {
	if s.client == nil {
		return nil
	}
	err := s.client.Close()
	s.client = nil
	return err
}

func classify(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "WRONGPASS"), strings.Contains(msg, "NOAUTH"), strings.Contains(msg, "NOPERM"),
		strings.Contains(msg, "invalid password"), strings.Contains(msg, "invalid username-password"):
		return source.Permanent(fmt.Errorf("%w (check the user and password in the dsn and the ACL of the user)", err))
	case strings.Contains(msg, "WRONGTYPE"):
		return source.Permanent(err)
	}
	return err
}

// change is a decoded stream entry.
type change struct {
	op  model.Op
	key string
	row map[string]any
}

// parseEntry decodes one entry following the package conventions.
func parseEntry(m redis.XMessage) (change, error) {
	fields := make(map[string]string, len(m.Values))
	for k, v := range m.Values {
		fields[k] = fmt.Sprint(v)
	}
	var ch change
	switch strings.ToLower(fields["op"]) {
	case "", "upsert", "insert", "update", "replace":
		ch.op = model.OpUpsert
	case "delete":
		ch.op = model.OpDelete
	case "truncate":
		ch.op = model.OpTruncate
		return ch, nil
	default:
		return ch, source.Permanent(fmt.Errorf("redis: entry %s has unknown op %q (use upsert, delete or truncate)", m.ID, fields["op"]))
	}

	row := map[string]any{}
	if data, ok := fields["data"]; ok {
		dec := json.NewDecoder(bytes.NewReader([]byte(data)))
		dec.UseNumber()
		var obj map[string]any
		if err := dec.Decode(&obj); err != nil || obj == nil {
			return ch, source.Permanent(fmt.Errorf("redis: entry %s: field data must be a JSON object: %v", m.ID, err))
		}
		for k, v := range obj {
			row[k] = normalizeJSON(v)
		}
	} else {
		for k, v := range fields {
			if k != "op" {
				row[k] = v
			}
		}
	}
	if id, ok := fields["id"]; ok {
		if _, has := row[keyField]; !has {
			row[keyField] = id
		}
	}
	idv, ok := row[keyField]
	if !ok || idv == nil {
		return ch, source.Permanent(fmt.Errorf("redis: entry %s has no id field (every upsert and delete needs one)", m.ID))
	}
	ch.key = fmt.Sprint(idv)
	if ch.op == model.OpDelete {
		ch.row = map[string]any{keyField: idv}
	} else {
		ch.row = row
	}
	return ch, nil
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
			return source.IntegralFloat(f)
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

// inferColumns builds sorted columns from sampled rows; "id" is always present.
func inferColumns(rows []map[string]any) []model.Column {
	types := map[string]string{}
	nullable := map[string]bool{}
	for _, r := range rows {
		for k, v := range r {
			t := typeName(v)
			if v == nil {
				nullable[k] = true
			}
			switch old, ok := types[k]; {
			case !ok, old == "null":
				types[k] = t
			case old != t && t != "null":
				types[k] = "mixed"
			}
		}
	}
	for k := range types {
		for _, r := range rows {
			if _, ok := r[k]; !ok {
				nullable[k] = true
			}
		}
	}
	if _, ok := types[keyField]; !ok {
		types[keyField] = "string"
	}
	names := make([]string, 0, len(types))
	for k := range types {
		names = append(names, k)
	}
	sort.Strings(names)
	cols := make([]model.Column, 0, len(names))
	for _, n := range names {
		cols = append(cols, model.Column{Name: n, Type: types[n], Nullable: nullable[n], PrimaryKey: n == keyField})
	}
	return cols
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int64, uint64:
		return "int"
	case float64:
		return "float"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// streamID is a parsed entry id.
type streamID struct{ ms, seq uint64 }

func parseID(s string) (streamID, error) {
	if s == "" {
		return streamID{}, errors.New("empty id")
	}
	msPart, seqPart, hasSeq := strings.Cut(s, "-")
	ms, err := strconv.ParseUint(msPart, 10, 64)
	if err != nil {
		return streamID{}, fmt.Errorf("invalid stream id %q", s)
	}
	var seq uint64
	if hasSeq {
		if seq, err = strconv.ParseUint(seqPart, 10, 64); err != nil {
			return streamID{}, fmt.Errorf("invalid stream id %q", s)
		}
	}
	return streamID{ms, seq}, nil
}

func (id streamID) String() string {
	return strconv.FormatUint(id.ms, 10) + "-" + strconv.FormatUint(id.seq, 10)
}
func (id streamID) isZero() bool { return id.ms == 0 && id.seq == 0 }
func (id streamID) less(o streamID) bool {
	return id.ms < o.ms || (id.ms == o.ms && id.seq < o.seq)
}
