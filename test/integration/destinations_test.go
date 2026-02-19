//go:build integration

package integration

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func startSecondMeili(t *testing.T) string {
	c, err := testcontainers.GenericContainer(context.Background(), testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "getmeili/meilisearch:latest", ExposedPorts: []string{"7700/tcp"},
			Env:        map[string]string{"MEILI_MASTER_KEY": "second-key", "MEILI_NO_ANALYTICS": "true"},
			WaitingFor: wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.PortEndpoint(context.Background(), "7700/tcp", "http")
	require.NoError(t, err)
	return url
}

func TestManyDestinations(t *testing.T) {
	second := startSecondMeili(t)
	e := newEnv(t, func(c *config.Config) { c.MeiliURL = "" }) // multi-instance mode

	var cfg map[string]any
	e.mustDo("GET", "/api/v1/auth/config", nil, &cfg, 200)
	assert.Equal(t, false, cfg["singleDestination"])
	assert.Equal(t, map[string]any{"destinations": float64(0), "connectors": float64(0), "tasks": float64(0), "complete": false}, cfg["onboarding"])
	assert.Equal(t, 409, e.do("GET", "/api/v1/indexes", nil, nil), "no destination yet")

	var test dest.TestResult
	e.mustDo("POST", "/api/v1/destinations/test", map[string]any{"name": "x", "url": second, "apiKey": "wrong"}, &test, 200)
	assert.False(t, test.OK)
	e.mustDo("POST", "/api/v1/destinations/test", map[string]any{"name": "x", "url": second, "apiKey": "second-key"}, &test, 200)
	require.True(t, test.OK, test.Error)
	assert.NotEmpty(t, test.Version)

	var primary, replica engine.DestinationView
	e.mustDo("POST", "/api/v1/destinations", map[string]any{"name": "Primary", "url": meiliURL, "apiKey": masterKey, "gzip": true}, &primary, 201)
	e.mustDo("POST", "/api/v1/destinations", map[string]any{"name": "EU replica", "url": second, "apiKey": "second-key"}, &replica, 201)
	assert.True(t, primary.IsDefault, "the first destination becomes the default")
	assert.Equal(t, model.SecretMask, replica.APIKey)
	assert.True(t, replica.Health.Healthy)

	// One source table synced to both instances.
	table := uniq("fanout")
	tab := seed(table, 40)
	index := uniq("idx")
	mk := func(destID string) model.TaskView {
		task := memTask(table, index)
		task["index"].(map[string]any)["destinationId"] = destID
		var v model.TaskView
		e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
		return v
	}
	a := mk("")
	b := mk(replica.ID)
	assert.Equal(t, primary.ID, a.Index.DestinationID, "the default destination is pinned on save")
	e.waitState(a.ID, model.StateStreaming)
	e.waitState(b.ID, model.StateStreaming)
	waitIndexDocs := func(destID string, n float64) {
		eventually(t, time.Minute, func() bool {
			var ix map[string]any
			if e.do("GET", "/api/v1/indexes/"+index+"?destination="+destID, nil, &ix) != 200 {
				return false
			}
			return ix["stats"].(map[string]any)["numberOfDocuments"] == n
		}, func() string { return "documents on " + destID })
	}
	waitIndexDocs(primary.ID, 40)
	waitIndexDocs(replica.ID, 40)
	tab.Upsert(map[string]any{"id": int64(41), "name": "both"})
	waitIndexDocs(primary.ID, 41)
	waitIndexDocs(replica.ID, 41)

	var ix map[string]any
	e.mustDo("GET", "/api/v1/indexes/"+index+"?destination="+replica.ID, nil, &ix, 200)
	assert.Equal(t, []any{b.ID}, ix["managedBy"], "managedBy is per destination")

	// All instances at once, grouped per destination.
	var groups []struct {
		DestinationID   string           `json:"destinationId"`
		DestinationName string           `json:"destinationName"`
		IsDefault       bool             `json:"isDefault"`
		Healthy         bool             `json:"healthy"`
		Error           string           `json:"error"`
		Indexes         []map[string]any `json:"indexes"`
	}
	e.mustDo("GET", "/api/v1/indexes?destination=*", nil, &groups, 200)
	require.Len(t, groups, 2)
	assert.Equal(t, primary.ID, groups[0].DestinationID, "default destination first")
	assert.True(t, groups[0].IsDefault)
	byDest := map[string][]any{}
	for _, g := range groups {
		assert.True(t, g.Healthy, g.Error)
		for _, ix := range g.Indexes {
			assert.Equal(t, g.DestinationID, ix["destinationId"])
			if ix["uid"] == index {
				byDest[g.DestinationID] = ix["managedBy"].([]any)
			}
		}
	}
	assert.Equal(t, map[string][]any{primary.ID: {a.ID}, replica.ID: {b.ID}}, byDest, "same uid listed under each instance")

	// Destination views show health and synced tasks.
	var view engine.DestinationView
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/destinations/"+replica.ID, nil, &view, 200)
		return len(view.UsedBy) == 1 && !view.Sync.LastSyncAt.IsZero()
	}, func() string { return "destination view" })
	assert.Equal(t, b.ID, view.UsedBy[0].ID)
	assert.Equal(t, 1, view.Sync.Running)
	// Health is refreshed at the health check interval, or on demand.
	eventually(t, 30*time.Second, func() bool {
		var h dest.Health
		e.mustDo("POST", "/api/v1/destinations/"+replica.ID+"/health/check", nil, &h, 200)
		return h.Documents == 41
	}, func() string { return "health documents" })
	e.mustDo("GET", "/api/v1/destinations/"+replica.ID, nil, &view, 200)
	assert.EqualValues(t, 41, view.Health.Documents)

	var overview map[string]any
	e.mustDo("GET", "/api/v1/overview", nil, &overview, 200)
	assert.Len(t, overview["destinations"], 2)

	// Guards.
	var conflict map[string]any
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/destinations/"+replica.ID, nil, &conflict))
	assert.Len(t, conflict["usedBy"], 1)
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/destinations/"+primary.ID, nil, nil), "default cannot be deleted")

	// Rotating the replica key restarts its task.
	e.mustDo("PUT", "/api/v1/destinations/"+replica.ID, map[string]any{"name": "EU replica", "url": second, "apiKey": model.SecretMask, "timeout": "20s"}, &view, 200)
	var logs []model.LogEntry
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/tasks/"+b.ID+"/logs", nil, &logs, 200)
		for _, l := range logs {
			if l.Message == `destination "EU replica" changed: restarting` {
				return true
			}
		}
		return false
	}, func() string { return "no restart" })
	e.waitState(b.ID, model.StateStreaming)

	e.mustDo("POST", "/api/v1/destinations/"+replica.ID+"/default", nil, &view, 200)
	assert.True(t, view.IsDefault)
	e.mustDo("DELETE", "/api/v1/tasks/"+a.ID, nil, nil, 204)
	e.mustDo("DELETE", "/api/v1/destinations/"+primary.ID, nil, nil, 204)

	// Alerts fire for an unreachable destination by name.
	var down engine.DestinationView
	e.mustDo("POST", "/api/v1/destinations", map[string]any{"name": "Broken", "url": "http://127.0.0.1:1"}, &down, 201)
	var rule map[string]any
	downRule := map[string]any{"name": "Meili down", "enabled": true, "condition": map[string]any{"type": "meili_down"},
		"channels": map[string]any{"telegramChatIds": []string{"-1001"}}}
	assert.Equal(t, 409, e.do("POST", "/api/v1/alerts/rules", downRule, nil), "no SMTP or Telegram yet")
	tg := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"result":{"username":"cdc_bot"}}`)
	}))
	defer tg.Close()
	e.mustDo("PUT", "/api/v1/settings/telegram", map[string]any{"botToken": "1:x", "apiUrl": tg.URL}, nil, 200)
	e.mustDo("POST", "/api/v1/alerts/rules", downRule, &rule, 201)
	var active []map[string]any
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/alerts/active", nil, &active, 200)
		return len(active) == 1
	}, func() string { return "meili_down did not fire" })
	assert.Equal(t, "Broken", active[0]["destinationName"])
}

func TestSingleInstanceMode(t *testing.T) {
	e := newEnv(t) // MEILI_URL set
	var cfg map[string]any
	e.mustDo("GET", "/api/v1/auth/config", nil, &cfg, 200)
	assert.Equal(t, true, cfg["singleDestination"])
	var list []engine.DestinationView
	e.mustDo("GET", "/api/v1/destinations", nil, &list, 200)
	require.Len(t, list, 1)
	assert.True(t, list[0].ManagedByEnv)
	assert.Equal(t, model.DefaultDestinationID, list[0].ID)
	assert.Equal(t, 409, e.do("POST", "/api/v1/destinations", map[string]any{"name": "Other", "url": "http://other:7700"}, nil))
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/destinations/default", nil, nil))
	// The connection is pinned; only cosmetic fields change.
	var v engine.DestinationView
	e.mustDo("PUT", "/api/v1/destinations/default", map[string]any{"name": "Production", "url": "http://elsewhere:7700"}, &v, 200)
	assert.Equal(t, "Production", v.Name)
	assert.Equal(t, meiliURL, v.URL)
	// Anonymous callers do not learn deployment details.
	e.token = ""
	var anon map[string]any
	e.mustDo("GET", "/api/v1/auth/config", nil, &anon, 200)
	assert.NotContains(t, anon, "singleDestination")
	assert.NotContains(t, anon, "onboarding")
}

func TestZeroConfigSecretKeyFile(t *testing.T) {
	dir := t.TempDir()
	e := newEnv(t, func(c *config.Config) { c.SecretKey = ""; c.DataDir = dir })
	raw, err := os.ReadFile(filepath.Join(dir, "secret.key"))
	require.NoError(t, err)
	assert.Len(t, string(raw), 45, "base64 of 32 bytes + newline")
	st, _ := os.Stat(filepath.Join(dir, "secret.key"))
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	var c engine.ConnectorView
	e.mustDo("POST", "/api/v1/connectors", map[string]any{"name": "pg", "type": "postgres", "config": map[string]any{"dsn": "postgres://u:pw@h/db"}}, &c, 201)
	plain, err := e.app.Engine.RevealConnector(c.ID)
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:pw@h/db", plain.Config.DSN)
}
