//go:build integration

// Package integration runs the whole service against a real Meilisearch.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/all"
	_ "github.com/ja7ad/meilisearch-cdc/internal/source/memory"
)

const masterKey = "integration-master-key"

var meiliURL string

func TestMain(m *testing.M) {
	ctx := context.Background()
	image := os.Getenv("MEILI_IMAGE")
	if image == "" {
		image = "getmeili/meilisearch:latest"
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        image,
			ExposedPorts: []string{"7700/tcp"},
			Env:          map[string]string{"MEILI_MASTER_KEY": masterKey, "MEILI_NO_ANALYTICS": "true", "MEILI_ENV": "development"},
			WaitingFor:   wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "start meilisearch:", err)
		os.Exit(1)
	}
	endpoint, err := c.PortEndpoint(ctx, "7700/tcp", "http")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	meiliURL = endpoint
	code := m.Run()
	_ = c.Terminate(ctx)
	os.Exit(code)
}

// env is a running service plus helpers.
type env struct {
	t     *testing.T
	app   *app.App
	srv   *httptest.Server
	meili meilisearch.ServiceManager
	token string
}

func newEnv(t *testing.T, mutate ...func(*config.Config)) *env {
	t.Helper()
	cfg := config.Config{
		Listen: "", DataDir: t.TempDir(), MeiliURL: meiliURL, MeiliAPIKey: masterKey, MeiliGzip: true,
		MeiliTimeout: 30 * time.Second, AdminToken: "admin", SecretKey: "secret", LogLevel: "warn", StopTimeout: 10 * time.Second,
	}
	for _, m := range mutate {
		m(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	if os.Getenv("CDC_TEST_LOG") != "" {
		log = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelDebug}))
	}
	a, err := app.New(cfg, log)
	require.NoError(t, err)
	require.NoError(t, a.Start(context.Background()))
	srv := httptest.NewServer(a.Handler)
	e := &env{t: t, app: a, srv: srv, meili: meilisearch.New(meiliURL, meilisearch.WithAPIKey(masterKey)), token: cfg.AdminToken}
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = a.Close(ctx)
	})
	return e
}

// do sends a JSON request and decodes the response into out (if non-nil).
func (e *env) do(method, path string, body, out any) int {
	e.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		require.NoError(e.t, err)
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, r)
	require.NoError(e.t, err)
	req.Header.Set("Content-Type", "application/json")
	if e.token != "" {
		req.Header.Set("Authorization", "Bearer "+e.token)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(e.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		require.NoError(e.t, json.Unmarshal(raw, out), string(raw))
	}
	return resp.StatusCode
}

func (e *env) mustDo(method, path string, body, out any, want int) {
	e.t.Helper()
	var raw json.RawMessage
	code := e.do(method, path, body, &raw)
	require.Equal(e.t, want, code, "%s %s -> %s", method, path, string(raw))
	if out != nil {
		require.NoError(e.t, json.Unmarshal(raw, out))
	}
}

func (e *env) status(id string) model.Status {
	var v model.TaskView
	e.mustDo("GET", "/api/v1/tasks/"+id, nil, &v, 200)
	return v.Status
}

func (e *env) waitState(id string, states ...model.State) model.Status {
	e.t.Helper()
	var st model.Status
	eventually(e.t, 60*time.Second, func() bool {
		st = e.status(id)
		for _, s := range states {
			if st.State == s {
				return true
			}
		}
		return false
	}, func() string {
		return fmt.Sprintf("task %s in %s (%s %s), want %v", id, st.State, st.Message, st.Error, states)
	})
	return st
}

func (e *env) docCount(uid string) int64 {
	st, err := e.meili.Index(uid).GetStats(context.Background(), nil)
	if err != nil {
		return -1
	}
	return st.NumberOfDocuments
}

func (e *env) waitDocs(uid string, n int64) {
	e.t.Helper()
	eventually(e.t, 60*time.Second, func() bool { return e.docCount(uid) == n },
		func() string { return fmt.Sprintf("index %s has %d docs, want %d", uid, e.docCount(uid), n) })
}

func (e *env) doc(uid, id string) map[string]any {
	var d map[string]any
	if err := e.meili.Index(uid).GetDocument(context.Background(), id, nil, &d); err != nil {
		return nil
	}
	return d
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal(msg())
}

func uniq(prefix string) string { return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()) }
