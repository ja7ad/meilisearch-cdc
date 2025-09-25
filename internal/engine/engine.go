// Package engine supervises CDC tasks: persistence, lifecycle, the
// snapshot and streaming pipeline, observability and data quality jobs.
package engine

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/diff"
	"github.com/ja7ad/meilisearch-cdc/internal/events"
	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/metrics"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Checkpoint is the persisted progress of a task.
type Checkpoint struct {
	// Position is the last source position confirmed in Meilisearch.
	Position model.Position `json:"position"`
	// StartPosition was captured before the snapshot; streaming resumes
	// from it once the snapshot is done.
	StartPosition model.Position `json:"startPosition,omitempty"`
	SnapshotDone  bool           `json:"snapshotDone"`
	// Resync requests a full rebuild on next start.
	Resync    bool      `json:"resync,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// TaskEvent notifies task list changes.
type TaskEvent struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// Point is a sample of a task's throughput.
type Point struct {
	T        int64   `json:"t"`
	Rate     float64 `json:"rate"`
	LagMs    int64   `json:"lagMs"`
	Upserted int64   `json:"upserted"`
	Deleted  int64   `json:"deleted"`
}

// Config configures the engine.
type Config struct {
	Destinations *dest.Registry
	DB           *store.DB
	Cipher       *secret.Cipher
	Metrics      *metrics.Metrics
	Logger       *slog.Logger
	// StopTimeout bounds how long a stopping task may drain.
	StopTimeout time.Duration
}

// Engine owns all tasks.
type Engine struct {
	cfg   Config
	log   *slog.Logger
	dests *dest.Registry

	tasks       *store.Bucket[model.Task]
	connectors  *store.Bucket[model.Connector]
	connTests   *store.Bucket[ConnectorTest]
	statuses    *store.Bucket[model.Status]
	checkpoints *store.Bucket[Checkpoint]
	dlq         *store.Bucket[model.DLQEntry]
	diffs       *store.Bucket[diff.Report]

	StatusBus *events.Bus[model.Status]
	LogBus    *events.Bus[model.LogEntry]
	TaskBus   *events.Bus[TaskEvent]
	// HealthBus carries every completed destination health check.
	HealthBus *events.Bus[DestinationHealthEvent]

	mu      sync.Mutex
	runners map[string]*runner
	logs    map[string]*events.Ring[model.LogEntry]
	series  map[string]*events.Ring[Point]
	status  map[string]*model.Status
	allLogs *events.Ring[model.LogEntry]
	diffRun map[string]context.CancelFunc

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Errors returned by the engine.
var (
	ErrNotFound = errors.New("task not found")
	ErrConflict = errors.New("conflict")
)

// New opens the buckets. Call Start to resume tasks.
func New(cfg Config) (*Engine, error) {
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Metrics == nil {
		cfg.Metrics = metrics.New()
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = 20 * time.Second
	}
	e := &Engine{
		cfg:       cfg,
		log:       cfg.Logger,
		dests:     cfg.Destinations,
		StatusBus: events.NewBus[model.Status](),
		LogBus:    events.NewBus[model.LogEntry](),
		TaskBus:   events.NewBus[TaskEvent](),
		HealthBus: events.NewBus[DestinationHealthEvent](),
		runners:   map[string]*runner{},
		logs:      map[string]*events.Ring[model.LogEntry]{},
		series:    map[string]*events.Ring[Point]{},
		status:    map[string]*model.Status{},
		allLogs:   events.NewRing[model.LogEntry](500),
		diffRun:   map[string]context.CancelFunc{},
	}
	if e.dests != nil {
		e.dests.OnHealth = func(id string, h dest.Health) {
			e.HealthBus.Publish(DestinationHealthEvent{ID: id, Health: h})
		}
	}
	var err error
	// Secrets are encrypted on write and stay encrypted in memory; they are
	// decrypted only when a connector opens its connection (Reveal).
	seal := func(t model.Task) (model.Task, error) {
		var err error
		if t.Source.DSN, err = cfg.Cipher.Rewrap(t.Source.DSN, aad(t.ID, "dsn")); err != nil {
			return t, err
		}
		if t.Source.SecretAccessKey, err = cfg.Cipher.Rewrap(t.Source.SecretAccessKey, aad(t.ID, "secretAccessKey")); err != nil {
			return t, err
		}
		t.Source.WebhookSecret, err = cfg.Cipher.Rewrap(t.Source.WebhookSecret, aad(t.ID, "webhookSecret"))
		return t, err
	}
	if e.tasks, err = store.NewBucket(cfg.DB, "tasks", store.WithHooks(seal, nil),
		store.WithValidation(func(t model.Task) error { return t.Validate() })); err != nil {
		return nil, err
	}
	if err := e.openConnectors(); err != nil {
		return nil, err
	}
	if e.statuses, err = store.NewBucket[model.Status](cfg.DB, "status"); err != nil {
		return nil, err
	}
	if e.checkpoints, err = store.NewBucket[Checkpoint](cfg.DB, "checkpoints"); err != nil {
		return nil, err
	}
	if e.dlq, err = store.NewBucket[model.DLQEntry](cfg.DB, "dlq"); err != nil {
		return nil, err
	}
	if e.diffs, err = store.NewBucket[diff.Report](cfg.DB, "diffs"); err != nil {
		return nil, err
	}
	return e, nil
}

func aad(taskID, field string) string { return "task:" + taskID + ":" + field }

// Reveal returns t with its secrets decrypted, for opening connections.
// Callers must not store or log the result.
func (e *Engine) Reveal(t model.Task) (model.Task, error) {
	var err error
	if t.Source.WebhookSecret, err = e.cfg.Cipher.Decrypt(t.Source.WebhookSecret, aad(t.ID, "webhookSecret")); err != nil {
		return t, fmt.Errorf("decrypt the webhook secret: %w", err)
	}
	if t.Source.ConnectorID != "" {
		c, err := e.RevealConnector(t.Source.ConnectorID)
		if err != nil {
			return t, err
		}
		t.Source = t.Source.WithConnector(c)
		return t, nil
	}
	if t.Source.DSN, err = e.cfg.Cipher.Decrypt(t.Source.DSN, aad(t.ID, "dsn")); err != nil {
		return t, fmt.Errorf("decrypt source credentials: %w", err)
	}
	if t.Source.SecretAccessKey, err = e.cfg.Cipher.Decrypt(t.Source.SecretAccessKey, aad(t.ID, "secretAccessKey")); err != nil {
		return t, fmt.Errorf("decrypt source credentials: %w", err)
	}
	return t, nil
}

// rewrapSecrets re-encrypts secrets stored with an old key or in plain text.
func (e *Engine) rewrapSecrets() {
	tasks, _ := e.tasks.List("", 0)
	n := 0
	for _, t := range tasks {
		if e.cfg.Cipher.NeedsRewrap(t.Source.DSN) || e.cfg.Cipher.NeedsRewrap(t.Source.SecretAccessKey) || e.cfg.Cipher.NeedsRewrap(t.Source.WebhookSecret) {
			if err := e.tasks.Put(t.ID, t); err != nil {
				e.log.Error("re-encrypt task secrets", "task", t.ID, "err", err)
				continue
			}
			n++
		}
	}
	conns, _ := e.connectors.List("", 0)
	for _, c := range conns {
		if e.cfg.Cipher.NeedsRewrap(c.Config.DSN) || e.cfg.Cipher.NeedsRewrap(c.Config.SecretAccessKey) {
			if err := e.connectors.Put(c.ID, c); err != nil {
				e.log.Error("re-encrypt connector secrets", "connector", c.ID, "err", err)
				continue
			}
			n++
		}
	}
	if n > 0 {
		e.log.Info("re-encrypted stored secrets with the current key", "records", n)
	}
}

// Destinations returns the destination registry.
func (e *Engine) Destinations() *dest.Registry { return e.dests }

// Target returns a destination client ("" = default).
func (e *Engine) Target(id string) (*dest.Target, error) { return e.dests.Target(id) }

// target returns the destination a task writes to.
func (e *Engine) target(t model.Task) (*dest.Target, error) {
	return e.dests.Target(t.Index.DestinationID)
}

// resolveDestination pins a task to a concrete destination id.
func (e *Engine) resolveDestination(t *model.Task) error {
	id, err := e.dests.Resolve(t.Index.DestinationID)
	if err != nil {
		return err
	}
	t.Index.DestinationID = id
	return nil
}

// Metrics returns the metrics.
func (e *Engine) Metrics() *metrics.Metrics { return e.cfg.Metrics }

// Start restores statuses and starts tasks whose desired state is running.
func (e *Engine) Start(ctx context.Context) error {
	e.ctx, e.cancel = context.WithCancel(context.WithoutCancel(ctx))
	e.rewrapSecrets()
	tasks, err := e.tasks.List("", 0)
	if err != nil {
		return err
	}
	for _, t := range tasks {
		st, ok, _ := e.statuses.Get(t.ID)
		if !ok {
			st = model.Status{TaskID: t.ID}
		}
		if st.State.Active() || st.State == "" {
			st.State = model.StateStopped
		}
		if t.Desired == model.StatePaused {
			st.State = model.StatePaused
		}
		st.Inflight, st.Rate = 0, 0
		e.mu.Lock()
		e.status[t.ID] = &st
		e.mu.Unlock()
		if t.Desired == model.StateRunning {
			e.spawn(t)
		}
	}
	e.wg.Add(1)
	go e.sampler()
	return nil
}

// Shutdown stops every runner gracefully and persists statuses.
func (e *Engine) Shutdown(ctx context.Context) {
	e.mu.Lock()
	rs := make([]*runner, 0, len(e.runners))
	for _, r := range e.runners {
		rs = append(rs, r)
	}
	e.mu.Unlock()
	var wg sync.WaitGroup
	for _, r := range rs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.stop(ctx)
		}()
	}
	wg.Wait()
	if e.cancel != nil {
		e.cancel()
	}
	e.wg.Wait()
	e.persistStatuses()
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// List returns every task with its status, optionally filtered.
func (e *Engine) List(state, q, tag string) ([]model.TaskView, error) {
	tasks, err := e.tasks.List("", 0)
	if err != nil {
		return nil, err
	}
	q = strings.ToLower(q)
	out := make([]model.TaskView, 0, len(tasks))
	for _, t := range tasks {
		st := e.Status(t.ID)
		if state != "" && string(st.State) != state {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(t.Name+" "+t.ID+" "+t.Index.UID+" "+t.Source.Table), q) {
			continue
		}
		if tag != "" && !contains(t.Tags, tag) {
			continue
		}
		out = append(out, model.TaskView{Task: t.Redacted(), Status: st})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Get returns the task with secrets.
func (e *Engine) Get(id string) (model.Task, error) {
	t, ok, err := e.tasks.Get(id)
	if err != nil {
		return t, err
	}
	if !ok {
		return t, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return t, nil
}

// View returns the redacted task and its status. The DSN shows host and
// user (the password is masked) so it can be edited.
func (e *Engine) View(id string) (model.TaskView, error) {
	t, err := e.Get(id)
	if err != nil {
		return model.TaskView{}, err
	}
	if plain, err := e.Reveal(t); err == nil {
		t = plain
	}
	return model.TaskView{Task: t.Redacted(), Status: e.Status(id)}, nil
}

// Status returns a copy of the task status.
func (e *Engine) Status(id string) model.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	if st, ok := e.status[id]; ok {
		return *st
	}
	return model.Status{TaskID: id, State: model.StateStopped}
}

// checkIndexOwner refuses two tasks writing the same index with different keys.
func (e *Engine) checkIndexOwner(t model.Task) error {
	tasks, err := e.tasks.List("", 0)
	if err != nil {
		return err
	}
	for _, o := range tasks {
		if o.ID != t.ID && o.Index.UID == t.Index.UID && o.Index.DestinationID == t.Index.DestinationID && o.Mapping.PrimaryKeyField != t.Mapping.PrimaryKeyField {
			return fmt.Errorf("%w: task %q already writes index %q with primary key %q", ErrConflict, o.Name, o.Index.UID, o.Mapping.PrimaryKeyField)
		}
	}
	return nil
}

// Create validates and stores a new task.
func (e *Engine) Create(t model.Task, start bool) (model.TaskView, error) {
	if t.ID == "" {
		t.ID = newID()
	}
	if _, ok, _ := e.tasks.Get(t.ID); ok {
		return model.TaskView{}, fmt.Errorf("%w: task %s already exists", ErrConflict, t.ID)
	}
	if err := e.resolveConnector(&t); err != nil {
		return model.TaskView{}, err
	}
	if err := e.resolveDestination(&t); err != nil {
		return model.TaskView{}, err
	}
	t.ApplyDefaults()
	t.Desired = model.StateStopped
	if start {
		t.Desired = model.StateRunning
	}
	now := time.Now().UTC()
	t.CreatedAt, t.UpdatedAt = now, now
	if err := t.Validate(); err != nil {
		return model.TaskView{}, err
	}
	if _, err := indexmgr.ParseSettings(t.Index.Settings); err != nil {
		return model.TaskView{}, err
	}
	if err := e.checkIndexOwner(t); err != nil {
		return model.TaskView{}, err
	}
	if err := e.tasks.Put(t.ID, t); err != nil {
		return model.TaskView{}, err
	}
	e.mu.Lock()
	e.status[t.ID] = &model.Status{TaskID: t.ID, State: model.StateStopped, UpdatedAt: now}
	e.mu.Unlock()
	e.logf(t.ID, "info", "task created")
	e.TaskBus.Publish(TaskEvent{ID: t.ID, Action: "created"})
	if start {
		e.spawn(t)
	}
	return e.View(t.ID)
}

// Update replaces a task definition, restarting it when running. Masked
// secrets keep their stored value.
func (e *Engine) Update(ctx context.Context, id string, t model.Task) (model.TaskView, error) {
	prev, err := e.Get(id)
	if err != nil {
		return model.TaskView{}, err
	}
	t.ID = id
	plainPrev, err := e.Reveal(prev)
	if err != nil {
		return model.TaskView{}, err
	}
	if t.Source.WebhookSecret == model.SecretMask {
		t.Source.WebhookSecret = plainPrev.Source.WebhookSecret // task level, also with a connector
	}
	if t.Source.ConnectorID == "" {
		t.RestoreSecrets(plainPrev)
	} else if err := e.resolveConnector(&t); err != nil {
		return model.TaskView{}, err
	}
	if err := e.resolveDestination(&t); err != nil {
		return model.TaskView{}, err
	}
	t.ApplyDefaults()
	t.Desired = prev.Desired
	t.CreatedAt, t.UpdatedAt = prev.CreatedAt, time.Now().UTC()
	if err := t.Validate(); err != nil {
		return model.TaskView{}, err
	}
	if _, err := indexmgr.ParseSettings(t.Index.Settings); err != nil {
		return model.TaskView{}, err
	}
	if err := e.checkIndexOwner(t); err != nil {
		return model.TaskView{}, err
	}
	sourceChanged := prev.Source.Type != t.Source.Type || prev.Source.Table != t.Source.Table || prev.Source.ConnectorID != t.Source.ConnectorID ||
		prev.Source.Schema != t.Source.Schema || prev.Source.Database != t.Source.Database
	wasRunning := e.running(id)
	if wasRunning {
		e.stopRunner(ctx, id)
	}
	if err := e.tasks.Put(id, t); err != nil {
		return model.TaskView{}, err
	}
	if sourceChanged || prev.Index.UID != t.Index.UID || prev.Index.DestinationID != t.Index.DestinationID {
		// A different table or index invalidates the checkpoint.
		_ = e.checkpoints.Delete(id)
		e.logf(id, "warn", "source or index changed: checkpoint cleared, the next start runs a full snapshot")
	}
	e.logf(id, "info", "task updated")
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "updated"})
	if wasRunning {
		e.spawn(t) // the runner applies managed settings on start
	} else if t.Index.ManageSettings && len(t.Index.Settings) > 0 && string(t.Index.Settings) != string(prev.Index.Settings) {
		// Keep the index in step with the definition even while stopped.
		if _, err := e.ApplyTaskSettings(ctx, id); err != nil && !indexmgr.IsNotFound(err) {
			e.logf(id, "warn", "could not apply index settings: %v", err)
		}
	}
	return e.View(id)
}

// Delete stops and removes a task. dropSource releases server side source
// resources (replication slots); deleteIndex must equal the index uid to
// delete the index too.
func (e *Engine) Delete(ctx context.Context, id string, dropSource bool, deleteIndex string) error {
	t, err := e.Get(id)
	if err != nil {
		return err
	}
	e.stopRunner(ctx, id)
	if dropSource {
		if err := e.dropSource(ctx, t); err != nil {
			e.logf(id, "warn", "could not drop source resources: %v", err)
		}
	}
	if deleteIndex != "" {
		if deleteIndex != t.Index.UID {
			return fmt.Errorf("%w: deleteIndex must equal %q", ErrConflict, t.Index.UID)
		}
		tg, err := e.target(t)
		if err != nil {
			return err
		}
		if err := tg.Indexes.Drop(ctx, t.Index.UID); err != nil {
			return err
		}
	}
	for _, del := range []func(string) error{e.tasks.Delete, e.statuses.Delete, e.checkpoints.Delete, e.diffs.Delete} {
		if err := del(id); err != nil {
			return err
		}
	}
	if _, err := e.dlq.DeletePrefix(id + "/"); err != nil {
		return err
	}
	e.mu.Lock()
	delete(e.status, id)
	delete(e.logs, id)
	delete(e.series, id)
	e.mu.Unlock()
	e.cfg.Metrics.Forget(id)
	e.TaskBus.Publish(TaskEvent{ID: id, Action: "deleted"})
	return nil
}

func (e *Engine) dropSource(ctx context.Context, t model.Task) error {
	t, err := e.Reveal(t)
	if err != nil {
		return err
	}
	src, err := source.New(t.Source, t.ID)
	if err != nil {
		return err
	}
	d, ok := src.(source.Dropper)
	if !ok {
		return nil
	}
	if err := src.Open(ctx); err != nil {
		return err
	}
	defer src.Close()
	return d.Drop(ctx)
}

func (e *Engine) setDesired(id string, s model.State) (model.Task, error) {
	var out model.Task
	err := e.tasks.Update(id, func(t *model.Task, exists bool) error {
		if !exists {
			return fmt.Errorf("%w: %s", ErrNotFound, id)
		}
		t.Desired = s
		out = *t
		return nil
	})
	return out, err
}

// Action runs a lifecycle action.
func (e *Engine) Action(ctx context.Context, id, action string) (model.TaskView, error) {
	var err error
	switch action {
	case "start", "resume":
		err = e.StartTask(id)
	case "stop":
		err = e.StopTask(ctx, id, model.StateStopped)
	case "pause":
		err = e.StopTask(ctx, id, model.StatePaused)
	case "restart":
		if err = e.StopTask(ctx, id, model.StateStopped); err == nil {
			err = e.StartTask(id)
		}
	case "resync":
		err = e.Resync(ctx, id)
	case "reset-checkpoint":
		err = e.ResetCheckpoint(ctx, id)
	default:
		return model.TaskView{}, fmt.Errorf("unknown action %q", action)
	}
	if err != nil {
		return model.TaskView{}, err
	}
	return e.View(id)
}

// StartTask starts (or resumes) a task.
func (e *Engine) StartTask(id string) error {
	t, err := e.setDesired(id, model.StateRunning)
	if err != nil {
		return err
	}
	if !e.running(id) {
		e.spawn(t)
	}
	return nil
}

// StopTask stops a task; final is stopped or paused.
func (e *Engine) StopTask(ctx context.Context, id string, final model.State) error {
	if _, err := e.setDesired(id, final); err != nil {
		return err
	}
	e.stopRunner(ctx, id)
	e.setState(id, final, "", nil)
	e.logf(id, "info", "task %s", final)
	return nil
}

// Resync rebuilds the index from a fresh snapshot.
func (e *Engine) Resync(ctx context.Context, id string) error {
	t, err := e.Get(id)
	if err != nil {
		return err
	}
	if t.Sync.Mode == model.ModeStream {
		return fmt.Errorf("%w: resync needs a snapshot; the task runs in stream only mode", ErrConflict)
	}
	if t.Source.Type == model.SourceWebhook {
		return fmt.Errorf("%w: webhook tasks have no source table to copy again; push the documents instead", ErrConflict)
	}
	e.stopRunner(ctx, id)
	if err := e.checkpoints.Update(id, func(cp *Checkpoint, _ bool) error {
		cp.Resync = true
		cp.StartPosition = "" // captured again by the new snapshot
		cp.UpdatedAt = time.Now().UTC()
		return nil
	}); err != nil {
		return err
	}
	e.logf(id, "info", "resync requested")
	return e.StartTask(id)
}

// ResetCheckpoint forgets progress; the next start snapshots again.
func (e *Engine) ResetCheckpoint(ctx context.Context, id string) error {
	t, err := e.Get(id)
	if err != nil {
		return err
	}
	running := e.running(id)
	e.stopRunner(ctx, id)
	if err := e.checkpoints.Delete(id); err != nil {
		return err
	}
	e.logf(id, "warn", "checkpoint reset")
	if running {
		e.spawn(t)
	}
	return nil
}

// Checkpoint returns the stored checkpoint.
func (e *Engine) Checkpoint(id string) (Checkpoint, error) {
	cp, _, err := e.checkpoints.Get(id)
	return cp, err
}

// BatchResult is the outcome of one task in a batch action.
type BatchResult struct {
	ID    string `json:"id"`
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// Batch runs an action on several tasks concurrently.
func (e *Engine) Batch(ctx context.Context, ids []string, action string) []BatchResult {
	out := make([]BatchResult, len(ids))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for i, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			var err error
			if action == "delete" {
				err = e.Delete(ctx, id, false, "")
			} else {
				_, err = e.Action(ctx, id, action)
			}
			out[i] = BatchResult{ID: id, OK: err == nil}
			if err != nil {
				out[i].Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return out
}

// ManagedBy returns the ids of tasks writing to an index of a destination.
func (e *Engine) ManagedBy(destID, uid string) []string {
	destID, _ = e.dests.Resolve(destID)
	tasks, _ := e.tasks.List("", 0)
	var out []string
	for _, t := range tasks {
		if t.Index.UID == uid && t.Index.DestinationID == destID {
			out = append(out, t.ID)
		}
	}
	return out
}

// IndexBusy reports whether a running task writes to the index.
func (e *Engine) IndexBusy(destID, uid string) bool {
	for _, id := range e.ManagedBy(destID, uid) {
		if e.running(id) {
			return true
		}
	}
	return false
}

func (e *Engine) running(id string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	_, ok := e.runners[id]
	return ok
}

func (e *Engine) spawn(t model.Task) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.runners[t.ID]; ok {
		return
	}
	base := e.ctx
	if base == nil {
		base = context.Background()
	}
	r := newRunner(e, t, base)
	e.runners[t.ID] = r
	if _, ok := e.status[t.ID]; !ok {
		e.status[t.ID] = &model.Status{TaskID: t.ID}
	}
	e.wg.Add(1)
	go func() {
		defer e.wg.Done()
		r.loop()
		e.mu.Lock()
		if e.runners[t.ID] == r {
			delete(e.runners, t.ID)
		}
		e.mu.Unlock()
		e.cfg.Metrics.Running.WithLabelValues(t.ID).Set(0)
	}()
}

func (e *Engine) stopRunner(ctx context.Context, id string) {
	e.mu.Lock()
	r := e.runners[id]
	e.mu.Unlock()
	if r != nil {
		r.stop(ctx)
	}
}
