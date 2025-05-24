// Package diff compares a source table with its Meilisearch index, both at
// the document level (missing, extra and stale documents) and at the schema
// level (columns, fields and settings).
package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Status of a diff run.
type Status string

const (
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// FieldDiff is a field whose value differs.
type FieldDiff struct {
	Field  string `json:"field"`
	Source any    `json:"source"`
	Index  any    `json:"index"`
}

// Mismatch is a document whose content differs.
type Mismatch struct {
	ID     string      `json:"id"`
	Fields []FieldDiff `json:"fields"`
}

// Samples lists example ids for each category.
type Samples struct {
	Missing    []string   `json:"missing"`
	Extra      []string   `json:"extra"`
	Mismatched []Mismatch `json:"mismatched"`
}

// Report is the result of a document diff.
type Report struct {
	TaskID      string    `json:"taskId"`
	IndexUID    string    `json:"indexUid"`
	Status      Status    `json:"status"`
	Error       string    `json:"error,omitempty"`
	StartedAt   time.Time `json:"startedAt"`
	FinishedAt  time.Time `json:"finishedAt,omitempty"`
	SourceCount int64     `json:"sourceCount"`
	IndexCount  int64     `json:"indexCount"`
	Missing     int64     `json:"missing"`
	Extra       int64     `json:"extra"`
	Mismatched  int64     `json:"mismatched"`
	Skipped     int64     `json:"skipped"` // source rows that could not be transformed
	InSync      bool      `json:"inSync"`
	Samples     Samples   `json:"samples"`
	// Repair results, when the run repaired the index.
	Repaired *RepairResult `json:"repaired,omitempty"`
}

// RepairResult counts the writes a repair made.
type RepairResult struct {
	Upserted int `json:"upserted"`
	Deleted  int `json:"deleted"`
	DLQ      int `json:"dlq"`
}

// Options control a run.
type Options struct {
	SampleLimit   int  `json:"sampleLimit"`
	CompareFields bool `json:"compareFields"`
	// Repair flags; when any is set the index is fixed during the run.
	RepairMissing    bool `json:"missing"`
	RepairMismatched bool `json:"mismatched"`
	RepairExtra      bool `json:"extra"`
}

func (o Options) repair() bool { return o.RepairMissing || o.RepairMismatched || o.RepairExtra }

// Documents reads documents of an index page by page.
type Documents interface {
	GetDocuments(ctx context.Context, param *meilisearch.DocumentsQuery, resp *meilisearch.DocumentsResult) error
}

// Repairer applies repair writes (normally a sink.Applier).
type Repairer interface {
	ApplySync(ctx context.Context, upserts, replace []transform.Doc, deletes []string) ([]model.DLQEntry, error)
}

// Checker runs diffs for one task.
type Checker struct {
	TaskID   string
	IndexUID string
	Source   source.Source // opened
	Tr       *transform.Transformer
	Index    Documents
	Repair   Repairer
	PageSize int64
	// Progress is called with the current report while running.
	Progress func(Report)
}

type entry struct {
	hash   uint64
	fields *[]string // interned sorted field list
	state  uint8
}

const (
	stateUnseen uint8 = iota
	stateMatch
	stateMismatch
)

// canonical returns a JSON round tripped copy so source and index values
// compare equal (int64 vs float64, time formats already applied).
func canonical(doc transform.Doc) (map[string]any, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	return out, json.Unmarshal(raw, &out)
}

// comparable returns v with every float rounded to 15 significant digits.
// Meilisearch does not keep the last digit of some floats (it returns 1.4
// for 1.4000000000000001), so exact comparison would report documents that
// are in sync as mismatched, and repairing them would never converge.
func comparable(v any) any {
	switch t := v.(type) {
	case float64:
		r, err := strconv.ParseFloat(strconv.FormatFloat(t, 'g', 15, 64), 64)
		if err != nil {
			return t
		}
		return r
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, x := range t {
			out[k] = comparable(x)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, x := range t {
			out[i] = comparable(x)
		}
		return out
	}
	return v
}

func comparableJSON(v any) []byte {
	raw, _ := json.Marshal(comparable(v))
	return raw
}

func hashFields(doc map[string]any, fields []string) uint64 {
	h := xxhash.New()
	for _, f := range fields {
		_, _ = h.WriteString(f)
		_, _ = h.Write([]byte{0})
		raw := comparableJSON(doc[f])
		_, _ = h.Write(raw)
		_, _ = h.Write([]byte{1})
	}
	return h.Sum64()
}

// Run executes the diff. It never returns partial results as success: on
// error the report status is failed.
func (c *Checker) Run(ctx context.Context, opts Options) (rep Report) {
	if opts.SampleLimit <= 0 {
		opts.SampleLimit = 50
	}
	if c.PageSize <= 0 {
		c.PageSize = 1000
	}
	rep = Report{TaskID: c.TaskID, IndexUID: c.IndexUID, Status: StatusRunning, StartedAt: time.Now().UTC()}
	fail := func(err error) Report {
		rep.Status, rep.Error, rep.FinishedAt = StatusFailed, err.Error(), time.Now().UTC()
		return rep
	}
	pk := c.Tr.PrimaryKeyField()

	// Pass 1: source.
	entries := map[string]*entry{}
	interned := map[string]*[]string{}
	var mu sync.Mutex
	err := c.Source.Snapshot(ctx, func(ctx context.Context, evs []model.Event) error {
		mu.Lock()
		defer mu.Unlock()
		for _, ev := range evs {
			ch, err := c.Tr.Apply(ev)
			if err != nil || ch.Op != model.OpUpsert {
				rep.Skipped++
				continue
			}
			doc, err := canonical(ch.Doc)
			if err != nil {
				rep.Skipped++
				continue
			}
			fields := make([]string, 0, len(doc))
			for k := range doc {
				fields = append(fields, k)
			}
			sort.Strings(fields)
			sig := strings.Join(fields, "\x00")
			fp, ok := interned[sig]
			if !ok {
				fp = &fields
				interned[sig] = fp
			}
			entries[ch.ID] = &entry{hash: hashFields(doc, *fp), fields: fp}
			rep.SourceCount++
		}
		if c.Progress != nil && rep.SourceCount%10000 < int64(len(evs)) {
			c.Progress(rep)
		}
		return nil
	})
	if err != nil {
		return fail(fmt.Errorf("read source: %w", err))
	}

	// Pass 2: index.
	var extra []string
	sampleIndexDocs := map[string]map[string]any{}
	for offset := int64(0); ; offset += c.PageSize {
		var res meilisearch.DocumentsResult
		if err := c.Index.GetDocuments(ctx, &meilisearch.DocumentsQuery{Offset: offset, Limit: c.PageSize}, &res); err != nil {
			return fail(fmt.Errorf("read index: %w", err))
		}
		for _, hit := range res.Results {
			doc := map[string]any{}
			for k, raw := range hit {
				var v any
				_ = json.Unmarshal(raw, &v)
				doc[k] = v
			}
			rep.IndexCount++
			id := idString(doc[pk])
			e, ok := entries[id]
			if !ok {
				rep.Extra++
				extra = append(extra, id)
				continue
			}
			if !opts.CompareFields || hashFields(doc, *e.fields) == e.hash {
				e.state = stateMatch
				continue
			}
			e.state = stateMismatch
			rep.Mismatched++
			if len(sampleIndexDocs) < opts.SampleLimit {
				sampleIndexDocs[id] = doc
			}
		}
		if c.Progress != nil {
			c.Progress(rep)
		}
		if int64(len(res.Results)) < c.PageSize {
			break
		}
	}
	var missing []string
	for id, e := range entries {
		if e.state == stateUnseen {
			rep.Missing++
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	rep.Samples.Missing = head(missing, opts.SampleLimit)
	rep.Samples.Extra = head(extra, opts.SampleLimit)

	// Pass 3: re-read the source for field level samples and repairs.
	needSamples := len(sampleIndexDocs) > 0
	needRepair := opts.repair() && (rep.Missing > 0 || rep.Mismatched > 0 || rep.Extra > 0)
	if needSamples || needRepair {
		var result RepairResult
		var pending []transform.Doc
		flush := func(ctx context.Context) error {
			if len(pending) == 0 || c.Repair == nil {
				return nil
			}
			dlq, err := c.Repair.ApplySync(ctx, nil, pending, nil)
			result.Upserted += len(pending) - len(dlq)
			result.DLQ += len(dlq)
			pending = pending[:0]
			return err
		}
		mismatches := map[string]Mismatch{}
		err := c.Source.Snapshot(ctx, func(ctx context.Context, evs []model.Event) error {
			mu.Lock()
			defer mu.Unlock()
			for _, ev := range evs {
				ch, err := c.Tr.Apply(ev)
				if err != nil || ch.Op != model.OpUpsert {
					continue
				}
				e := entries[ch.ID]
				if e == nil {
					continue
				}
				if idx, ok := sampleIndexDocs[ch.ID]; ok {
					doc, _ := canonical(ch.Doc)
					mismatches[ch.ID] = Mismatch{ID: ch.ID, Fields: fieldDiffs(doc, idx, *e.fields)}
				}
				if needRepair && ((e.state == stateUnseen && opts.RepairMissing) || (e.state == stateMismatch && opts.RepairMismatched)) {
					pending = append(pending, ch.Doc)
					if len(pending) >= 1000 {
						if err := flush(ctx); err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
		if err == nil {
			err = flush(ctx)
		}
		if err != nil {
			return fail(fmt.Errorf("repair/samples: %w", err))
		}
		ids := make([]string, 0, len(mismatches))
		for id := range mismatches {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			rep.Samples.Mismatched = append(rep.Samples.Mismatched, mismatches[id])
		}
		if needRepair && opts.RepairExtra && len(extra) > 0 && c.Repair != nil {
			for i := 0; i < len(extra); i += 1000 {
				chunk := extra[i:min(i+1000, len(extra))]
				dlq, err := c.Repair.ApplySync(ctx, nil, nil, chunk)
				result.Deleted += len(chunk) - len(dlq)
				result.DLQ += len(dlq)
				if err != nil {
					return fail(fmt.Errorf("delete extra documents: %w", err))
				}
			}
		}
		if needRepair {
			rep.Repaired = &result
		}
	}

	if rep.Samples.Mismatched == nil {
		rep.Samples.Mismatched = []Mismatch{}
	}
	rep.InSync = rep.Missing == 0 && rep.Extra == 0 && rep.Mismatched == 0
	rep.Status = StatusDone
	rep.FinishedAt = time.Now().UTC()
	return rep
}

func fieldDiffs(src, idx map[string]any, fields []string) []FieldDiff {
	var out []FieldDiff
	for _, f := range fields {
		if string(comparableJSON(src[f])) != string(comparableJSON(idx[f])) {
			out = append(out, FieldDiff{Field: f, Source: src[f], Index: idx[f]})
		}
	}
	return out
}

func idString(v any) string {
	switch t := v.(type) {
	case float64:
		if t == float64(int64(t)) {
			return fmt.Sprintf("%d", int64(t))
		}
	case string:
		return t
	}
	return fmt.Sprint(v)
}

func head(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	if s == nil {
		return []string{}
	}
	return s
}
