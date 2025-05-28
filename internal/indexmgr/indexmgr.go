// Package indexmgr manages Meilisearch indexes for CDC tasks: creation,
// primary key safety, declarative settings with drift detection and
// zero-downtime rebuilds through shadow indexes and swaps.
package indexmgr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
)

// Manager wraps a Meilisearch client.
type Manager struct {
	Client meilisearch.ServiceManager
	// RawGet fetches a Meilisearch path as JSON. Settings are read raw so
	// drift checks are exact: the SDK Settings type drops false and zero
	// values and cannot hold granular filterable attributes.
	RawGet func(ctx context.Context, path string) ([]byte, error)
	Log    *slog.Logger
	// WaitTimeout bounds how long index operations wait for their task.
	WaitTimeout time.Duration
}

// New returns a Manager.
func New(client meilisearch.ServiceManager) *Manager {
	return &Manager{Client: client, WaitTimeout: 2 * time.Minute}
}

// ErrPrimaryKeyMismatch protects an index that already uses another key.
var ErrPrimaryKeyMismatch = errors.New("index primary key mismatch")

// IsNotFound reports whether err is Meilisearch's index_not_found.
func IsNotFound(err error) bool {
	var me *meilisearch.Error
	return errors.As(err, &me) && (me.APIError.Code == meilisearch.APIErrCodeIndexNotFound || me.StatusCode == 404)
}

// Wait waits for a task and turns a failed task into an error.
func (m *Manager) Wait(ctx context.Context, uid int64) (*meilisearch.Task, error) {
	ctx, cancel := context.WithTimeout(ctx, m.WaitTimeout)
	defer cancel()
	t, err := m.Client.WaitForTask(ctx, uid, 50*time.Millisecond)
	if err != nil {
		return nil, fmt.Errorf("wait for task %d: %w", uid, err)
	}
	if t.Status != meilisearch.TaskStatusSucceeded {
		return t, fmt.Errorf("task %d %s: %s (%s)", uid, t.Status, t.Error.Message, t.Error.Code)
	}
	return t, nil
}

// Ensure makes sure the index exists with the given primary key. An empty
// index with another key is switched over; a non-empty one is refused with
// ErrPrimaryKeyMismatch, because changing the key would orphan documents.
func (m *Manager) Ensure(ctx context.Context, uid, pk string) (*meilisearch.IndexResult, error) {
	idx, err := m.Client.GetIndex(ctx, uid)
	if IsNotFound(err) {
		info, err := m.Client.CreateIndex(ctx, &meilisearch.IndexConfig{Uid: uid, PrimaryKey: pk})
		if err != nil {
			return nil, fmt.Errorf("create index %s: %w", uid, err)
		}
		if _, err := m.Wait(ctx, info.TaskUID); err != nil {
			// Another writer may have created it concurrently.
			if !strings.Contains(err.Error(), "index_already_exists") {
				return nil, fmt.Errorf("create index %s: %w", uid, err)
			}
		}
		return m.Client.GetIndex(ctx, uid)
	}
	if err != nil {
		return nil, fmt.Errorf("get index %s: %w", uid, err)
	}
	if idx.PrimaryKey == pk {
		return idx, nil
	}
	stats, err := m.Client.Index(uid).GetStats(ctx, nil)
	if err != nil {
		return nil, err
	}
	if stats.NumberOfDocuments > 0 && idx.PrimaryKey != "" {
		return nil, fmt.Errorf("%w: index %q uses %q but the task writes %q and the index holds %d documents; "+
			"use a new index, change mapping.primaryKeyField, or resync with zero-downtime enabled",
			ErrPrimaryKeyMismatch, uid, idx.PrimaryKey, pk, stats.NumberOfDocuments)
	}
	if err := m.SetPrimaryKey(ctx, uid, pk); err != nil {
		return nil, err
	}
	return m.Client.GetIndex(ctx, uid)
}

// SetPrimaryKey changes the primary key of an empty index.
func (m *Manager) SetPrimaryKey(ctx context.Context, uid, pk string) error {
	info, err := m.Client.Index(uid).UpdateIndex(ctx, &meilisearch.UpdateIndexRequestParams{PrimaryKey: pk})
	if err != nil {
		return fmt.Errorf("update primary key of %s: %w", uid, err)
	}
	_, err = m.Wait(ctx, info.TaskUID)
	return err
}

// parsed is settings JSON split into what the SDK Settings type can carry
// and the parts that need a dedicated call.
type parsed struct {
	settings *meilisearch.Settings
	// granular filterable attributes ({"attributePatterns": [...], "features": {...}})
	// do not fit Settings.FilterableAttributes ([]string).
	filterable     *[]interface{}
	facetSearchOff bool
}

// parse splits settings JSON. In strict mode unknown keys are errors; the
// lenient mode (copying live settings) skips keys the SDK does not know
// and reports them in dropped.
func parse(raw json.RawMessage, strict bool) (p parsed, dropped []string, err error) {
	p, err = parseStrict(raw, strict, &dropped)
	return p, dropped, err
}

func parseStrict(raw json.RawMessage, strict bool, dropped *[]string) (parsed, error) {
	p := parsed{settings: &meilisearch.Settings{}}
	if len(bytes.TrimSpace(raw)) == 0 {
		return p, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return p, fmt.Errorf("invalid settings: %w", err)
	}
	if f, ok := fields["filterableAttributes"]; ok {
		var plain []string
		if json.Unmarshal(f, &plain) != nil {
			var granular []interface{}
			if err := json.Unmarshal(f, &granular); err != nil {
				return p, fmt.Errorf("invalid settings: filterableAttributes: %w", err)
			}
			p.filterable = &granular
			delete(fields, "filterableAttributes")
		}
	}
	if f, ok := fields["facetSearch"]; ok && string(bytes.TrimSpace(f)) == "false" {
		p.facetSearchOff = true // dropped by the SDK type's omitempty
	}
	if !strict {
		known := knownSettings()
		for k := range fields {
			if !known[k] {
				*dropped = append(*dropped, k)
				delete(fields, k)
			}
		}
	}
	rest, _ := json.Marshal(fields)
	dec := json.NewDecoder(bytes.NewReader(rest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(p.settings); err != nil {
		return p, fmt.Errorf("invalid settings: %w", err)
	}
	return p, nil
}

// knownSettings lists the JSON keys of the SDK Settings type.
func knownSettings() map[string]bool {
	out := map[string]bool{}
	t := reflect.TypeOf(meilisearch.Settings{})
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" {
			out[name] = true
		}
	}
	return out
}

// ParseSettings validates settings JSON (unknown keys are rejected).
func ParseSettings(raw json.RawMessage) (*meilisearch.Settings, error) {
	p, _, err := parse(raw, true)
	return p.settings, err
}

// ApplySettings updates the index settings and waits for the tasks.
func (m *Manager) ApplySettings(ctx context.Context, uid string, raw json.RawMessage) (*meilisearch.Task, error) {
	return m.applySettings(ctx, uid, raw, true)
}

func (m *Manager) applySettings(ctx context.Context, uid string, raw json.RawMessage, strict bool) (*meilisearch.Task, error) {
	p, dropped, err := parse(raw, strict)
	if err != nil {
		return nil, err
	}
	if len(dropped) > 0 && m.Log != nil {
		m.Log.Warn("settings not supported by the Meilisearch SDK were not copied", "index", uid, "keys", dropped)
	}
	idx := m.Client.Index(uid)
	info, err := idx.UpdateSettings(ctx, p.settings)
	if err != nil {
		return nil, fmt.Errorf("update settings of %s: %w", uid, err)
	}
	t, err := m.Wait(ctx, info.TaskUID)
	if err != nil {
		return t, err
	}
	if p.filterable != nil {
		info, err := idx.UpdateFilterableAttributes(ctx, p.filterable)
		if err != nil {
			return t, fmt.Errorf("update filterable attributes of %s: %w", uid, err)
		}
		if t, err = m.Wait(ctx, info.TaskUID); err != nil {
			return t, err
		}
	}
	if p.facetSearchOff {
		info, err := idx.UpdateFacetSearch(ctx, false)
		if err != nil {
			return t, err
		}
		return m.Wait(ctx, info.TaskUID)
	}
	return t, nil
}

// Change is one setting whose live value differs from the desired one.
type Change struct {
	Key     string `json:"key"`
	Current any    `json:"current"`
	Desired any    `json:"desired"`
}

// setKeys are list settings whose order is irrelevant.
var setKeys = map[string]bool{
	"filterableAttributes": true, "sortableAttributes": true, "stopWords": true,
	"dictionary": true, "separatorTokens": true, "nonSeparatorTokens": true,
}

// Diff compares the desired settings with the live ones. Only keys present
// in desired are compared, so unmanaged settings never show up as drift.
func (m *Manager) Diff(ctx context.Context, uid string, raw json.RawMessage) ([]Change, error) {
	if _, err := ParseSettings(raw); err != nil {
		return nil, err
	}
	live, err := m.LiveSettings(ctx, uid)
	if err != nil {
		return nil, err
	}
	return DiffSettings(live, raw)
}

// LiveSettings returns the index settings as raw JSON.
func (m *Manager) LiveSettings(ctx context.Context, uid string) (json.RawMessage, error) {
	if m.RawGet != nil {
		return m.RawGet(ctx, "/indexes/"+url.PathEscape(uid)+"/settings")
	}
	s, err := m.Client.Index(uid).GetSettings(ctx)
	if err != nil {
		return nil, err
	}
	return json.Marshal(s)
}

// DiffSettings compares live settings JSON with desired JSON.
func DiffSettings(liveJSON json.RawMessage, raw json.RawMessage) ([]Change, error) {
	desired := map[string]any{}
	if len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, &desired); err != nil {
			return nil, err
		}
	}
	current := map[string]any{}
	if err := json.Unmarshal(liveJSON, &current); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(desired))
	for k := range desired {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Change
	for _, k := range keys {
		d, c := desired[k], current[k]
		if equalSetting(k, c, d) {
			continue
		}
		out = append(out, Change{Key: k, Current: c, Desired: d})
	}
	return out, nil
}

func equalSetting(key string, current, desired any) bool {
	if current == nil {
		current = zeroLike(desired)
	}
	if desired == nil {
		desired = zeroLike(current)
	}
	if setKeys[key] {
		a, aok := current.([]any)
		b, bok := desired.([]any)
		if aok && bok {
			return sameSet(a, b)
		}
	}
	if dm, ok := desired.(map[string]any); ok {
		// Objects (typoTolerance, faceting, embedders...) are compared on the
		// keys the user wrote.
		cm, _ := current.(map[string]any)
		for k, dv := range dm {
			if !equalSetting(k, cm[k], dv) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(current, desired)
}

func zeroLike(v any) any {
	switch v.(type) {
	case []any:
		return []any{}
	case map[string]any:
		return map[string]any{}
	case bool:
		return false
	case float64:
		return float64(0)
	case string:
		return ""
	}
	return nil
}

func sameSet(a, b []any) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(v any) string { j, _ := json.Marshal(v); return string(j) }
	as := make([]string, len(a))
	bs := make([]string, len(b))
	for i := range a {
		as[i], bs[i] = key(a[i]), key(b[i])
	}
	slices.Sort(as)
	slices.Sort(bs)
	return slices.Equal(as, bs)
}

// ShadowName returns the name of the temporary rebuild index for uid.
func ShadowName(uid string) string {
	return fmt.Sprintf("%s__cdc_shadow_%d", uid, time.Now().Unix())
}

// IsShadow reports whether uid is a rebuild index created by the service.
func IsShadow(uid string) bool { return strings.Contains(uid, "__cdc_shadow_") }

// CreateShadow creates a shadow index with the live index settings (when it
// exists) overlaid with the desired ones, so the swapped-in index behaves
// exactly like the one it replaces.
func (m *Manager) CreateShadow(ctx context.Context, live, pk string, desired json.RawMessage) (string, error) {
	shadow := ShadowName(live)
	info, err := m.Client.CreateIndex(ctx, &meilisearch.IndexConfig{Uid: shadow, PrimaryKey: pk})
	if err != nil {
		return "", fmt.Errorf("create shadow index: %w", err)
	}
	if _, err := m.Wait(ctx, info.TaskUID); err != nil {
		return "", err
	}
	if s, err := m.LiveSettings(ctx, live); err == nil {
		// Lenient: keys newer than the SDK must not block a rebuild.
		if _, err := m.applySettings(ctx, shadow, s, false); err != nil {
			return shadow, fmt.Errorf("copy settings to shadow: %w", err)
		}
	} else if !IsNotFound(err) {
		return shadow, err
	}
	if len(bytes.TrimSpace(desired)) > 0 {
		if _, err := m.ApplySettings(ctx, shadow, desired); err != nil {
			return shadow, err
		}
	}
	return shadow, nil
}

// Promote atomically replaces live with shadow and deletes the old data.
// When live does not exist the shadow is renamed.
func (m *Manager) Promote(ctx context.Context, live, shadow string) error {
	_, err := m.Client.GetIndex(ctx, live)
	rename := IsNotFound(err)
	if err != nil && !rename {
		return err
	}
	info, err := m.Client.SwapIndexes(ctx, []*meilisearch.SwapIndexesParams{{Indexes: []string{live, shadow}, Rename: rename}})
	if err != nil {
		return fmt.Errorf("swap %s and %s: %w", live, shadow, err)
	}
	if _, err := m.Wait(ctx, info.TaskUID); err != nil {
		return err
	}
	if rename {
		return nil
	}
	// After the swap the shadow name holds the previous data.
	return m.Drop(ctx, shadow)
}

// Drop deletes an index and waits.
func (m *Manager) Drop(ctx context.Context, uid string) error {
	info, err := m.Client.DeleteIndex(ctx, uid)
	if err != nil {
		if IsNotFound(err) {
			return nil
		}
		return err
	}
	_, err = m.Wait(ctx, info.TaskUID)
	return err
}

// DocumentCount returns the number of documents, or 0 when the index is missing.
func (m *Manager) DocumentCount(ctx context.Context, uid string) (int64, error) {
	st, err := m.Client.Index(uid).GetStats(ctx, nil)
	if IsNotFound(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return st.NumberOfDocuments, nil
}

// WaitIdle waits until the index has no enqueued or processing tasks.
func (m *Manager) WaitIdle(ctx context.Context, uid string) error {
	for {
		res, err := m.Client.GetTasks(ctx, &meilisearch.TasksQuery{
			IndexUIDS: []string{uid},
			Statuses:  []meilisearch.TaskStatus{meilisearch.TaskStatusEnqueued, meilisearch.TaskStatusProcessing},
			Limit:     1,
		})
		if err != nil {
			return err
		}
		if len(res.Results) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
