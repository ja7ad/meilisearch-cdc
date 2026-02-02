//go:build e2e

// Package e2e builds the real binary, runs it as a separate process and
// syncs real databases into a real Meilisearch.
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

const masterKey = "e2e-master-key"

var (
	binary   string
	meiliURL string
	meili    meilisearch.ServiceManager
)

func TestMain(m *testing.M) {
	code, err := setup(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "e2e setup:", err)
		os.Exit(1)
	}
	os.Exit(code)
}

func setup(m *testing.M) (int, error) {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "cdc-e2e")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(dir)
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	binary = filepath.Join(dir, "meilisearch-cdc")
	build := exec.Command("go", "build", "-race", "-o", binary, "./cmd/meilisearch-cdc")
	build.Dir = root
	build.Stdout, build.Stderr = os.Stdout, os.Stderr
	if err := build.Run(); err != nil {
		return 0, fmt.Errorf("build: %w", err)
	}

	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        envOr("MEILI_IMAGE", "getmeili/meilisearch:latest"),
			ExposedPorts: []string{"7700/tcp"},
			Env:          map[string]string{"MEILI_MASTER_KEY": masterKey, "MEILI_NO_ANALYTICS": "true"},
			WaitingFor:   wait.ForHTTP("/health").WithPort("7700/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		return 0, err
	}
	defer c.Terminate(ctx)
	if meiliURL, err = c.PortEndpoint(ctx, "7700/tcp", "http"); err != nil {
		return 0, err
	}
	meili = meilisearch.New(meiliURL, meilisearch.WithAPIKey(masterKey))
	return m.Run(), nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// service is a running meilisearch-cdc process.
type service struct {
	t       *testing.T
	dataDir string
	url     string
	cmd     *exec.Cmd
	log     *bytes.Buffer
}

func freePort(t *testing.T) int {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// startService runs the binary on dataDir (reused across restarts).
func startService(t *testing.T, dataDir string) *service {
	t.Helper()
	port := freePort(t)
	s := &service{t: t, dataDir: dataDir, url: fmt.Sprintf("http://127.0.0.1:%d", port), log: &bytes.Buffer{}}
	s.cmd = exec.Command(binary)
	s.cmd.Env = append(os.Environ(),
		"CDC_LISTEN=127.0.0.1:"+fmt.Sprint(port),
		"CDC_DATA_DIR="+dataDir,
		"MEILI_URL="+meiliURL,
		"MEILI_API_KEY="+masterKey,
		"CDC_ADMIN_TOKEN=e2e",
		"CDC_SECRET_KEY=e2e-secret",
		"CDC_LOG_LEVEL=info",
	)
	s.cmd.Stdout, s.cmd.Stderr = s.log, s.log
	require.NoError(t, s.cmd.Start())
	t.Cleanup(func() {
		s.stop(syscall.SIGTERM)
		if t.Failed() {
			t.Logf("service log:\n%s", s.log.String())
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(s.url + "/healthz"); err == nil {
			resp.Body.Close()
			return s
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("service did not start:\n%s", s.log.String())
	return nil
}

func (s *service) stop(sig syscall.Signal) {
	if s.cmd == nil || s.cmd.Process == nil || s.cmd.ProcessState != nil {
		return
	}
	_ = s.cmd.Process.Signal(sig)
	done := make(chan struct{})
	go func() { _ = s.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(40 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
}

func (s *service) do(method, path string, body, out any) int {
	s.t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, s.url+path, r)
	req.Header.Set("Authorization", "Bearer e2e")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(s.t, err)
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil && len(raw) > 0 {
		require.NoError(s.t, json.Unmarshal(raw, out), string(raw))
	}
	return resp.StatusCode
}

func (s *service) createTask(task map[string]any) string {
	s.t.Helper()
	var v model.TaskView
	code := s.do("POST", "/api/v1/tasks?start=true", task, &v)
	require.Equal(s.t, 201, code, "%+v", v)
	return v.ID
}

func (s *service) status(id string) model.Status {
	var v model.TaskView
	s.do("GET", "/api/v1/tasks/"+id, nil, &v)
	return v.Status
}

func (s *service) waitState(id string, want model.State) {
	s.t.Helper()
	var st model.Status
	eventually(s.t, 2*time.Minute, func() bool { st = s.status(id); return st.State == want },
		func() string {
			return fmt.Sprintf("task %s is %s (%s %s), want %s", id, st.State, st.Message, st.Error, want)
		})
}

func docCount(uid string) int64 {
	st, err := meili.Index(uid).GetStats(context.Background(), nil)
	if err != nil {
		return -1
	}
	return st.NumberOfDocuments
}

func waitDocs(t *testing.T, uid string, n int64) {
	t.Helper()
	eventually(t, 2*time.Minute, func() bool { return docCount(uid) == n },
		func() string { return fmt.Sprintf("index %s has %d documents, want %d", uid, docCount(uid), n) })
}

func getDoc(uid, id string) map[string]any {
	var d map[string]any
	if meili.Index(uid).GetDocument(context.Background(), id, nil, &d) != nil {
		return nil
	}
	return d
}

func waitDoc(t *testing.T, uid, id string, cond func(map[string]any) bool) {
	t.Helper()
	eventually(t, 2*time.Minute, func() bool { return cond(getDoc(uid, id)) },
		func() string { return fmt.Sprintf("document %s/%s is %v", uid, id, getDoc(uid, id)) })
}

func eventually(t *testing.T, timeout time.Duration, cond func() bool, msg func() string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	t.Fatal(msg())
}

func named(v string) func(map[string]any) bool {
	return func(d map[string]any) bool { return d != nil && d["name"] == v }
}

func gone(d map[string]any) bool { return d == nil }

func indexName(t *testing.T) string {
	return fmt.Sprintf("e2e_%d", time.Now().UnixNano())
}
