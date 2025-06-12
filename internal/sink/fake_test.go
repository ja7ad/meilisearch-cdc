package sink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// fakeMeili mimics Meilisearch's asynchronous, ordered and atomic task queue.
type fakeMeili struct {
	mu      sync.Mutex
	pk      string
	docs    map[string]map[string]any
	tasks   map[int64]*meilisearch.Task
	queue   chan func() // processed in order by one goroutine
	nextUID int64

	// Failure injection.
	reject      func(doc map[string]any) string // error code for an invalid document
	enqueueErr  func(n int) error               // error for an enqueue of n items
	failNext    int                             // next tasks fail with an internal error
	processWait time.Duration

	enqueues int
	metadata []string
}

func newFake(pk string) *fakeMeili {
	f := &fakeMeili{pk: pk, docs: map[string]map[string]any{}, tasks: map[int64]*meilisearch.Task{}, queue: make(chan func(), 1024)}
	go func() {
		for fn := range f.queue {
			if f.processWait > 0 {
				time.Sleep(f.processWait)
			}
			fn()
		}
	}()
	return f
}

func (f *fakeMeili) close() { close(f.queue) }

func (f *fakeMeili) enqueue(n int, opts *meilisearch.DocumentOptions, apply func() (string, bool)) (*meilisearch.TaskInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueues++
	if opts != nil {
		f.metadata = append(f.metadata, opts.TaskCustomMetadata)
	}
	if f.enqueueErr != nil {
		if err := f.enqueueErr(n); err != nil {
			return nil, err
		}
	}
	f.nextUID++
	uid := f.nextUID
	t := &meilisearch.Task{UID: uid, Status: meilisearch.TaskStatusEnqueued}
	f.tasks[uid] = t
	fail := f.failNext > 0
	if fail {
		f.failNext--
	}
	f.queue <- func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		if fail {
			t.Status = meilisearch.TaskStatusFailed
			t.Error = meilisearch.APIErrorDetails{Code: "internal", Message: "boom"}
			return
		}
		if code, ok := apply(); !ok {
			t.Status = meilisearch.TaskStatusFailed
			t.Error = meilisearch.APIErrorDetails{Code: meilisearch.APIErrCode(code), Message: "invalid document"}
			return
		}
		t.Status = meilisearch.TaskStatusSucceeded
	}
	return &meilisearch.TaskInfo{TaskUID: uid, Status: meilisearch.TaskStatusEnqueued}, nil
}

func (f *fakeMeili) id(d map[string]any) string { return fmt.Sprint(d[f.pk]) }

// write applies documents atomically: one invalid document fails the task.
func (f *fakeMeili) write(docsPtr interface{}, merge bool) (string, bool) {
	docs := docsPtr.([]transform.Doc)
	if f.reject != nil {
		for _, d := range docs {
			if code := f.reject(d); code != "" {
				return code, false
			}
		}
	}
	for _, d := range docs {
		id := f.id(d)
		if prev, ok := f.docs[id]; ok && merge {
			m := maps.Clone(prev)
			maps.Copy(m, d)
			f.docs[id] = m
		} else {
			f.docs[id] = maps.Clone(d)
		}
	}
	return "", true
}

// decodeNDJSON parses a request body back into documents. Integers come
// back as int (json.Number) so tests can compare with what they sent.
func decodeNDJSON(body []byte) []transform.Doc {
	var docs []transform.Doc
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	for dec.More() {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			panic(err)
		}
		for k, v := range d {
			if n, ok := v.(json.Number); ok {
				if i, err := n.Int64(); err == nil {
					d[k] = int(i)
				} else {
					f, _ := n.Float64()
					d[k] = f
				}
			}
		}
		docs = append(docs, d)
	}
	return docs
}

func (f *fakeMeili) AddDocumentsNdjson(_ context.Context, body []byte, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error) {
	docs := decodeNDJSON(body)
	return f.enqueue(len(docs), opts, func() (string, bool) { return f.write(docs, false) })
}

func (f *fakeMeili) UpdateDocumentsNdjson(_ context.Context, body []byte, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error) {
	docs := decodeNDJSON(body)
	return f.enqueue(len(docs), opts, func() (string, bool) { return f.write(docs, true) })
}

func (f *fakeMeili) DeleteDocuments(_ context.Context, ids []string, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error) {
	return f.enqueue(len(ids), opts, func() (string, bool) {
		for _, id := range ids {
			delete(f.docs, id)
		}
		return "", true
	})
}

func (f *fakeMeili) DeleteAllDocuments(_ context.Context, opts *meilisearch.DocumentOptions) (*meilisearch.TaskInfo, error) {
	return f.enqueue(0, opts, func() (string, bool) {
		f.docs = map[string]map[string]any{}
		return "", true
	})
}

func (f *fakeMeili) GetStats(context.Context, *meilisearch.StatsParams) (*meilisearch.StatsIndex, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &meilisearch.StatsIndex{NumberOfDocuments: int64(len(f.docs))}, nil
}

func (f *fakeMeili) GetTask(_ context.Context, uid int64) (*meilisearch.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[uid]
	if !ok {
		return nil, &meilisearch.Error{StatusCode: 404, APIError: meilisearch.APIErrorDetails{Code: "task_not_found"}}
	}
	cp := *t
	return &cp, nil
}

func (f *fakeMeili) snapshot() map[string]map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make(map[string]map[string]any, len(f.docs))
	for k, v := range f.docs {
		out[k] = maps.Clone(v)
	}
	return out
}

func (f *fakeMeili) seed(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := 0; i < n; i++ {
		id := fmt.Sprint(i)
		f.docs[id] = map[string]any{f.pk: id}
	}
}
