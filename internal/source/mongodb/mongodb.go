// Package mongodb captures changes from a MongoDB collection with change
// streams. The deployment must be a replica set or a sharded cluster.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/connstring"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	batchSize  = 1000
	sampleSize = 100
	keyField   = "_id"
)

// Server error codes.
const (
	codeInvalidResumeToken       = 260
	codeChangeStreamFatal        = 280
	codeChangeStreamHistoryLost  = 286
	codeChangeStreamNotSupported = 40573
)

func init() {
	source.Register(model.SourceMongoDB, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

// Source streams one collection.
//
// Positions are change stream resume tokens serialised as canonical
// extended JSON, for example {"_data":"8263F1..."}. Stream resumes after
// the token; the empty position starts at the current time.
type Source struct {
	cfg    model.Source
	client *mongo.Client
	coll   *mongo.Collection
}

// New returns an unopened source.
func New(cfg model.Source) *Source { return &Source{cfg: cfg} }

// Open connects and checks the deployment supports change streams.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("mongodb: source.dsn is required (mongodb://host:27017/?replicaSet=rs0)"))
	}
	if s.cfg.Table == "" {
		return source.Permanent(errors.New("mongodb: source.table (the collection) is required"))
	}
	cs, err := connstring.ParseAndValidate(s.cfg.DSN)
	if err != nil {
		return source.Permanent(fmt.Errorf("mongodb: invalid dsn: %w", err))
	}
	db := s.cfg.Database
	if db == "" {
		db = cs.Database
	}
	if db == "" {
		return source.Permanent(errors.New("mongodb: source.database is required (or put the database in the dsn path)"))
	}
	client, err := mongo.Connect(options.Client().ApplyURI(s.cfg.DSN))
	if err != nil {
		return source.Permanent(fmt.Errorf("mongodb: %w", err))
	}
	s.client = client

	var hello struct {
		SetName string `bson:"setName"`
		Msg     string `bson:"msg"`
	}
	if err := client.Database("admin").RunCommand(ctx, bson.D{{Key: "hello", Value: 1}}).Decode(&hello); err != nil {
		return fmt.Errorf("mongodb: connect: %w", err)
	}
	if hello.SetName == "" && hello.Msg != "isdbgrid" {
		return source.Permanent(errors.New("mongodb: change streams need a replica set or sharded cluster; " +
			"start mongod with --replSet and run rs.initiate() (a single node replica set is fine)"))
	}
	s.coll = client.Database(db).Collection(s.cfg.Table)
	return nil
}

// Schema samples up to 100 documents to infer the fields.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	cur, err := s.coll.Aggregate(ctx, mongo.Pipeline{{{Key: "$sample", Value: bson.D{{Key: "size", Value: sampleSize}}}}})
	if err != nil {
		return nil, fmt.Errorf("mongodb: sample: %w", err)
	}
	defer cur.Close(ctx)
	var docs []bson.Raw
	for cur.Next(ctx) {
		docs = append(docs, append(bson.Raw(nil), cur.Current...))
	}
	if err := cur.Err(); err != nil {
		return nil, fmt.Errorf("mongodb: sample: %w", err)
	}
	out := &model.Schema{
		Columns:    inferSchema(docs),
		PrimaryKey: []string{keyField},
		Inferred:   true,
		RowCount:   -1,
	}
	if len(out.Columns) == 0 {
		out.Columns = []model.Column{{Name: keyField, Type: "objectId", PrimaryKey: true}}
	}
	if n, err := s.coll.EstimatedDocumentCount(ctx); err == nil {
		out.RowCount = n
	}
	return out, nil
}

func (s *Source) watch(ctx context.Context, from model.Position) (*mongo.ChangeStream, error) {
	opts := options.ChangeStream().SetFullDocument(options.UpdateLookup)
	if from != "" {
		token, err := decodePosition(from)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", source.ErrPositionLost, err)
		}
		opts.SetResumeAfter(token)
	}
	cs, err := s.coll.Watch(ctx, mongo.Pipeline{}, opts)
	if err != nil {
		return nil, classify(err)
	}
	return cs, nil
}

// CurrentPosition opens a change stream and returns its post batch resume
// token without consuming events.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	cs, err := s.watch(ctx, "")
	if err != nil {
		return "", err
	}
	defer cs.Close(context.WithoutCancel(ctx))
	token := cs.ResumeToken()
	if token == nil {
		// Servers older than 4.0.7 send no post batch resume token with the
		// initial batch. An event read here happened before the snapshot,
		// which copies it anyway.
		cs.TryNext(ctx)
		if err := cs.Err(); err != nil {
			return "", classify(err)
		}
		token = cs.ResumeToken()
	}
	if token == nil {
		return "", errors.New("mongodb: change stream returned no resume token")
	}
	return encodePosition(token)
}

// Snapshot reads every document of the collection.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	cur, err := s.coll.Find(ctx, bson.D{}, options.Find().SetBatchSize(batchSize))
	if err != nil {
		return fmt.Errorf("mongodb: snapshot: %w", err)
	}
	defer cur.Close(context.WithoutCancel(ctx))
	batch := make([]model.Event, 0, batchSize)
	for cur.Next(ctx) {
		row, err := docToRow(cur.Current)
		if err != nil {
			return fmt.Errorf("mongodb: snapshot: %w", err)
		}
		batch = append(batch, model.Event{Op: model.OpUpsert, Row: row, Snapshot: true})
		if len(batch) == batchSize {
			if err := emit(ctx, batch); err != nil {
				return err
			}
			batch = make([]model.Event, 0, batchSize)
		}
	}
	if err := cur.Err(); err != nil {
		return fmt.Errorf("mongodb: snapshot: %w", err)
	}
	if len(batch) > 0 {
		return emit(ctx, batch)
	}
	return nil
}

type changeEvent struct {
	OperationType string         `bson:"operationType"`
	FullDocument  bson.RawValue  `bson:"fullDocument"`
	DocumentKey   bson.Raw       `bson:"documentKey"`
	ClusterTime   bson.Timestamp `bson:"clusterTime"`
	WallTime      *bson.DateTime `bson:"wallTime"`
}

// Stream emits changes after from. Events of one server batch are emitted
// together.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	cs, err := s.watch(ctx, from)
	if err != nil {
		return err
	}
	defer cs.Close(context.WithoutCancel(ctx))
	var batch []model.Event
	for cs.Next(ctx) {
		ev, stop, err := s.toEvent(cs)
		if err != nil {
			return err
		}
		if ev != nil {
			batch = append(batch, *ev)
		}
		if stop != nil || cs.RemainingBatchLength() == 0 {
			if len(batch) > 0 {
				if err := emit(ctx, batch); err != nil {
					return err
				}
				batch = nil
			}
		}
		if stop != nil {
			return stop
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return classify(cs.Err())
}

// toEvent converts the current change. stop is set for events that end the
// stream (invalidate).
func (s *Source) toEvent(cs *mongo.ChangeStream) (ev *model.Event, stop error, err error) {
	var ce changeEvent
	if err := cs.Decode(&ce); err != nil {
		return nil, nil, fmt.Errorf("mongodb: decode change: %w", err)
	}
	pos, err := encodePosition(cs.ResumeToken())
	if err != nil {
		return nil, nil, err
	}
	out := model.Event{Position: pos}
	switch {
	case ce.WallTime != nil:
		out.Time = ce.WallTime.Time().UTC()
	case ce.ClusterTime.T != 0:
		out.Time = time.Unix(int64(ce.ClusterTime.T), 0).UTC()
	}
	switch ce.OperationType {
	case "insert", "update", "replace":
		doc, ok := ce.FullDocument.DocumentOK()
		if !ok {
			// Deleted before the update lookup ran; the delete follows.
			return nil, nil, nil
		}
		row, err := docToRow(doc)
		if err != nil {
			return nil, nil, fmt.Errorf("mongodb: convert document: %w", err)
		}
		out.Op, out.Row = model.OpUpsert, row
	case "delete":
		id, err := ce.DocumentKey.LookupErr(keyField)
		if err != nil {
			return nil, nil, fmt.Errorf("mongodb: delete event without _id: %w", err)
		}
		v, err := convertValue(id)
		if err != nil {
			return nil, nil, err
		}
		out.Op, out.Row = model.OpDelete, map[string]any{keyField: v}
	case "drop", "rename", "dropDatabase":
		out.Op = model.OpTruncate
	case "invalidate":
		out.Op = model.OpTruncate
		return &out, source.Permanent(fmt.Errorf("mongodb: change stream on %s invalidated (collection dropped or renamed); recreate the task to resync", s.coll.Name())), nil
	default:
		// DDL and other events (createIndexes, modify...) carry no row data.
		return nil, nil, nil
	}
	return &out, nil, nil
}

// Ack is a no-op: the resume token alone describes progress.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close disconnects.
func (s *Source) Close() error {
	if s.client == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.client.Disconnect(ctx)
}

// classify maps server errors to ErrPositionLost or permanent errors.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var se mongo.ServerError
	if errors.As(err, &se) {
		switch {
		case se.HasErrorCode(codeChangeStreamHistoryLost), se.HasErrorCode(codeInvalidResumeToken), se.HasErrorCode(codeChangeStreamFatal):
			return fmt.Errorf("%w: %v", source.ErrPositionLost, err)
		case se.HasErrorCode(codeChangeStreamNotSupported):
			return source.Permanent(fmt.Errorf("mongodb: change streams need a replica set: %w", err))
		}
	}
	return fmt.Errorf("mongodb: change stream: %w", err)
}

// encodePosition serialises a resume token as canonical extended JSON.
func encodePosition(token bson.Raw) (model.Position, error) {
	b, err := bson.MarshalExtJSON(token, true, false)
	if err != nil {
		return "", fmt.Errorf("mongodb: encode resume token: %w", err)
	}
	return model.Position(b), nil
}

func decodePosition(pos model.Position) (bson.Raw, error) {
	var token bson.Raw
	if err := bson.UnmarshalExtJSON([]byte(pos), true, &token); err != nil {
		return nil, fmt.Errorf("mongodb: invalid position %q: %w", pos, err)
	}
	return token, nil
}
