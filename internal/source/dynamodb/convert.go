package dynamodb

import (
	"fmt"
	"strconv"

	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	streamtypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"

	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

// itemToRow converts a DynamoDB item to a normalised row. Numbers become
// int64 when integral, float64 otherwise (or their string form when they
// fit neither); sets become []any.
func itemToRow(item map[string]types.AttributeValue) map[string]any {
	row := make(map[string]any, len(item))
	for k, v := range item {
		row[k] = convertValue(v)
	}
	return row
}

func streamImageToRow(img map[string]streamtypes.AttributeValue) (map[string]any, error) {
	item, err := attributevalue.FromDynamoDBStreamsMap(img)
	if err != nil {
		return nil, fmt.Errorf("dynamodb: convert stream image: %w", err)
	}
	return itemToRow(item), nil
}

func convertValue(av types.AttributeValue) any {
	switch v := av.(type) {
	case *types.AttributeValueMemberS:
		return v.Value
	case *types.AttributeValueMemberN:
		return parseNumber(v.Value)
	case *types.AttributeValueMemberB:
		return v.Value
	case *types.AttributeValueMemberBOOL:
		return v.Value
	case *types.AttributeValueMemberNULL:
		return nil
	case *types.AttributeValueMemberL:
		out := make([]any, len(v.Value))
		for i, e := range v.Value {
			out[i] = convertValue(e)
		}
		return out
	case *types.AttributeValueMemberM:
		return itemToRow(v.Value)
	case *types.AttributeValueMemberSS:
		out := make([]any, len(v.Value))
		for i, e := range v.Value {
			out[i] = e
		}
		return out
	case *types.AttributeValueMemberNS:
		out := make([]any, len(v.Value))
		for i, e := range v.Value {
			out[i] = parseNumber(e)
		}
		return out
	case *types.AttributeValueMemberBS:
		out := make([]any, len(v.Value))
		for i, e := range v.Value {
			out[i] = e
		}
		return out
	}
	return nil
}

func parseNumber(s string) any {
	if i, err := strconv.ParseInt(s, 10, 64); err == nil {
		return i
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return source.IntegralFloat(f)
	}
	return s
}

// typeName is the schema type reported for an attribute value.
func typeName(av types.AttributeValue) string {
	switch av.(type) {
	case *types.AttributeValueMemberS:
		return "string"
	case *types.AttributeValueMemberN:
		return "number"
	case *types.AttributeValueMemberB:
		return "binary"
	case *types.AttributeValueMemberBOOL:
		return "bool"
	case *types.AttributeValueMemberNULL:
		return "null"
	case *types.AttributeValueMemberL:
		return "list"
	case *types.AttributeValueMemberM:
		return "map"
	case *types.AttributeValueMemberSS:
		return "stringSet"
	case *types.AttributeValueMemberNS:
		return "numberSet"
	case *types.AttributeValueMemberBS:
		return "binarySet"
	}
	return "unknown"
}

func scalarTypeName(t types.ScalarAttributeType) string {
	switch t {
	case types.ScalarAttributeTypeS:
		return "string"
	case types.ScalarAttributeTypeN:
		return "number"
	case types.ScalarAttributeTypeB:
		return "binary"
	}
	return string(t)
}
