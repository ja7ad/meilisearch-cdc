// Package dest manages the Meilisearch instances tasks write to: their
// stored settings, SDK clients and health.
package dest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Errors.
var (
	ErrNotFound  = errors.New("destination not found")
	ErrNoDefault = errors.New("no Meilisearch destination is configured: add one first")
	ErrConflict  = errors.New("conflict")
	// ErrSingleInstance refuses destination changes while MEILI_URL pins
	// the service to one Meilisearch instance.
	ErrSingleInstance = errors.New("single instance mode: MEILI_URL is set, so the service writes only to that Meilisearch instance; unset it to manage destinations in the dashboard")
)

// Target is a ready to use destination.
type Target struct {
	ID      string
	Name    string
	Client  meilisearch.ServiceManager
	Indexes *indexmgr.Manager
}

// Health is a destination's state from its last check.
type Health struct {
	Healthy      bool   `json:"healthy"`
	Version      string `json:"version,omitempty"`
	Indexes      int    `json:"indexes"`
	Documents    int64  `json:"documents"`
	DatabaseSize any    `json:"databaseSize,omitempty"`
	Error        string `json:"error,omitempty"`
	LatencyMs    int64  `json:"latencyMs"`
	// ConsecutiveFailures counts failed checks since the last healthy one.
	ConsecutiveFailures int       `json:"consecutiveFailures"`
	LastHealthyAt       time.Time `json:"lastHealthyAt,omitzero"`
	CheckedAt           time.Time `json:"checkedAt"`
	NextCheckAt         time.Time `json:"nextCheckAt"`
}

// Registry stores destinations and caches their clients.
type Registry struct {
	// Single is set when MEILI_URL is configured: only the environment
	// destination exists for the service.
	Single bool

	b      *store.Bucket[model.Destination]
	cipher *secret.Cipher
	log    *slog.Logger

	// OnHealth is called after every completed health check.
	OnHealth func(id string, h Health)

	mu      sync.Mutex
	targets map[string]*Target
	health  map[string]Health
	probing map[string]chan struct{} // closed when the running check ends
}

func aad(id string) string { return "destination:" + id + ":apiKey" }

// New opens the destination bucket.
func New(db *store.DB, c *secret.Cipher, log *slog.Logger) (*Registry, error) {
	r := &Registry{cipher: c, log: log, targets: map[string]*Target{}, health: map[string]Health{},
		probing: map[string]chan struct{}{}}
	seal := func(d model.Destination) (model.Destination, error) {
		var err error
		d.APIKey, err = c.Rewrap(d.APIKey, aad(d.ID))
		return d, err
	}
	var err error
	r.b, err = store.NewBucket(db, "destinations", store.WithHooks(seal, nil))
	return r, err
}

// NewClient builds an SDK client tuned for bulk writes: pooled
// connections, gzip bodies and retries left to the engine.
func NewClient(url, key string, gzip bool, timeout time.Duration) meilisearch.ServiceManager {
	opts := []meilisearch.Option{
		meilisearch.WithAPIKey(key),
		meilisearch.WithCustomClient(&http.Client{Timeout: timeout, Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment, MaxIdleConns: 256, MaxIdleConnsPerHost: 64, IdleConnTimeout: 90 * time.Second,
			TLSHandshakeTimeout: 10 * time.Second, ForceAttemptHTTP2: true,
		}}),
		meilisearch.WithCustomRetries([]int{502, 503, 504}, 2),
	}
	if gzip {
		opts = append(opts, meilisearch.WithContentEncoding(meilisearch.GzipEncoding, meilisearch.BestSpeed))
	}
	return meilisearch.New(url, opts...)
}

// rawGetter reads Meilisearch JSON directly (exact settings).
func rawGetter(url, key string, timeout time.Duration) func(ctx context.Context, path string) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	base := strings.TrimRight(url, "/")
	return func(ctx context.Context, path string) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
		if err != nil {
			return nil, err
		}
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			me := &meilisearch.Error{Endpoint: path, Method: http.MethodGet, StatusCode: resp.StatusCode}
			me.ErrorBody(body)
			if me.APIError.Message == "" {
				me.APIError.Message = strings.TrimSpace(string(body))
			}
			return nil, me
		}
		return body, nil
	}
}

// List returns every destination (API keys encrypted), default first.
func (r *Registry) List() ([]model.Destination, error) {
	out, err := r.b.List("", 0)
	if r.Single {
		out = slices.DeleteFunc(out, func(d model.Destination) bool { return d.ID != model.DefaultDestinationID })
	}
	slices.SortFunc(out, func(a, b model.Destination) int {
		if a.IsDefault != b.IsDefault {
			if a.IsDefault {
				return -1
			}
			return 1
		}
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})
	if out == nil {
		out = []model.Destination{}
	}
	return out, err
}

// Get returns a destination (API key encrypted).
func (r *Registry) Get(id string) (model.Destination, error) {
	if r.Single && id != model.DefaultDestinationID {
		return model.Destination{}, fmt.Errorf("%w: %s (%w)", ErrNotFound, id, ErrSingleInstance)
	}
	d, ok, err := r.b.Get(id)
	if err == nil && !ok {
		err = fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return d, err
}

// DefaultID returns the id of the default destination.
func (r *Registry) DefaultID() (string, error) {
	all, err := r.List()
	if err != nil {
		return "", err
	}
	for _, d := range all {
		if d.IsDefault {
			return d.ID, nil
		}
	}
	if len(all) > 0 {
		return all[0].ID, nil
	}
	return "", ErrNoDefault
}

// Resolve turns "" into the default destination id.
func (r *Registry) Resolve(id string) (string, error) {
	if id != "" {
		_, err := r.Get(id)
		return id, err
	}
	return r.DefaultID()
}

// Target returns the client of a destination ("" = default).
func (r *Registry) Target(id string) (*Target, error) {
	id, err := r.Resolve(id)
	if err != nil {
		return nil, err
	}
	r.mu.Lock()
	t, ok := r.targets[id]
	r.mu.Unlock()
	if ok {
		return t, nil
	}
	d, err := r.Get(id)
	if err != nil {
		return nil, err
	}
	key, err := r.cipher.Decrypt(d.APIKey, aad(d.ID))
	if err != nil {
		return nil, fmt.Errorf("decrypt the API key of %s: %w", d.Name, err)
	}
	timeout := d.Timeout.Or(30 * time.Second)
	client := NewClient(d.URL, key, d.Gzip, timeout)
	t = &Target{ID: d.ID, Name: d.Name, Client: client,
		Indexes: &indexmgr.Manager{Client: client, WaitTimeout: 2 * time.Minute, RawGet: rawGetter(d.URL, key, timeout), Log: r.log}}
	r.mu.Lock()
	r.targets[id] = t
	r.mu.Unlock()
	return t, nil
}

func (r *Registry) invalidate(id string) {
	r.mu.Lock()
	delete(r.targets, id)
	delete(r.health, id)
	delete(r.probing, id) // a running check must not restore stale health
	r.mu.Unlock()
}

func newID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Create stores a destination. The first one becomes the default.
func (r *Registry) Create(d model.Destination) (model.Destination, error) {
	if r.Single {
		return d, ErrSingleInstance
	}
	if d.ID == "" {
		d.ID = newID()
	}
	if _, ok, _ := r.b.Get(d.ID); ok {
		return d, fmt.Errorf("%w: destination %s already exists", ErrConflict, d.ID)
	}
	if err := d.Validate(); err != nil {
		return d, err
	}
	all, err := r.List()
	if err != nil {
		return d, err
	}
	if len(all) == 0 {
		d.IsDefault = true
	} else if d.IsDefault {
		if err := r.clearDefault(); err != nil {
			return d, err
		}
	}
	d.CreatedAt = time.Now().UTC()
	d.UpdatedAt = d.CreatedAt
	return d, r.b.Put(d.ID, d)
}

// Update replaces a destination. A masked key keeps the stored one. It
// reports whether the connection (url or key) changed.
func (r *Registry) Update(id string, d model.Destination) (model.Destination, bool, error) {
	prev, err := r.Get(id)
	if err != nil {
		return d, false, err
	}
	prevKey, err := r.cipher.Decrypt(prev.APIKey, aad(id))
	if err != nil {
		return d, false, err
	}
	d.ID = id
	if d.APIKey == model.SecretMask {
		d.APIKey = prevKey
	}
	if prev.ManagedByEnv {
		// The environment owns the connection of this destination.
		d.URL, d.APIKey, d.ManagedByEnv = prev.URL, prevKey, true
	}
	if err := d.Validate(); err != nil {
		return d, false, err
	}
	d.IsDefault = prev.IsDefault
	d.CreatedAt, d.UpdatedAt = prev.CreatedAt, time.Now().UTC()
	changed := d.URL != prev.URL || d.APIKey != prevKey || d.Gzip != prev.Gzip || d.Timeout != prev.Timeout
	if err := r.b.Put(id, d); err != nil {
		return d, false, err
	}
	r.invalidate(id)
	return d, changed, nil
}

func (r *Registry) clearDefault() error {
	all, err := r.List()
	if err != nil {
		return err
	}
	for _, d := range all {
		if d.IsDefault {
			if _, err := r.b.UpdateExisting(d.ID, func(v *model.Destination) error { v.IsDefault = false; return nil }); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetDefault makes id the default destination.
func (r *Registry) SetDefault(id string) error {
	if r.Single {
		return ErrSingleInstance
	}
	if _, err := r.Get(id); err != nil {
		return err
	}
	if err := r.clearDefault(); err != nil {
		return err
	}
	found, err := r.b.UpdateExisting(id, func(v *model.Destination) error { v.IsDefault = true; return nil })
	if err == nil && !found {
		err = fmt.Errorf("destination %s was deleted", id)
	}
	return err
}

// Delete removes a destination; callers check it is unused first.
func (r *Registry) Delete(id string) error {
	if r.Single {
		return ErrSingleInstance
	}
	d, err := r.Get(id)
	if err != nil {
		return err
	}
	if d.IsDefault {
		if all, _ := r.List(); len(all) > 1 {
			return fmt.Errorf("%w: make another destination the default first", ErrConflict)
		}
	}
	r.invalidate(id)
	return r.b.Delete(id)
}

// Purge deletes every destination, including the MEILI_URL one (EnsureEnv
// recreates it); callers check that no task uses them first.
func (r *Registry) Purge() (int, error) {
	n, err := r.b.Clear()
	r.mu.Lock()
	clear(r.targets)
	clear(r.health)
	clear(r.probing)
	r.mu.Unlock()
	return n, err
}

// EnsureEnv creates or refreshes the destination from MEILI_URL and turns
// on single instance mode. Without MEILI_URL it does nothing and
// destinations are managed in the dashboard.
func (r *Registry) EnsureEnv(url, key string, gzip bool, timeout time.Duration) error {
	if url == "" {
		return nil
	}
	r.Single = true
	prev, ok, err := r.b.Get(model.DefaultDestinationID)
	if err != nil {
		return err
	}
	d := model.Destination{ID: model.DefaultDestinationID, Name: "Default", URL: url, APIKey: key, Gzip: gzip,
		Timeout: model.Duration(timeout), ManagedByEnv: true}
	if ok {
		d.Name, d.Description, d.Tags, d.CreatedAt = prev.Name, prev.Description, prev.Tags, prev.CreatedAt
		d.HealthCheckInterval = prev.HealthCheckInterval
	} else {
		d.CreatedAt = time.Now().UTC()
	}
	d.IsDefault = true // the only destination in single instance mode
	if err := d.Validate(); err != nil {
		return fmt.Errorf("MEILI_URL: %w", err)
	}
	d.UpdatedAt = time.Now().UTC()
	if err := r.b.Put(d.ID, d); err != nil {
		return err
	}
	r.invalidate(d.ID)
	return nil
}

// TestResult is the outcome of a connection test.
type TestResult struct {
	OK        bool   `json:"ok"`
	Error     string `json:"error,omitempty"`
	Version   string `json:"version,omitempty"`
	LatencyMs int64  `json:"latencyMs"`
	Indexes   int    `json:"indexes"`
}

// Test checks a (possibly unsaved) destination. A masked key on a saved
// destination uses the stored key.
func (r *Registry) Test(ctx context.Context, d model.Destination) TestResult {
	if d.ID != "" && d.APIKey == model.SecretMask {
		prev, err := r.Get(d.ID)
		if err == nil && prev.URL != d.URL {
			// Never send a stored key to another server.
			return TestResult{Error: "the URL changed: enter the API key again to test it"}
		}
		if err == nil {
			d.APIKey, _ = r.cipher.Decrypt(prev.APIKey, aad(d.ID))
		}
	}
	if err := d.Validate(); err != nil {
		return TestResult{Error: err.Error()}
	}
	return check(ctx, NewClient(d.URL, d.APIKey, d.Gzip, d.Timeout.Or(10*time.Second)))
}

func check(ctx context.Context, c meilisearch.ServiceManager) TestResult {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	start := time.Now()
	v, err := c.Version(ctx)
	res := TestResult{LatencyMs: time.Since(start).Milliseconds()}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.Version = v.PkgVersion
	ix, err := c.ListIndexes(ctx, &meilisearch.IndexesQuery{Limit: 1})
	if err != nil {
		res.Error = "connected, but listing indexes failed (check the API key permissions): " + err.Error()
		return res
	}
	res.Indexes = int(ix.Total)
	res.OK = true
	return res
}

// Health returns the last health of a destination. The monitor keeps it
// fresh; without a recent check (monitor not running, or a new
// destination) it checks now.
func (r *Registry) Health(ctx context.Context, id string) Health {
	r.mu.Lock()
	h, ok := r.health[id]
	r.mu.Unlock()
	if ok && time.Now().Before(h.NextCheckAt.Add(5*time.Second)) {
		return h
	}
	return r.Check(ctx, id)
}

// Check checks a destination now and caches the result. Concurrent calls
// for the same destination share one check.
func (r *Registry) Check(ctx context.Context, id string) Health {
	r.mu.Lock()
	if wait, ok := r.probing[id]; ok {
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
		}
		r.mu.Lock()
		h := r.health[id]
		r.mu.Unlock()
		return h
	}
	done := make(chan struct{})
	r.probing[id] = done
	prev, hadPrev := r.health[id]
	r.mu.Unlock()

	h := r.probe(ctx, id)
	if h.Healthy {
		h.LastHealthyAt = h.CheckedAt
	} else if hadPrev {
		h.ConsecutiveFailures = prev.ConsecutiveFailures + 1
		h.LastHealthyAt = prev.LastHealthyAt
	} else {
		h.ConsecutiveFailures = 1
	}

	r.mu.Lock()
	// A destination edited or deleted during the check has dropped its
	// cache entry (and this check); keep the stale result out.
	if r.probing[id] == done {
		r.health[id] = h
		delete(r.probing, id)
	}
	r.mu.Unlock()
	close(done)
	if r.OnHealth != nil {
		r.OnHealth(id, h)
	}
	return h
}

func (r *Registry) probe(ctx context.Context, id string) Health {
	h := Health{CheckedAt: time.Now().UTC()}
	d, err := r.Get(id)
	if err != nil {
		h.Error = err.Error()
		h.NextCheckAt = h.CheckedAt.Add(model.DefaultHealthInterval)
		return h
	}
	every := d.HealthEvery()
	h.NextCheckAt = h.CheckedAt.Add(every)
	t, err := r.Target(id)
	if err != nil {
		h.Error = err.Error()
		return h
	}
	ctx, cancel := context.WithTimeout(ctx, min(every, 5*time.Second))
	defer cancel()
	start := time.Now()
	v, err := t.Client.Version(ctx)
	if err != nil {
		h.Error = err.Error()
		return h
	}
	h.Healthy, h.Version = true, v.PkgVersion
	h.LatencyMs = time.Since(start).Milliseconds()
	if s, err := t.Client.GetStats(ctx, nil); err == nil {
		h.Indexes, h.DatabaseSize = len(s.Indexes), s.DatabaseSize
		for _, ix := range s.Indexes {
			h.Documents += ix.NumberOfDocuments
		}
	}
	return h
}

// Monitor checks every destination at its health check interval until ctx
// is cancelled.
func (r *Registry) Monitor(ctx context.Context) {
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		all, err := r.List()
		if err != nil {
			r.log.Warn("health monitor: list destinations", "err", err)
		}
		now := time.Now()
		for _, d := range all {
			r.mu.Lock()
			h, ok := r.health[d.ID]
			_, running := r.probing[d.ID]
			r.mu.Unlock()
			// The interval of the stored destination wins over the one
			// planned by the last check, so a shorter interval applies now.
			if running {
				continue
			}
			if ok {
				due := h.NextCheckAt
				if c := h.CheckedAt.Add(d.HealthEvery()); c.Before(due) {
					due = c
				}
				if now.Before(due) {
					continue
				}
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.Check(ctx, d.ID)
			}()
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
