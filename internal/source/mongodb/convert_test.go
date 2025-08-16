package mongodb

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

func TestDocToRow(t *testing.T) {
	oid := bson.NewObjectID()
	ts := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	dec, _ := bson.ParseDecimal128("12.50")
	huge, _ := bson.ParseDecimal128("1E+6000")
	doc, err := bson.Marshal(bson.D{
		{Key: "_id", Value: oid},
		{Key: "i32", Value: int32(7)},
		{Key: "i64", Value: int64(8)},
		{Key: "f", Value: 1.5},
		{Key: "s", Value: "x"},
		{Key: "b", Value: true},
		{Key: "n", Value: nil},
		{Key: "d", Value: bson.NewDateTimeFromTime(ts)},
		{Key: "dec", Value: dec},
		{Key: "huge", Value: huge},
		{Key: "bin", Value: bson.Binary{Subtype: 0, Data: []byte{1, 2}}},
		{Key: "uuid", Value: bson.Binary{Subtype: 4, Data: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15}}},
		{Key: "obj", Value: bson.D{{Key: "a", Value: int32(1)}, {Key: "arr", Value: bson.A{"x", int64(2)}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	row, err := docToRow(doc)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"_id":  oid.Hex(),
		"i32":  int64(7),
		"i64":  int64(8),
		"f":    1.5,
		"s":    "x",
		"b":    true,
		"n":    nil,
		"d":    ts,
		"dec":  12.5,
		"huge": "1E+6000",
		"bin":  []byte{1, 2},
		"uuid": "00010203-0405-0607-0809-0a0b0c0d0e0f",
		"obj":  map[string]any{"a": int64(1), "arr": []any{"x", int64(2)}},
	}
	if !reflect.DeepEqual(row, want) {
		t.Fatalf("got  %#v\nwant %#v", row, want)
	}
}

func TestInferSchema(t *testing.T) {
	d1, _ := bson.Marshal(bson.D{{Key: "_id", Value: int32(1)}, {Key: "name", Value: "a"}, {Key: "v", Value: int32(1)}})
	d2, _ := bson.Marshal(bson.D{{Key: "_id", Value: int32(2)}, {Key: "name", Value: nil}, {Key: "v", Value: "s"}, {Key: "extra", Value: true}})
	cols := inferSchema([]bson.Raw{d1, d2})
	got := map[string]string{}
	for _, c := range cols {
		got[c.Name] = c.Type
		switch c.Name {
		case "_id":
			if !c.PrimaryKey || c.Nullable {
				t.Errorf("_id: %+v", c)
			}
		case "name", "extra":
			if !c.Nullable {
				t.Errorf("%s should be nullable", c.Name)
			}
		}
	}
	want := map[string]string{"_id": "int", "name": "string", "v": "mixed", "extra": "bool"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("types: %v", got)
	}
}

func TestPositionRoundTrip(t *testing.T) {
	token, _ := bson.Marshal(bson.D{{Key: "_data", Value: "8263F1A2B3000000012B0429296E1404"}})
	pos, err := encodePosition(token)
	if err != nil {
		t.Fatal(err)
	}
	if string(pos) != `{"_data":"8263F1A2B3000000012B0429296E1404"}` {
		t.Fatalf("position: %s", pos)
	}
	back, err := decodePosition(pos)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual([]byte(back), []byte(token)) {
		t.Fatalf("round trip mismatch")
	}
	if _, err := decodePosition("garbage"); err == nil {
		t.Fatal("want error")
	}
}

func TestClassify(t *testing.T) {
	for code, want := range map[int32]func(error) bool{
		286:   func(err error) bool { return errors.Is(err, source.ErrPositionLost) },
		260:   func(err error) bool { return errors.Is(err, source.ErrPositionLost) },
		40573: source.IsPermanent,
		11600: func(err error) bool { return !source.IsPermanent(err) && !errors.Is(err, source.ErrPositionLost) },
	} {
		if err := classify(mongo.CommandError{Code: code, Message: "x"}); !want(err) {
			t.Errorf("code %d: unexpected classification %v", code, err)
		}
	}
	if classify(nil) != nil {
		t.Error("nil error")
	}
}
