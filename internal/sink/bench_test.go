package sink

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

func BenchmarkBatchAdd(b *testing.B) {
	changes := make([]transform.Change, 1000)
	for i := range changes {
		id := strconv.Itoa(i % 700) // some hot rows coalesce
		changes[i] = transform.Change{Op: model.OpUpsert, ID: id, Doc: transform.Doc{"id": id, "v": i}, Position: model.Position(strconv.Itoa(i))}
	}
	b.ReportAllocs()
	for b.Loop() {
		bt := newBatch(model.WriteReplace)
		for _, c := range changes {
			bt.add(c)
		}
		_ = bt.toFlight()
	}
}

// BenchmarkWriterThroughput pushes documents through the writer into the
// in-memory Meilisearch fake: it measures the pipeline overhead, not
// Meilisearch.
func BenchmarkWriterThroughput(b *testing.B) {
	f := newFake("id")
	defer f.close()
	c := cfg()
	c.BatchSize = 5000
	c.MaxInflight = 8
	c.FlushInterval = 50 * time.Millisecond
	w := New(f, f, c, Hooks{})
	in := make(chan []transform.Change, 16)
	done := make(chan error, 1)
	go func() { done <- w.Run(context.Background(), in) }()
	b.ReportAllocs()
	n := 0
	for b.Loop() {
		batch := make([]transform.Change, 500)
		for i := range batch {
			id := strconv.Itoa(n)
			n++
			batch[i] = transform.Change{Op: model.OpUpsert, ID: id, Doc: transform.Doc{"id": id, "name": "x", "price": 1.5}, Position: model.Position(strconv.Itoa(n))}
		}
		in <- batch
	}
	close(in)
	if err := <-done; err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(float64(n)/b.Elapsed().Seconds(), "docs/s")
}
