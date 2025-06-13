package sink

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Applier sends documents and waits for the result. Besides backing the
// Writer it is used directly for DLQ replays and diff repairs.
type Applier struct {
	idx     Index
	tasks   Tasks
	cfg     Config
	onRetry func(int, error)
	// docSize is the recent average encoded document size, used to size
	// encode buffers in one allocation.
	docSize atomic.Int64
}

// NewApplier returns an Applier for one index.
func NewApplier(idx Index, tasks Tasks, cfg Config) *Applier {
	return &Applier{idx: idx, tasks: tasks, cfg: cfg.withDefaults()}
}

func (a *Applier) policy() retry.Policy {
	p := a.cfg.Retry
	user := p.OnRetry
	p.OnRetry = func(attempt int, err error, wait time.Duration) {
		if a.onRetry != nil {
			a.onRetry(attempt, err)
		}
		if user != nil {
			user(attempt, err, wait)
		}
	}
	return p
}

func (a *Applier) opts() *meilisearch.DocumentOptions {
	pk := a.cfg.PrimaryKey
	o := &meilisearch.DocumentOptions{TaskCustomMetadata: "cdc:" + a.cfg.TaskID}
	if pk != "" {
		o.PrimaryKey = &pk
	}
	return o
}

// deleteOpts carries only task metadata: delete endpoints reject primaryKey.
func (a *Applier) deleteOpts() *meilisearch.DocumentOptions {
	return &meilisearch.DocumentOptions{TaskCustomMetadata: "cdc:" + a.cfg.TaskID}
}

// requestErr marks errors that retrying the same request cannot fix.
func (a *Applier) requestErr(err error) error {
	if classifyRequestError(err) != kindRetry {
		return retry.Permanent(err)
	}
	return err
}

// enqueue sends upserts, replacements, merges and deletes and returns their
// task uids.
func (a *Applier) enqueue(ctx context.Context, upserts, replace, merge []transform.Doc, deletes []string) ([]int64, error) {
	var uids []int64
	if len(upserts) > 0 {
		u, err := a.enqueueDocs(ctx, upserts, a.cfg.WriteMode == model.WriteReplace)
		uids = append(uids, u...)
		if err != nil {
			return uids, err
		}
	}
	if len(replace) > 0 {
		u, err := a.enqueueDocs(ctx, replace, true)
		uids = append(uids, u...)
		if err != nil {
			return uids, err
		}
	}
	if len(merge) > 0 {
		u, err := a.enqueueDocs(ctx, merge, false)
		uids = append(uids, u...)
		if err != nil {
			return uids, err
		}
	}
	if len(deletes) > 0 {
		u, err := a.enqueueDeletes(ctx, deletes)
		uids = append(uids, u...)
		if err != nil {
			return uids, err
		}
	}
	return uids, nil
}

func (a *Applier) enqueueDocs(ctx context.Context, docs []transform.Doc, replace bool) ([]int64, error) {
	// Encode once; retries resend the same bytes.
	buf := getBuf()
	defer putBuf(buf)
	if avg := a.docSize.Load(); avg > 0 {
		buf.Grow(int(avg) * len(docs) * 9 / 8)
	}
	if err := encodeNDJSON(buf, docs); err != nil {
		return nil, retry.Permanent(fmt.Errorf("encode documents: %w", err))
	}
	body := buf.Bytes()
	a.docSize.Store(int64(len(body) / max(len(docs), 1)))
	uid, err := retry.Do(ctx, a.policy(), func(ctx context.Context, _ int) (int64, error) {
		var (
			info *meilisearch.TaskInfo
			err  error
		)
		if replace {
			info, err = a.idx.AddDocumentsNdjson(ctx, body, a.opts())
		} else {
			info, err = a.idx.UpdateDocumentsNdjson(ctx, body, a.opts())
		}
		if err != nil {
			return 0, a.requestErr(err)
		}
		return info.TaskUID, nil
	})
	if err != nil && len(docs) > 1 && classifyRequestError(err) == kindTooLarge {
		mid := len(docs) / 2
		left, err := a.enqueueDocs(ctx, docs[:mid], replace)
		if err != nil {
			return left, err
		}
		right, err := a.enqueueDocs(ctx, docs[mid:], replace)
		return append(left, right...), err
	}
	if err != nil {
		return nil, err
	}
	return []int64{uid}, nil
}

func (a *Applier) enqueueDeletes(ctx context.Context, ids []string) ([]int64, error) {
	uid, err := retry.Do(ctx, a.policy(), func(ctx context.Context, _ int) (int64, error) {
		info, err := a.idx.DeleteDocuments(ctx, ids, a.deleteOpts())
		if err != nil {
			return 0, a.requestErr(err)
		}
		return info.TaskUID, nil
	})
	if err != nil && len(ids) > 1 && classifyRequestError(err) == kindTooLarge {
		mid := len(ids) / 2
		left, err := a.enqueueDeletes(ctx, ids[:mid])
		if err != nil {
			return left, err
		}
		right, err := a.enqueueDeletes(ctx, ids[mid:])
		return append(left, right...), err
	}
	if err != nil {
		return nil, err
	}
	return []int64{uid}, nil
}

// wait polls a task until it reaches a final status.
func (a *Applier) wait(ctx context.Context, uid int64) (*meilisearch.Task, error) {
	interval := a.cfg.TaskPoll
	for {
		t, err := a.tasks.GetTask(ctx, uid)
		if err == nil {
			switch t.Status {
			case meilisearch.TaskStatusSucceeded, meilisearch.TaskStatusFailed, meilisearch.TaskStatusCanceled:
				return t, nil
			}
		} else if classifyRequestError(err) == kindFatal {
			return nil, fmt.Errorf("follow task %d: %w", uid, err)
		}
		if err := retry.Sleep(ctx, interval); err != nil {
			return nil, err
		}
		if interval < 500*time.Millisecond {
			interval *= 2
		}
	}
}

// ApplySync writes documents and deletes and waits for Meilisearch. Batches
// rejected because of their content are bisected until the offending
// documents are found; those are returned as DLQ entries and everything
// else is applied.
func (a *Applier) ApplySync(ctx context.Context, upserts, replace []transform.Doc, deletes []string) ([]model.DLQEntry, error) {
	return a.applySync(ctx, upserts, replace, nil, deletes)
}

func (a *Applier) applySync(ctx context.Context, upserts, replace, merge []transform.Doc, deletes []string) ([]model.DLQEntry, error) {
	var dlq []model.DLQEntry
	for _, part := range []struct {
		docs    []transform.Doc
		replace bool
	}{{upserts, a.cfg.WriteMode == model.WriteReplace}, {replace, true}, {merge, false}} {
		d, err := a.applyDocs(ctx, part.docs, part.replace)
		dlq = append(dlq, d...)
		if err != nil {
			return dlq, err
		}
	}
	d, err := a.applyDeletes(ctx, deletes)
	return append(dlq, d...), err
}

// outcome runs one attempt and reports how it failed.
func (a *Applier) outcome(ctx context.Context, uids []int64, enqErr error) (errorKind, error) {
	if enqErr != nil {
		k := classifyRequestError(enqErr)
		if ctx.Err() != nil {
			k = kindFatal
		}
		return k, enqErr
	}
	for _, uid := range uids {
		t, err := a.wait(ctx, uid)
		if err != nil {
			return kindFatal, err
		}
		switch t.Status {
		case meilisearch.TaskStatusSucceeded:
		case meilisearch.TaskStatusCanceled:
			return kindRetry, fmt.Errorf("task %d was canceled", uid)
		default:
			return classifyTaskError(string(t.Error.Code), t.Error.Message), fmt.Errorf("task %d failed: %s (%s)", uid, t.Error.Message, t.Error.Code)
		}
	}
	return 0, nil
}

func (a *Applier) applyDocs(ctx context.Context, docs []transform.Doc, replace bool) ([]model.DLQEntry, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	p := a.policy()
	for attempt := 1; ; attempt++ {
		uids, enqErr := a.enqueueDocs(ctx, docs, replace)
		kind, err := a.outcome(ctx, uids, enqErr)
		if err == nil {
			return nil, nil
		}
		switch kind {
		case kindDocument, kindTooLarge:
			if len(docs) == 1 {
				op := "upsert"
				if replace {
					op = "replace"
				}
				return []model.DLQEntry{a.dlqEntry(docID(docs[0], a.cfg.PrimaryKey), op, docs[0], err, attempt)}, nil
			}
			mid := len(docs) / 2
			left, lerr := a.applyDocs(ctx, docs[:mid], replace)
			if lerr != nil {
				return left, lerr
			}
			right, rerr := a.applyDocs(ctx, docs[mid:], replace)
			return append(left, right...), rerr
		case kindRetry:
			if p.MaxAttempts > 0 && attempt >= p.MaxAttempts {
				return nil, fmt.Errorf("%w: %w", retry.ErrExhausted, err)
			}
			if p.OnRetry != nil {
				p.OnRetry(attempt, err, 0)
			}
			if serr := retry.Sleep(ctx, p.Backoff(attempt)); serr != nil {
				return nil, serr
			}
		default:
			return nil, err
		}
	}
}

func (a *Applier) applyDeletes(ctx context.Context, ids []string) ([]model.DLQEntry, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	p := a.policy()
	for attempt := 1; ; attempt++ {
		uids, enqErr := a.enqueueDeletes(ctx, ids)
		kind, err := a.outcome(ctx, uids, enqErr)
		if err == nil {
			return nil, nil
		}
		switch kind {
		case kindDocument, kindTooLarge:
			if len(ids) == 1 {
				return []model.DLQEntry{a.dlqEntry(ids[0], "delete", nil, err, attempt)}, nil
			}
			mid := len(ids) / 2
			left, lerr := a.applyDeletes(ctx, ids[:mid])
			if lerr != nil {
				return left, lerr
			}
			right, rerr := a.applyDeletes(ctx, ids[mid:])
			return append(left, right...), rerr
		case kindRetry:
			if p.MaxAttempts > 0 && attempt >= p.MaxAttempts {
				return nil, fmt.Errorf("%w: %w", retry.ErrExhausted, err)
			}
			if p.OnRetry != nil {
				p.OnRetry(attempt, err, 0)
			}
			if serr := retry.Sleep(ctx, p.Backoff(attempt)); serr != nil {
				return nil, serr
			}
		default:
			return nil, err
		}
	}
}

func (a *Applier) dlqEntry(id, op string, doc transform.Doc, err error, attempts int) model.DLQEntry {
	return model.DLQEntry{
		TaskID:    a.cfg.TaskID,
		DocID:     id,
		Op:        op,
		Document:  doc,
		Error:     err.Error(),
		Code:      taskErrCode(err),
		Attempts:  attempts,
		CreatedAt: time.Now().UTC(),
	}
}

func taskErrCode(err error) string {
	if c := errCode(err); c != "" {
		return c
	}
	// Task failures are formatted as "... (code)".
	s := err.Error()
	if n := len(s); n > 2 && s[n-1] == ')' {
		for i := n - 2; i >= 0; i-- {
			if s[i] == '(' {
				return s[i+1 : n-1]
			}
		}
	}
	return ""
}
