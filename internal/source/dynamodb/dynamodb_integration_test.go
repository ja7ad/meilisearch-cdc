//go:build integration

package dynamodb

import (
	"context"
	"strconv"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/testcontainers/testcontainers-go"
	tcdynamodb "github.com/testcontainers/testcontainers-go/modules/dynamodb"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/sourcetest"
)

func startDynamo(t *testing.T) (model.Source, *dynamodb.Client) {
	t.Helper()
	ctx := context.Background()
	c, err := tcdynamodb.Run(ctx, "amazon/dynamodb-local:2.2.1", tcdynamodb.WithDisableTelemetry())
	testcontainers.CleanupContainer(t, c)
	if err != nil {
		t.Fatal(err)
	}
	hostPort, err := c.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.Source{
		Type: model.SourceDynamoDB, Region: "us-east-1", Endpoint: "http://" + hostPort,
		AccessKeyID: "dummy", SecretAccessKey: "dummy",
	}
	s := New(cfg)
	s.cfg.Table = "_probe"
	_ = s.Open(ctx) // builds the clients; the table does not exist
	return cfg, s.db
}

func createTable(t *testing.T, db *dynamodb.Client, name string, stream *types.StreamSpecification) {
	t.Helper()
	_, err := db.CreateTable(context.Background(), &dynamodb.CreateTableInput{
		TableName:            aws.String(name),
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String("id"), AttributeType: types.ScalarAttributeTypeN}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String("id"), KeyType: types.KeyTypeHash}},
		BillingMode:          types.BillingModePayPerRequest,
		StreamSpecification:  stream,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func num(i int) types.AttributeValue {
	return &types.AttributeValueMemberN{Value: strconv.Itoa(i)}
}

func TestConformance(t *testing.T) {
	cfg, db := startDynamo(t)
	cfg.Table = "items"
	createTable(t, db, "items", &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeNewAndOldImages})
	ctx := context.Background()

	sourcetest.Run(t, sourcetest.Harness{
		NewSource: func(t *testing.T) source.Source {
			s, err := source.New(cfg, "t1")
			if err != nil {
				t.Fatal(err)
			}
			return s
		},
		Insert: func(t *testing.T, id int, name string, price float64) {
			_, err := db.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String("items"), Item: map[string]types.AttributeValue{
				"id":    num(id),
				"name":  &types.AttributeValueMemberS{Value: name},
				"price": &types.AttributeValueMemberN{Value: strconv.FormatFloat(price, 'f', -1, 64)},
			}})
			if err != nil {
				t.Fatal(err)
			}
		},
		Update: func(t *testing.T, id int, name string) {
			_, err := db.UpdateItem(ctx, &dynamodb.UpdateItemInput{
				TableName:                 aws.String("items"),
				Key:                       map[string]types.AttributeValue{"id": num(id)},
				UpdateExpression:          aws.String("SET #n = :n"),
				ExpressionAttributeNames:  map[string]string{"#n": "name"},
				ExpressionAttributeValues: map[string]types.AttributeValue{":n": &types.AttributeValueMemberS{Value: name}},
			})
			if err != nil {
				t.Fatal(err)
			}
		},
		Delete: func(t *testing.T, id int) {
			_, err := db.DeleteItem(ctx, &dynamodb.DeleteItemInput{TableName: aws.String("items"), Key: map[string]types.AttributeValue{"id": num(id)}})
			if err != nil {
				t.Fatal(err)
			}
		},
	})

	t.Run("misconfigured", func(t *testing.T) {
		createTable(t, db, "nostream", nil)
		createTable(t, db, "keysonly", &types.StreamSpecification{StreamEnabled: aws.Bool(true), StreamViewType: types.StreamViewTypeKeysOnly})
		for _, table := range []string{"nostream", "keysonly", "missing"} {
			c := cfg
			c.Table = table
			if err := New(c).Open(ctx); !source.IsPermanent(err) {
				t.Errorf("%s: want permanent error, got %v", table, err)
			}
		}
	})
}
