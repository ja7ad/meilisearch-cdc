package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	recordLimit = 1000
	// replaySlack widens the since filter to absorb clock skew and the
	// coarse ApproximateCreationDateTime (rounded down to the second by
	// DynamoDB, to the minute by DynamoDB Local).
	replaySlack = time.Minute
)

type shard struct {
	id, parent string
}

// reader follows one shard.
type reader struct {
	id   string
	iter *string
	// filter skips records older than since (shards read from TRIM_HORIZON).
	filter bool
}

// streamer holds the state of one Stream call.
type streamer struct {
	s       *Source
	emit    source.EmitFunc
	pos     position
	readers map[string]*reader
	order   []string
}

// Stream reads every shard of the table stream, parents before children,
// and emits changes after from.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	st := &streamer{s: s, emit: emit, readers: map[string]*reader{}}
	if from == "" {
		st.pos = position{Shards: map[string]string{}, Since: time.Now().UTC()}
	} else {
		p, err := decodePosition(from)
		if err != nil {
			return fmt.Errorf("%w: %v", source.ErrPositionLost, err)
		}
		st.pos = p
	}
	err := st.run(ctx)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return err
}

func (st *streamer) run(ctx context.Context) error {
	var lastDiscover time.Time
	discover := true
	for {
		if discover || time.Since(lastDiscover) >= st.s.discoverInterval {
			if err := st.discover(ctx); err != nil {
				return err
			}
			lastDiscover, discover = time.Now(), false
		}
		busy := false
		for _, id := range append([]string(nil), st.order...) {
			n, closed, err := st.read(ctx, st.readers[id])
			if err != nil {
				return err
			}
			busy = busy || n > 0
			if closed {
				discover = true
			}
		}
		if busy || discover {
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(st.s.pollInterval):
		}
	}
}

func (st *streamer) listShards(ctx context.Context) ([]shard, error) {
	var out []shard
	var start *string
	for {
		res, err := st.s.streams.DescribeStream(ctx, &dynamodbstreams.DescribeStreamInput{
			StreamArn:             aws.String(st.s.streamARN),
			ExclusiveStartShardId: start,
		})
		if err != nil {
			var nf *types.ResourceNotFoundException
			if errors.As(err, &nf) {
				return nil, source.Permanent(fmt.Errorf("dynamodb: stream %s no longer exists (stream disabled or table recreated): %w", st.s.streamARN, err))
			}
			return nil, fmt.Errorf("dynamodb: describe stream: %w", err)
		}
		for _, sh := range res.StreamDescription.Shards {
			out = append(out, shard{id: aws.ToString(sh.ShardId), parent: aws.ToString(sh.ParentShardId)})
		}
		start = res.StreamDescription.LastEvaluatedShardId
		if start == nil {
			return out, nil
		}
	}
}

// discover lists shards and starts readers for shards whose parent is done.
func (st *streamer) discover(ctx context.Context) error {
	shards, err := st.listShards(ctx)
	if err != nil {
		return err
	}
	listed := make(map[string]bool, len(shards))
	for _, sh := range shards {
		listed[sh.id] = true
	}
	for id := range st.pos.Shards {
		if !listed[id] {
			return fmt.Errorf("%w: dynamodb: shard %s expired before it was fully read", source.ErrPositionLost, id)
		}
	}
	// Forget finished shards that left the stream (24h retention).
	done := st.pos.Done[:0]
	for _, id := range st.pos.Done {
		if listed[id] {
			done = append(done, id)
		}
	}
	st.pos.Done = done

	for _, sh := range shards {
		if st.readers[sh.id] != nil || st.pos.isDone(sh.id) {
			continue
		}
		if sh.parent != "" && listed[sh.parent] && !st.pos.isDone(sh.parent) {
			continue // parent first
		}
		r := &reader{id: sh.id}
		if err := st.openIterator(ctx, r); err != nil {
			return err
		}
		st.readers[sh.id] = r
		st.order = append(st.order, sh.id)
	}
	return nil
}

// openIterator positions r after its last delivered record, or at
// TRIM_HORIZON for shards not read yet.
func (st *streamer) openIterator(ctx context.Context, r *reader) error {
	in := &dynamodbstreams.GetShardIteratorInput{StreamArn: aws.String(st.s.streamARN), ShardId: aws.String(r.id)}
	if seq := st.pos.Shards[r.id]; seq != "" {
		in.ShardIteratorType = types.ShardIteratorTypeAfterSequenceNumber
		in.SequenceNumber = aws.String(seq)
		r.filter = false
	} else {
		in.ShardIteratorType = types.ShardIteratorTypeTrimHorizon
		r.filter = true
	}
	out, err := st.s.streams.GetShardIterator(ctx, in)
	if err != nil {
		var trimmed *types.TrimmedDataAccessException
		var nf *types.ResourceNotFoundException
		if errors.As(err, &trimmed) || errors.As(err, &nf) {
			return fmt.Errorf("%w: dynamodb: shard %s: %v", source.ErrPositionLost, r.id, err)
		}
		return fmt.Errorf("dynamodb: get shard iterator: %w", err)
	}
	r.iter = out.ShardIterator
	return nil
}

// read fetches one batch from r and emits it. closed reports the shard was
// read to its end.
func (st *streamer) read(ctx context.Context, r *reader) (n int, closed bool, err error) {
	out, err := st.s.streams.GetRecords(ctx, &dynamodbstreams.GetRecordsInput{
		ShardIterator: r.iter,
		Limit:         aws.Int32(recordLimit),
	})
	if err != nil {
		var expired *types.ExpiredIteratorException
		var limited *types.LimitExceededException
		var trimmed *types.TrimmedDataAccessException
		switch {
		case errors.As(err, &expired):
			return 0, false, st.openIterator(ctx, r)
		case errors.As(err, &trimmed):
			if st.pos.Shards[r.id] != "" {
				return 0, false, fmt.Errorf("%w: dynamodb: shard %s: %v", source.ErrPositionLost, r.id, err)
			}
			return 0, false, st.openIterator(ctx, r)
		case errors.As(err, &limited):
			return 0, false, nil // throttled: try again next round
		}
		return 0, false, fmt.Errorf("dynamodb: get records: %w", err)
	}

	since := st.pos.Since.Add(-replaySlack)
	var evs []model.Event
	for _, rec := range out.Records {
		sr := rec.Dynamodb
		if sr == nil {
			continue
		}
		st.pos.Shards[r.id] = aws.ToString(sr.SequenceNumber)
		var ts time.Time
		if sr.ApproximateCreationDateTime != nil {
			ts = sr.ApproximateCreationDateTime.UTC()
		}
		if r.filter && !ts.IsZero() && ts.Before(since) {
			continue
		}
		ev := model.Event{Time: ts}
		switch rec.EventName {
		case types.OperationTypeInsert, types.OperationTypeModify:
			if sr.NewImage == nil {
				continue
			}
			ev.Op = model.OpUpsert
			ev.Row, err = streamImageToRow(sr.NewImage)
		case types.OperationTypeRemove:
			ev.Op = model.OpDelete
			ev.Row, err = streamImageToRow(sr.Keys)
		default:
			continue
		}
		if err != nil {
			return 0, false, err
		}
		// Each event carries the whole checkpoint, so acking any of them is
		// safe and positions only move forward.
		ev.Position = st.pos.encode()
		evs = append(evs, ev)
	}
	if len(evs) > 0 {
		if err := st.emit(ctx, evs); err != nil {
			return 0, false, err
		}
	}
	r.iter = out.NextShardIterator
	if r.iter == nil {
		// Closed shard read to the end: children may start.
		delete(st.pos.Shards, r.id)
		st.pos.Done = append(st.pos.Done, r.id)
		delete(st.readers, r.id)
		for i, id := range st.order {
			if id == r.id {
				st.order = append(st.order[:i], st.order[i+1:]...)
				break
			}
		}
		return len(out.Records), true, nil
	}
	return len(out.Records), false, nil
}
