package dynamodb

import (
	"reflect"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	streamtypes "github.com/aws/aws-sdk-go-v2/service/dynamodbstreams/types"
)

func TestItemToRow(t *testing.T) {
	row := itemToRow(map[string]types.AttributeValue{
		"id":    &types.AttributeValueMemberN{Value: "42"},
		"big":   &types.AttributeValueMemberN{Value: "1e3"},
		"price": &types.AttributeValueMemberN{Value: "9.5"},
		"name":  &types.AttributeValueMemberS{Value: "x"},
		"ok":    &types.AttributeValueMemberBOOL{Value: true},
		"nil":   &types.AttributeValueMemberNULL{Value: true},
		"bin":   &types.AttributeValueMemberB{Value: []byte{1}},
		"list":  &types.AttributeValueMemberL{Value: []types.AttributeValue{&types.AttributeValueMemberS{Value: "a"}, &types.AttributeValueMemberN{Value: "1"}}},
		"map":   &types.AttributeValueMemberM{Value: map[string]types.AttributeValue{"k": &types.AttributeValueMemberN{Value: "-3"}}},
		"ss":    &types.AttributeValueMemberSS{Value: []string{"a", "b"}},
		"ns":    &types.AttributeValueMemberNS{Value: []string{"1", "2.5"}},
		"huge":  &types.AttributeValueMemberN{Value: "123456789012345678901234567890"},
	})
	want := map[string]any{
		"id": int64(42), "big": int64(1000), "price": 9.5, "name": "x", "ok": true, "nil": nil,
		"bin":  []byte{1},
		"list": []any{"a", int64(1)},
		"map":  map[string]any{"k": int64(-3)},
		"ss":   []any{"a", "b"},
		"ns":   []any{int64(1), 2.5},
		"huge": 1.2345678901234568e+29,
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("got  %#v\nwant %#v", row, want)
	}
}

func TestStreamImageToRow(t *testing.T) {
	row, err := streamImageToRow(map[string]streamtypes.AttributeValue{
		"id":   &streamtypes.AttributeValueMemberN{Value: "7"},
		"name": &streamtypes.AttributeValueMemberS{Value: "n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(row, map[string]any{"id": int64(7), "name": "n"}) {
		t.Fatalf("%#v", row)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	since := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	p := position{Shards: map[string]string{"shardId-1": "100"}, Done: []string{"shardId-0"}, Since: since}
	enc := p.encode()
	if string(enc) != `{"shards":{"shardId-1":"100"},"done":["shardId-0"],"since":"2026-01-02T03:04:05Z"}` {
		t.Fatalf("encode: %s", enc)
	}
	back, err := decodePosition(enc)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, p) || !back.isDone("shardId-0") || back.isDone("shardId-1") {
		t.Fatalf("round trip: %+v", back)
	}
	empty, err := decodePosition(position{Since: since}.encode())
	if err != nil || empty.Shards == nil || len(empty.Shards) != 0 {
		t.Fatalf("empty: %+v %v", empty, err)
	}
	if _, err := decodePosition("{"); err == nil {
		t.Fatal("want error")
	}
}
