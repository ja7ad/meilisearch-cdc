package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/diff"
	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/retry"
	"github.com/ja7ad/meilisearch-cdc/internal/sink"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// dlqKey sorts entries of a task by time.
func dlqKey(taskID string, at time.Time) string {
	return fmt.Sprintf("%s/%020d-%s", taskID, at.UnixNano(), newID())
}

func (e *Engine) addDLQ(taskID string, entries []model.DLQEntry) {
	batch := make(map[string]model.DLQEntry, len(entries))
	for _, en := range entries {
		if en.CreatedAt.IsZero() {
			en.CreatedAt = time.Now().UTC()
		}
		en.TaskID = taskID
		en.ID = dlqKey(taskID, en.CreatedAt)
		batch[en.ID] = en
	}
	if err := e.dlq.PutMany(batch); err != nil {
		e.logf(taskID, "error", "store dead letters: %v", err)
		return
	}
	e.cfg.Metrics.DLQ.WithLabelValues(taskID).Add(float64(len(entries)))
	e.mutate(taskID, func(st *model.Status) { st.Counters.DLQ += int64(len(entries)) })
}

// DLQ lists dead letters of a task, newest last.
func (e *Engine) DLQ(taskID string, limit int) ([]model.DLQEntry, error) {
	if _, err := e.Get(taskID); err != nil {
		return nil, err
	}
	out, err := e.dlq.List(taskID+"/", limit)
	if out == nil {
		out = []model.DLQEntry{}
	}
	return out, err
}

func (e *Engine) selectDLQ(taskID string, ids []string) ([]model.DLQEntry, error) {
	if len(ids) == 0 {
		return e.dlq.List(taskID+"/", 0)
	}
	out := make([]model.DLQEntry, 0, len(ids))
	for _, id := range ids {
		if en, ok, err := e.dlq.Get(id); err != nil {
			return nil, err
		} else if ok && en.TaskID == taskID {
			out = append(out, en)
		}
	}
	return out, nil
}

// DiscardDLQ deletes dead letters (all when ids is empty).
func (e *Engine) DiscardDLQ(taskID string, ids []string) (int, error) {
	if len(ids) == 0 {
		return e.dlq.DeletePrefix(taskID + "/")
	}
	entries, err := e.selectDLQ(taskID, ids)
	if err != nil {
		return 0, err
	}
	for _, en := range entries {
		if err := e.dlq.Delete(en.ID); err != nil {
			return 0, err
		}
	}
	return len(entries), nil
}

func (e *Engine) applier(t model.Task, pkField string) (*sink.Applier, error) {
	tg, err := e.target(t)
	if err != nil {
		return nil, err
	}
	return sink.NewApplier(tg.Client.Index(t.Index.UID), tg.Client, sink.Config{
		TaskID: t.ID, PrimaryKey: pkField, WriteMode: t.Index.WriteMode,
		Retry: retry.Policy{MaxAttempts: 3, Initial: 200 * time.Millisecond, Max: 2 * time.Second},
	}), nil
}

// ReplayDLQ resends dead letters; successes are removed, failures stay
// with an increased attempt count.
func (e *Engine) ReplayDLQ(ctx context.Context, taskID string, ids []string) (replayed, failed int, err error) {
	t, err := e.Get(taskID)
	if err != nil {
		return 0, 0, err
	}
	entries, err := e.selectDLQ(taskID, ids)
	if err != nil {
		return 0, 0, err
	}
	pk := t.Mapping.PrimaryKeyField
	ap, err := e.applier(t, pk)
	if err != nil {
		return 0, 0, err
	}
	for _, en := range entries {
		if en.Op == "transform" {
			failed++
			continue // the row itself is broken; fix the source or the mapping
		}
		var dlq []model.DLQEntry
		var aerr error
		if en.Op == "delete" {
			dlq, aerr = ap.ApplySync(ctx, nil, nil, []string{en.DocID})
		} else {
			dlq, aerr = ap.ApplySync(ctx, nil, []transform.Doc{en.Document}, nil)
		}
		if aerr != nil {
			return replayed, failed, aerr
		}
		if len(dlq) > 0 {
			failed++
			_, _ = e.dlq.UpdateExisting(en.ID, func(v *model.DLQEntry) error {
				v.Attempts++
				v.Error, v.Code = dlq[0].Error, dlq[0].Code
				return nil
			})
			continue
		}
		replayed++
		_ = e.dlq.Delete(en.ID)
	}
	e.logf(taskID, "info", "dead letter replay: %d replayed, %d still failing", replayed, failed)
	return replayed, failed, nil
}

// openSource opens a fresh connector and transformer for ad-hoc jobs.
func (e *Engine) openSource(ctx context.Context, t model.Task) (source.Source, *model.Schema, *transform.Transformer, error) {
	t, err := e.Reveal(t)
	if err != nil {
		return nil, nil, nil, err
	}
	src, err := source.New(t.Source, t.ID)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := src.Open(ctx); err != nil {
		src.Close()
		return nil, nil, nil, err
	}
	schema, err := src.Schema(ctx)
	if err != nil {
		src.Close()
		return nil, nil, nil, err
	}
	tr, err := transform.New(t.Mapping, schema.PrimaryKey)
	if err != nil {
		src.Close()
		return nil, nil, nil, err
	}
	return src, schema, tr, nil
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK         bool             `json:"ok"`
	Error      string           `json:"error,omitempty"`
	Schema     *model.Schema    `json:"schema,omitempty"`
	Sample     []map[string]any `json:"sample"`
	PrimaryKey string           `json:"primaryKey,omitempty"`
}

var errEnough = errors.New("enough rows")

// TestConnection connects with an unsaved task definition, reads the
// schema and returns a few transformed documents.
func (e *Engine) TestConnection(ctx context.Context, t model.Task) TestResult {
	if err := e.resolveConnector(&t); err != nil {
		return TestResult{Sample: []map[string]any{}, Error: err.Error()}
	}
	if t.ID == "" {
		t.ID = "test-" + newID()
	} else if prev, err := e.Get(t.ID); err == nil && t.Source.ConnectorID == "" {
		if plain, err := e.Reveal(prev); err == nil {
			t.RestoreSecrets(plain)
		}
	}
	t.ApplyDefaults()
	res := TestResult{Sample: []map[string]any{}}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	src, schema, tr, err := e.openSource(ctx, t)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	defer src.Close()
	res.Schema = schema
	res.PrimaryKey = tr.PrimaryKeyField()
	err = src.Snapshot(ctx, func(_ context.Context, evs []model.Event) error {
		for _, ev := range evs {
			c, err := tr.Apply(ev)
			if err != nil {
				return err
			}
			if c.Doc != nil {
				res.Sample = append(res.Sample, c.Doc)
			}
			if len(res.Sample) >= 5 {
				return errEnough
			}
		}
		return nil
	})
	if err != nil && !errors.Is(err, errEnough) {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}

// SchemaCheck runs the column checker.
func (e *Engine) SchemaCheck(ctx context.Context, taskID string) (diff.SchemaReport, error) {
	t, err := e.Get(taskID)
	if err != nil {
		return diff.SchemaReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	src, schema, tr, err := e.openSource(ctx, t)
	if err != nil {
		return diff.SchemaReport{}, err
	}
	defer src.Close()
	in := diff.SchemaInput{Task: t, Schema: schema, Tr: tr}
	tg, err := e.target(t)
	if err != nil {
		return diff.SchemaReport{}, err
	}
	if ix, err := tg.Client.GetIndex(ctx, t.Index.UID); err == nil {
		in.IndexPK = ix.PrimaryKey
		idx := tg.Client.Index(t.Index.UID)
		if in.Stats, err = idx.GetStats(ctx, nil); err != nil {
			return diff.SchemaReport{}, err
		}
		raw, err := tg.Indexes.LiveSettings(ctx, t.Index.UID)
		if err != nil {
			return diff.SchemaReport{}, err
		}
		in.Settings = settingsForCheck(raw)
	} else if !indexmgr.IsNotFound(err) {
		return diff.SchemaReport{}, err
	}
	return diff.CheckSchema(in), nil
}

// StartDiff launches a diff (and optional repair) in the background.
func (e *Engine) StartDiff(taskID string, opts diff.Options) error {
	t, err := e.Get(taskID)
	if err != nil {
		return err
	}
	e.mu.Lock()
	if _, busy := e.diffRun[taskID]; busy {
		e.mu.Unlock()
		return fmt.Errorf("%w: a diff is already running", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Hour)
	e.diffRun[taskID] = cancel
	e.mu.Unlock()

	running := diff.Report{TaskID: taskID, IndexUID: t.Index.UID, Status: diff.StatusRunning, StartedAt: time.Now().UTC()}
	_ = e.diffs.Put(taskID, running)
	e.logf(taskID, "info", "diff started")
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		defer func() {
			cancel()
			e.mu.Lock()
			delete(e.diffRun, taskID)
			e.mu.Unlock()
		}()
		rep := e.runDiff(ctx, t, opts)
		_ = e.diffs.Put(taskID, rep)
		switch {
		case rep.Status == diff.StatusFailed:
			e.logf(taskID, "error", "diff failed: %s", rep.Error)
		case rep.InSync:
			e.logf(taskID, "info", "diff finished: %d documents in sync", rep.SourceCount)
		default:
			e.logf(taskID, "warn", "diff finished: %d missing, %d extra, %d mismatched", rep.Missing, rep.Extra, rep.Mismatched)
		}
		if rep.Repaired != nil {
			e.logf(taskID, "info", "repair: %d upserted, %d deleted, %d rejected", rep.Repaired.Upserted, rep.Repaired.Deleted, rep.Repaired.DLQ)
		}
		e.TaskBus.Publish(TaskEvent{ID: taskID, Action: "diff"})
	}()
	return nil
}

func (e *Engine) runDiff(ctx context.Context, t model.Task, opts diff.Options) diff.Report {
	src, _, tr, err := e.openSource(ctx, t)
	if err != nil {
		return diff.Report{TaskID: t.ID, IndexUID: t.Index.UID, Status: diff.StatusFailed, Error: err.Error(), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	}
	defer src.Close()
	ap, err := e.applier(t, tr.PrimaryKeyField())
	if err != nil {
		return diff.Report{TaskID: t.ID, IndexUID: t.Index.UID, Status: diff.StatusFailed, Error: err.Error(), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	}
	tg, err := e.target(t)
	if err != nil {
		return diff.Report{TaskID: t.ID, IndexUID: t.Index.UID, Status: diff.StatusFailed, Error: err.Error(), StartedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()}
	}
	c := &diff.Checker{
		TaskID: t.ID, IndexUID: t.Index.UID, Source: src, Tr: tr,
		Index:  tg.Client.Index(t.Index.UID),
		Repair: ap,
		Progress: func(r diff.Report) {
			_ = e.diffs.Put(t.ID, r)
		},
	}
	return c.Run(ctx, opts)
}

// DiffReport returns the last diff report.
func (e *Engine) DiffReport(taskID string) (diff.Report, bool, error) {
	if _, err := e.Get(taskID); err != nil {
		return diff.Report{}, false, err
	}
	return e.diffs.Get(taskID)
}

// Export returns every task definition with secrets redacted.
func (e *Engine) Export() ([]model.Task, error) {
	tasks, err := e.tasks.List("", 0)
	for i := range tasks {
		tasks[i] = tasks[i].Redacted()
	}
	if tasks == nil {
		tasks = []model.Task{}
	}
	return tasks, err
}

// ImportResult summarises an import.
type ImportResult struct {
	Created int      `json:"created"`
	Errors  []string `json:"errors"`
}

// Import creates tasks in bulk (stopped).
func (e *Engine) Import(tasks []model.Task) ImportResult {
	res := ImportResult{Errors: []string{}}
	for _, t := range tasks {
		if _, err := e.Create(t, false); err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", t.Name, err))
			continue
		}
		res.Created++
	}
	return res
}

// Declare creates or updates a task from a declarative definition and
// applies its desired state.
func (e *Engine) Declare(ctx context.Context, t model.Task) error {
	desired := t.Desired
	if _, ok, err := e.tasks.Get(t.ID); err != nil {
		return err
	} else if !ok {
		_, err := e.Create(t, desired == model.StateRunning)
		return err
	}
	if _, err := e.Update(ctx, t.ID, t); err != nil {
		return err
	}
	switch desired {
	case model.StateRunning:
		return e.StartTask(t.ID)
	case model.StateStopped, model.StatePaused:
		return e.StopTask(ctx, t.ID, desired)
	}
	return nil
}

// settingsForCheck reads the attribute lists the schema check needs from
// raw settings, flattening granular filterable attributes to their patterns.
func settingsForCheck(raw json.RawMessage) *meilisearch.Settings {
	var v struct {
		RankingRules         []string `json:"rankingRules"`
		DistinctAttribute    *string  `json:"distinctAttribute"`
		SearchableAttributes []string `json:"searchableAttributes"`
		DisplayedAttributes  []string `json:"displayedAttributes"`
		SortableAttributes   []string `json:"sortableAttributes"`
		FilterableAttributes []any    `json:"filterableAttributes"`
	}
	_ = json.Unmarshal(raw, &v)
	s := &meilisearch.Settings{RankingRules: v.RankingRules, DistinctAttribute: v.DistinctAttribute,
		SearchableAttributes: v.SearchableAttributes, DisplayedAttributes: v.DisplayedAttributes, SortableAttributes: v.SortableAttributes}
	for _, f := range v.FilterableAttributes {
		switch x := f.(type) {
		case string:
			s.FilterableAttributes = append(s.FilterableAttributes, x)
		case map[string]any:
			if pats, ok := x["attributePatterns"].([]any); ok {
				for _, p := range pats {
					if ps, ok := p.(string); ok {
						s.FilterableAttributes = append(s.FilterableAttributes, ps)
					}
				}
			}
		}
	}
	return s
}

// TaskSettings compares a task's desired index settings with the live index.
type TaskSettings struct {
	IndexUID       string            `json:"indexUid"`
	ManageSettings bool              `json:"manageSettings"`
	Desired        json.RawMessage   `json:"desired"`
	Live           json.RawMessage   `json:"live"`
	IndexExists    bool              `json:"indexExists"`
	Changes        []indexmgr.Change `json:"changes"`
	InSync         bool              `json:"inSync"`
}

// TaskSettings returns desired vs live settings with drift.
func (e *Engine) TaskSettings(ctx context.Context, id string) (TaskSettings, error) {
	t, err := e.Get(id)
	if err != nil {
		return TaskSettings{}, err
	}
	out := TaskSettings{IndexUID: t.Index.UID, ManageSettings: t.Index.ManageSettings, Desired: t.Index.Settings, Changes: []indexmgr.Change{}}
	if len(out.Desired) == 0 {
		out.Desired = json.RawMessage("{}")
	}
	tg, err := e.target(t)
	if err != nil {
		return out, err
	}
	live, err := tg.Indexes.LiveSettings(ctx, t.Index.UID)
	if indexmgr.IsNotFound(err) {
		out.Live = json.RawMessage("null")
		out.InSync = len(t.Index.Settings) == 0
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Live, out.IndexExists = live, true
	changes, err := indexmgr.DiffSettings(live, out.Desired)
	if err != nil {
		return out, err
	}
	if changes != nil {
		out.Changes = changes
	}
	out.InSync = len(out.Changes) == 0
	return out, nil
}

// ApplyTaskSettings applies a task's desired settings to its index now.
func (e *Engine) ApplyTaskSettings(ctx context.Context, id string) (TaskSettings, error) {
	t, err := e.Get(id)
	if err != nil {
		return TaskSettings{}, err
	}
	if len(t.Index.Settings) == 0 {
		return TaskSettings{}, fmt.Errorf("%w: the task defines no index settings", ErrConflict)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	tg, err := e.target(t)
	if err != nil {
		return TaskSettings{}, err
	}
	if _, err := tg.Indexes.ApplySettings(ctx, t.Index.UID, t.Index.Settings); err != nil {
		return TaskSettings{}, err
	}
	e.logf(id, "info", "index settings applied to %s", t.Index.UID)
	return e.TaskSettings(ctx, id)
}
