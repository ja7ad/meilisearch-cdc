//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"strings"
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

// healthEvents streams destination.health events from the SSE endpoint.
func healthEvents(t *testing.T, e *env) <-chan engine.DestinationHealthEvent {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, "GET", e.srv.URL+"/api/v1/events?token="+e.token, nil)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	out := make(chan engine.DestinationHealthEvent, 64)
	go func() {
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		event := ""
		for sc.Scan() {
			line := sc.Text()
			if v, ok := strings.CutPrefix(line, "event: "); ok {
				event = v
			} else if v, ok := strings.CutPrefix(line, "data: "); ok && event == "destination.health" {
				var ev engine.DestinationHealthEvent
				if json.Unmarshal([]byte(v), &ev) == nil {
					out <- ev
				}
			}
		}
	}()
	return out
}

func TestDestinationHealthMonitor(t *testing.T) {
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "getmeili/meilisearch:latest", ExposedPorts: []string{"7700/tcp"},
			Env:        map[string]string{"MEILI_MASTER_KEY": "health-key", "MEILI_NO_ANALYTICS": "true"},
			WaitingFor: wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	testcontainers.CleanupContainer(t, c)
	require.NoError(t, err)
	url, err := c.PortEndpoint(ctx, "7700/tcp", "http")
	require.NoError(t, err)

	e := newEnv(t, func(c *config.Config) { c.MeiliURL = "" })
	events := healthEvents(t, e)

	assert.Equal(t, 422, e.do("POST", "/api/v1/destinations", map[string]any{"name": "fast", "url": url, "healthCheckInterval": "2s"}, nil))
	assert.Equal(t, 422, e.do("POST", "/api/v1/destinations", map[string]any{"name": "slow", "url": url, "healthCheckInterval": "2h"}, nil))

	var other engine.DestinationView
	e.mustDo("POST", "/api/v1/destinations", map[string]any{"name": "Default interval", "url": meiliURL, "apiKey": masterKey}, &other, 201)
	assert.Equal(t, "30s", other.HealthCheckInterval.D().String())

	var v engine.DestinationView
	e.mustDo("POST", "/api/v1/destinations", map[string]any{"name": "Watched", "url": url, "apiKey": "health-key", "healthCheckInterval": "5s"}, &v, 201)
	assert.Equal(t, 5*time.Second, v.HealthCheckInterval.D())
	require.True(t, v.Health.Healthy, v.Health.Error)
	assert.Equal(t, 0, v.Health.ConsecutiveFailures)
	assert.False(t, v.Health.LastHealthyAt.IsZero())
	assert.WithinDuration(t, v.Health.CheckedAt.Add(5*time.Second), v.Health.NextCheckAt, time.Second)

	// The monitor checks again on its own and pushes the result live.
	waitEvent := func(timeout time.Duration, cond func(dest.Health) bool) dest.Health {
		t.Helper()
		deadline := time.After(timeout)
		for {
			select {
			case ev := <-events:
				if ev.ID == v.ID && cond(ev.Health) {
					return ev.Health
				}
			case <-deadline:
				t.Fatalf("no matching destination.health event within %s", timeout)
			}
		}
	}
	first := v.Health.CheckedAt
	h := waitEvent(15*time.Second, func(h dest.Health) bool { return h.CheckedAt.After(first) })
	assert.True(t, h.Healthy)
	assert.LessOrEqual(t, h.CheckedAt.Sub(first), 8*time.Second, "checked at the 5s interval")

	// Check now.
	var now dest.Health
	e.mustDo("POST", "/api/v1/destinations/"+v.ID+"/health/check", nil, &now, 200)
	assert.True(t, now.Healthy)
	assert.True(t, now.CheckedAt.After(h.CheckedAt))
	assert.Equal(t, 404, e.do("POST", "/api/v1/destinations/nope/health/check", nil, nil))

	// Instance down: failures accumulate while lastHealthyAt is kept.
	require.NoError(t, c.Stop(ctx, nil))
	down := waitEvent(30*time.Second, func(h dest.Health) bool { return h.ConsecutiveFailures >= 2 })
	assert.False(t, down.Healthy)
	assert.NotEmpty(t, down.Error)
	assert.Equal(t, now.CheckedAt.Unix(), down.LastHealthyAt.Unix())
	var got engine.DestinationView
	e.mustDo("GET", "/api/v1/destinations/"+v.ID, nil, &got, 200)
	assert.False(t, got.Health.Healthy)
	assert.GreaterOrEqual(t, got.Health.ConsecutiveFailures, 2)

	// Changing the interval applies without waiting for the old schedule.
	body := map[string]any{"name": "Watched", "url": url, "apiKey": model.SecretMask, "healthCheckInterval": "1m0s"}
	e.mustDo("PUT", "/api/v1/destinations/"+v.ID, body, &got, 200)
	assert.Equal(t, time.Minute, got.HealthCheckInterval.D())
	assert.WithinDuration(t, got.Health.CheckedAt.Add(time.Minute), got.Health.NextCheckAt, time.Second)
}
