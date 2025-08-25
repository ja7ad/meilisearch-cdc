// Package kafka consumes Debezium change events from a Kafka topic, which
// brings every database Debezium supports. Messages must use the JSON
// converter, with or without schemas (key.converter and value.converter
// org.apache.kafka.connect.json.JsonConverter).
//
// Snapshot replays the topic from its start up to the current end offsets,
// folding changes per message key in memory, so it needs memory proportional
// to the live rows. For very large topics use the "stream" sync mode, which
// replays the topic from the earliest offset (or the committed group offsets)
// as ordinary change events.
package kafka

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kerr"
	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/kmsg"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	peekTimeout  = 5 * time.Second
	peekMessages = 50
	snapshotIdle = 10 * time.Second
	chunkSize    = 1000
)

func init() {
	source.Register(model.SourceKafka, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg, taskID), nil
	})
}

// Source consumes one topic.
//
// Positions are JSON objects mapping "topic/partition" to the next offset
// to read; see offsets.
type Source struct {
	cfg   model.Source
	group string
	admin *kgo.Client
	adm   *kadm.Client

	mu     sync.Mutex
	active *kgo.Client // the group consumer while Stream runs
}

// New returns an unopened source.
func New(cfg model.Source, taskID string) *Source {
	group := cfg.GroupID
	if group == "" {
		group = "cdc-" + taskID
	}
	return &Source{cfg: cfg, group: group}
}

// Open connects and checks the topic exists.
func (s *Source) Open(ctx context.Context) error {
	if len(s.cfg.Brokers) == 0 {
		return source.Permanent(errors.New("kafka: source.brokers is required"))
	}
	if s.cfg.Table == "" {
		return source.Permanent(errors.New("kafka: source.table (the Debezium topic, e.g. dbserver1.inventory.customers) is required"))
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(s.cfg.Brokers...))
	if err != nil {
		return source.Permanent(fmt.Errorf("kafka: %w", err))
	}
	s.admin, s.adm = cl, kadm.NewClient(cl)
	topics, err := s.adm.ListTopics(ctx, s.cfg.Table)
	if err != nil {
		return fmt.Errorf("kafka: list topics: %w", err)
	}
	d, ok := topics[s.cfg.Table]
	if !ok || errors.Is(d.Err, kerr.UnknownTopicOrPartition) {
		return source.Permanent(fmt.Errorf("kafka: topic %q does not exist; check the Debezium connector topic.prefix and table include list", s.cfg.Table))
	}
	if d.Err != nil {
		return fmt.Errorf("kafka: topic %q: %w", s.cfg.Table, d.Err)
	}
	return nil
}

// Schema peeks at up to 50 messages (for at most 5s) from the start of the
// topic. Columns come from before/after images, the primary key from the
// message key fields (default ["id"]).
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(s.cfg.Brokers...),
		kgo.ConsumeTopics(s.cfg.Table),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		return nil, fmt.Errorf("kafka: %w", err)
	}
	defer cl.Close()
	pctx, cancel := context.WithTimeout(ctx, peekTimeout)
	defer cancel()

	sc := &model.Schema{Inferred: true, RowCount: -1}
	cols := map[string]int{}
	addCol := func(name string, v any) {
		if i, ok := cols[name]; ok {
			if v == nil {
				sc.Columns[i].Nullable = true
			} else if sc.Columns[i].Type == "null" {
				sc.Columns[i].Type = jsonType(v)
			}
			return
		}
		cols[name] = len(sc.Columns)
		sc.Columns = append(sc.Columns, model.Column{Name: name, Type: jsonType(v), Nullable: v == nil})
	}
	var seen int
	for seen < peekMessages && pctx.Err() == nil {
		fetches := cl.PollFetches(pctx)
		fetches.EachRecord(func(r *kgo.Record) {
			seen++
			if sc.PrimaryKey == nil {
				if key := parseKey(r.Key); len(key) > 0 {
					sc.PrimaryKey = sortedKeys(key)
				}
			}
			if len(r.Value) == 0 {
				return
			}
			c, err := parseChange(r.Value)
			if err != nil {
				return
			}
			for _, row := range []map[string]any{c.After, c.Before} {
				for _, k := range sortedKeys(row) {
					addCol(k, row[k])
				}
			}
		})
	}
	if sc.PrimaryKey == nil {
		sc.PrimaryKey = []string{"id"}
	}
	for _, k := range sc.PrimaryKey {
		if _, ok := cols[k]; !ok {
			addCol(k, nil)
		}
		c := &sc.Columns[cols[k]]
		c.PrimaryKey, c.Nullable = true, false
	}
	return sc, nil
}

// CurrentPosition returns the end offsets of every partition.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	ends, err := s.endOffsets(ctx)
	if err != nil {
		return "", err
	}
	return ends.encode(), nil
}

func (s *Source) endOffsets(ctx context.Context) (offsets, error) {
	listed, err := s.adm.ListEndOffsets(ctx, s.cfg.Table)
	if err != nil {
		return nil, fmt.Errorf("kafka: list end offsets: %w", err)
	}
	if err := listed.Error(); err != nil {
		return nil, fmt.Errorf("kafka: list end offsets: %w", err)
	}
	out := offsets{}
	listed.Each(func(o kadm.ListedOffset) { out[partitionKey(o.Topic, o.Partition)] = o.Offset })
	return out, nil
}

// Snapshot replays the topic up to its current end offsets and emits the
// latest image of every live row.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	ends, err := s.endOffsets(ctx)
	if err != nil {
		return err
	}
	starts, err := s.adm.ListStartOffsets(ctx, s.cfg.Table)
	if err != nil {
		return fmt.Errorf("kafka: list start offsets: %w", err)
	}
	pending := map[int32]int64{} // partition -> end offset still to reach
	consume := map[string]map[int32]kgo.Offset{s.cfg.Table: {}}
	for k, end := range ends {
		_, p, _ := splitKey(k)
		start, ok := starts.Lookup(s.cfg.Table, p)
		if ok && start.Offset >= end {
			continue
		}
		pending[p] = end
		consume[s.cfg.Table][p] = kgo.NewOffset().AtStart()
	}
	if len(pending) == 0 {
		return nil
	}
	cl, err := kgo.NewClient(kgo.SeedBrokers(s.cfg.Brokers...), kgo.ConsumePartitions(consume))
	if err != nil {
		return fmt.Errorf("kafka: %w", err)
	}
	defer cl.Close()

	f := newFolder()
	lastProgress := time.Now()
	for len(pending) > 0 {
		pctx, cancel := context.WithTimeout(ctx, time.Second)
		fetches := cl.PollFetches(pctx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var ferr error
		fetches.EachError(func(_ string, _ int32, err error) {
			if !errors.Is(err, context.DeadlineExceeded) && ferr == nil {
				ferr = err
			}
		})
		if ferr != nil {
			return fmt.Errorf("kafka: snapshot fetch: %w", ferr)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			end, ok := pending[r.Partition]
			if !ok || r.Offset >= end {
				return
			}
			lastProgress = time.Now()
			if ferr == nil {
				ferr = f.add(r)
			}
			if r.Offset+1 >= end {
				delete(pending, r.Partition)
			}
		})
		if ferr != nil {
			return ferr
		}
		// Transaction markers or compaction can leave the last offsets
		// without records; stop once nothing below the end arrives.
		if time.Since(lastProgress) > snapshotIdle {
			break
		}
	}
	rows := f.rows()
	for len(rows) > 0 {
		n := min(chunkSize, len(rows))
		evs := make([]model.Event, n)
		for i, row := range rows[:n] {
			evs[i] = model.Event{Op: model.OpUpsert, Row: row, Snapshot: true}
		}
		if err := emit(ctx, evs); err != nil {
			return err
		}
		rows = rows[n:]
	}
	return nil
}

// folder keeps the latest image per message key.
type folder struct {
	order []string
	live  map[string]map[string]any
	nokey []map[string]any
}

func newFolder() *folder { return &folder{live: map[string]map[string]any{}} }

func (f *folder) add(r *kgo.Record) error {
	if len(r.Value) == 0 {
		return nil
	}
	c, err := parseChange(r.Value)
	if errors.Is(err, errSkip) {
		return nil
	}
	if err != nil {
		return err
	}
	key := string(r.Key)
	switch c.Op {
	case model.OpTruncate:
		f.order, f.live, f.nokey = nil, map[string]map[string]any{}, nil
	case model.OpDelete:
		delete(f.live, key)
	case model.OpUpsert:
		if r.Key == nil {
			f.nokey = append(f.nokey, c.After)
			return nil
		}
		if _, ok := f.live[key]; !ok {
			f.order = append(f.order, key)
		}
		f.live[key] = c.After
	}
	return nil
}

func (f *folder) rows() []map[string]any {
	out := slices.Clone(f.nokey)
	for _, k := range f.order {
		if row, ok := f.live[k]; ok {
			out = append(out, row)
			delete(f.live, k) // a key deleted then re-added appears twice in order
		}
	}
	return out
}

// Stream consumes the topic as consumer group s.group. An empty position
// resumes from the group's committed offsets, falling back to the earliest
// offset; otherwise consumption starts at the position's offsets.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	next, err := decodeOffsets(from)
	if err != nil {
		return fmt.Errorf("%w: %v", source.ErrPositionLost, err)
	}
	var nextMu sync.Mutex
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(s.cfg.Brokers...),
		kgo.ConsumerGroup(s.group),
		kgo.ConsumeTopics(s.cfg.Table),
		kgo.DisableAutoCommit(),
		kgo.ConsumeStartOffset(kgo.NewOffset().AtStart()),
		// Fail instead of silently skipping when offsets fell off the log.
		kgo.ConsumeResetOffset(kgo.NoResetOffset()),
		// Start where we left off: the stream position wins over the
		// committed offsets, which only advance on Ack.
		kgo.AdjustFetchOffsetsFn(func(_ context.Context, offs map[string]map[int32]kgo.Offset) (map[string]map[int32]kgo.Offset, error) {
			nextMu.Lock()
			defer nextMu.Unlock()
			for topic, parts := range offs {
				for p := range parts {
					if n, ok := next[partitionKey(topic, p)]; ok {
						parts[p] = kgo.NewOffset().At(n)
					}
				}
			}
			return offs, nil
		}),
	)
	if err != nil {
		return source.Permanent(fmt.Errorf("kafka: %w", err))
	}
	s.mu.Lock()
	s.active = cl
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active = nil
		s.mu.Unlock()
		cl.Close()
	}()

	for {
		fetches := cl.PollFetches(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if fetches.IsClientClosed() {
			return errors.New("kafka: client closed")
		}
		var ferr error
		fetches.EachError(func(topic string, p int32, err error) {
			if ferr != nil {
				return
			}
			if errors.Is(err, kerr.OffsetOutOfRange) {
				ferr = fmt.Errorf("%w: kafka: %s/%d: %v", source.ErrPositionLost, topic, p, err)
				return
			}
			ferr = fmt.Errorf("kafka: fetch %s/%d: %w", topic, p, err)
		})
		if ferr != nil {
			return ferr
		}
		var evs []model.Event
		fetches.EachRecord(func(r *kgo.Record) {
			if ferr != nil {
				return
			}
			nextMu.Lock()
			next[partitionKey(r.Topic, r.Partition)] = r.Offset + 1
			pos := next.encode()
			nextMu.Unlock()
			ev, err := toEvent(r)
			if err != nil {
				ferr = err
				return
			}
			if ev != nil {
				ev.Position = pos
				evs = append(evs, *ev)
			}
		})
		if ferr != nil {
			return ferr
		}
		if len(evs) > 0 {
			if err := emit(ctx, evs); err != nil {
				return err
			}
		}
	}
}

// toEvent converts a record; nil means the record carries no row change.
func toEvent(r *kgo.Record) (*model.Event, error) {
	if len(r.Value) == 0 {
		return nil, nil // tombstone following a delete
	}
	c, err := parseChange(r.Value)
	if errors.Is(err, errSkip) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%w (%s/%d@%d)", err, r.Topic, r.Partition, r.Offset)
	}
	ev := &model.Event{Op: c.Op, Time: c.Time}
	if ev.Time.IsZero() {
		ev.Time = r.Timestamp.UTC()
	}
	switch c.Op {
	case model.OpUpsert:
		ev.Row = c.After
	case model.OpDelete:
		ev.Row = c.Before
		if ev.Row == nil {
			ev.Row = parseKey(r.Key)
		}
		if ev.Row == nil {
			return nil, fmt.Errorf("kafka: delete without before image or key (%s/%d@%d)", r.Topic, r.Partition, r.Offset)
		}
	}
	return ev, nil
}

// Ack commits the position's offsets for the consumer group.
func (s *Source) Ack(ctx context.Context, pos model.Position) error {
	o, err := decodeOffsets(pos)
	if err != nil || len(o) == 0 {
		return err
	}
	s.mu.Lock()
	active := s.active
	s.mu.Unlock()
	if active != nil {
		commit := map[string]map[int32]kgo.EpochOffset{}
		for topic, parts := range o.byTopic() {
			commit[topic] = map[int32]kgo.EpochOffset{}
			for p, off := range parts {
				commit[topic][p] = kgo.EpochOffset{Epoch: -1, Offset: off}
			}
		}
		var cerr error
		active.CommitOffsetsSync(ctx, commit, func(_ *kgo.Client, _ *kmsg.OffsetCommitRequest, resp *kmsg.OffsetCommitResponse, err error) {
			if err != nil {
				cerr = err
				return
			}
			for _, t := range resp.Topics {
				for _, p := range t.Partitions {
					if e := kerr.ErrorForCode(p.ErrorCode); e != nil && cerr == nil {
						cerr = e
					}
				}
			}
		})
		if cerr == nil {
			return nil
		}
		// The group may be rebalancing; fall through to a direct commit.
	}
	var ko kadm.Offsets
	for topic, parts := range o.byTopic() {
		for p, off := range parts {
			ko.AddOffset(topic, p, off, -1)
		}
	}
	resp, err := s.adm.CommitOffsets(ctx, s.group, ko)
	if err != nil {
		return fmt.Errorf("kafka: commit offsets: %w", err)
	}
	if err := resp.Error(); err != nil {
		return fmt.Errorf("kafka: commit offsets: %w", err)
	}
	return nil
}

// Close closes the admin client.
func (s *Source) Close() error {
	if s.admin != nil {
		s.admin.Close()
	}
	return nil
}

func jsonType(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case int64, uint64:
		return "integer"
	case float64:
		return "number"
	case string:
		return "string"
	case time.Time:
		return "timestamp"
	case []byte:
		return "bytes"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "unknown"
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
