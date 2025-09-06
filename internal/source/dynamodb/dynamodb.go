// Package dynamodb captures changes from a DynamoDB table through DynamoDB
// Streams. The table needs a stream with the NEW_IMAGE or
// NEW_AND_OLD_IMAGES view type.
package dynamodb

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/dynamodbstreams"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	scanSegments = 4
	sampleSize   = 100
)

func init() {
	source.Register(model.SourceDynamoDB, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

// Source streams one table.
type Source struct {
	cfg       model.Source
	db        *dynamodb.Client
	streams   *dynamodbstreams.Client
	streamARN string
	pk        []string
	keyTypes  map[string]string
	itemCount int64

	// Polling intervals, shortened by tests.
	pollInterval     time.Duration
	discoverInterval time.Duration
}

// New returns an unopened source.
func New(cfg model.Source) *Source {
	return &Source{cfg: cfg, pollInterval: time.Second, discoverInterval: 10 * time.Second}
}

// Open builds the clients and checks the table stream.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.Table == "" {
		return source.Permanent(errors.New("dynamodb: source.table is required"))
	}
	var opts []func(*config.LoadOptions) error
	if s.cfg.Region != "" {
		opts = append(opts, config.WithRegion(s.cfg.Region))
	}
	if s.cfg.AccessKeyID != "" {
		opts = append(opts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(s.cfg.AccessKeyID, s.cfg.SecretAccessKey, "")))
	}
	awsCfg, err := config.LoadDefaultConfig(ctx, opts...)
	if err != nil {
		return source.Permanent(fmt.Errorf("dynamodb: load aws config: %w", err))
	}
	if awsCfg.Region == "" {
		return source.Permanent(errors.New("dynamodb: source.region is required (or set AWS_REGION)"))
	}
	s.db = dynamodb.NewFromConfig(awsCfg, func(o *dynamodb.Options) {
		if s.cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.cfg.Endpoint)
		}
	})
	s.streams = dynamodbstreams.NewFromConfig(awsCfg, func(o *dynamodbstreams.Options) {
		if s.cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(s.cfg.Endpoint)
		}
	})

	out, err := s.db.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(s.cfg.Table)})
	if err != nil {
		var nf *types.ResourceNotFoundException
		if errors.As(err, &nf) {
			return source.Permanent(fmt.Errorf("dynamodb: table %q does not exist in %s", s.cfg.Table, awsCfg.Region))
		}
		return fmt.Errorf("dynamodb: describe table: %w", err)
	}
	t := out.Table
	spec := t.StreamSpecification
	if spec == nil || !aws.ToBool(spec.StreamEnabled) || t.LatestStreamArn == nil {
		return source.Permanent(fmt.Errorf("dynamodb: table %q has no stream; enable one with "+
			"`aws dynamodb update-table --table-name %s --stream-specification StreamEnabled=true,StreamViewType=NEW_AND_OLD_IMAGES`",
			s.cfg.Table, s.cfg.Table))
	}
	if spec.StreamViewType != types.StreamViewTypeNewImage && spec.StreamViewType != types.StreamViewTypeNewAndOldImages {
		return source.Permanent(fmt.Errorf("dynamodb: the stream of table %q has view type %s; it must include new images: "+
			"disable the stream and enable it again with StreamViewType=NEW_AND_OLD_IMAGES", s.cfg.Table, spec.StreamViewType))
	}
	s.streamARN = aws.ToString(t.LatestStreamArn)
	s.itemCount = aws.ToInt64(t.ItemCount)

	s.keyTypes = map[string]string{}
	for _, d := range t.AttributeDefinitions {
		s.keyTypes[aws.ToString(d.AttributeName)] = scalarTypeName(d.AttributeType)
	}
	s.pk = s.pk[:0]
	for _, kt := range []types.KeyType{types.KeyTypeHash, types.KeyTypeRange} {
		for _, k := range t.KeySchema {
			if k.KeyType == kt {
				s.pk = append(s.pk, aws.ToString(k.AttributeName))
			}
		}
	}
	return nil
}

// Schema reports the key attributes plus the attributes of up to 100 items.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	out, err := s.db.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(s.cfg.Table), Limit: aws.Int32(sampleSize)})
	if err != nil {
		return nil, fmt.Errorf("dynamodb: sample: %w", err)
	}
	sc := &model.Schema{PrimaryKey: append([]string(nil), s.pk...), Inferred: true, RowCount: s.itemCount}
	seen := map[string]int{}
	isKey := map[string]bool{}
	for _, k := range s.pk {
		isKey[k] = true
		seen[k] = len(sc.Columns)
		sc.Columns = append(sc.Columns, model.Column{Name: k, Type: s.keyTypes[k], PrimaryKey: true})
	}
	count := map[string]int{}
	for _, item := range out.Items {
		for k, v := range item {
			count[k]++
			typ := typeName(v)
			i, ok := seen[k]
			if !ok {
				seen[k] = len(sc.Columns)
				sc.Columns = append(sc.Columns, model.Column{Name: k, Type: typ})
				continue
			}
			c := &sc.Columns[i]
			if typ == "null" {
				c.Nullable = !isKey[k]
			} else if c.Type != typ && !isKey[k] {
				c.Type = "mixed"
			}
		}
	}
	for i := range sc.Columns {
		c := &sc.Columns[i]
		if !c.PrimaryKey && (count[c.Name] < len(out.Items) || c.Type == "null") {
			c.Nullable = true
		}
	}
	return sc, nil
}

// CurrentPosition records the current time; shards are discovered when
// streaming starts.
func (s *Source) CurrentPosition(context.Context) (model.Position, error) {
	return position{Since: time.Now().UTC()}.encode(), nil
}

// Snapshot scans the table with parallel segments.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var (
		mu     sync.Mutex // serialises emit
		wg     sync.WaitGroup
		errMu  sync.Mutex
		result error
	)
	fail := func(err error) {
		errMu.Lock()
		if result == nil {
			result = err
		}
		errMu.Unlock()
		cancel()
	}
	for seg := range int32(scanSegments) {
		wg.Go(func() {
			p := dynamodb.NewScanPaginator(s.db, &dynamodb.ScanInput{
				TableName:     aws.String(s.cfg.Table),
				Segment:       aws.Int32(seg),
				TotalSegments: aws.Int32(scanSegments),
			})
			for p.HasMorePages() {
				page, err := p.NextPage(ctx)
				if err != nil {
					fail(fmt.Errorf("dynamodb: scan segment %d: %w", seg, err))
					return
				}
				if len(page.Items) == 0 {
					continue
				}
				evs := make([]model.Event, len(page.Items))
				for i, item := range page.Items {
					evs[i] = model.Event{Op: model.OpUpsert, Row: itemToRow(item), Snapshot: true}
				}
				mu.Lock()
				err = emit(ctx, evs)
				mu.Unlock()
				if err != nil {
					fail(err)
					return
				}
			}
		})
	}
	wg.Wait()
	return result
}

// Ack is a no-op: the position alone describes progress.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close releases nothing; the AWS clients hold no connections to close.
func (s *Source) Close() error { return nil }
