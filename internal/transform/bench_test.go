package transform

import (
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func benchRow(i int64) map[string]any {
	return map[string]any{
		"id": i, "name": "Product name that is reasonably long", "description": "A longer description field with several words in it",
		"price": 19.99, "in_stock": true, "category": "books", "tags": []any{"a", "b", "c"},
		"meta": map[string]any{"color": "red", "size": int64(42)}, "updated_at": time.Unix(1700000000, 0), "lat": 48.8, "lng": 2.35,
	}
}

func BenchmarkApply(b *testing.B) {
	tr, _ := New(model.Mapping{Exclude: []string{"lat"}, TimeFormat: model.TimeUnix, Geo: &model.GeoMapping{Lat: "lat", Lng: "lng"}}, []string{"id"})
	ev := model.Event{Op: model.OpUpsert, Row: benchRow(1), Position: "1"}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tr.Apply(ev); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyCompositeKey(b *testing.B) {
	tr, _ := New(model.Mapping{PrimaryKey: []string{"category", "name"}}, nil)
	ev := model.Event{Op: model.OpUpsert, Row: benchRow(1)}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := tr.Apply(ev); err != nil {
			b.Fatal(err)
		}
	}
}
