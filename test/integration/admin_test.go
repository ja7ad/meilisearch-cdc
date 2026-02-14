//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/admincli"
	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// shortDataDir keeps <dir>/admin.sock under the unix socket path limit
// (t.TempDir paths with long test names can exceed it on macOS).
func shortDataDir(t *testing.T) string {
	dir, err := os.MkdirTemp("", "cdc")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

type cliResult struct {
	code           int
	stdout, stderr string
}

// cli runs `meilisearch-cdc admin` in process against the env's data directory.
func (e *env) cli(stdin string, args ...string) cliResult {
	e.t.Helper()
	var out, errb bytes.Buffer
	full := append([]string{"-data-dir", e.app.Config.DataDir, "-secret-key", e.app.Config.SecretKey}, args...)
	code := admincli.Run(context.Background(), full, admincli.IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb})
	return cliResult{code, out.String(), errb.String()}
}

func (e *env) mustCLI(stdin string, args ...string) string {
	e.t.Helper()
	r := e.cli(stdin, args...)
	require.Equal(e.t, 0, r.code, "admin %v: %s%s", args, r.stdout, r.stderr)
	return r.stdout
}

func (e *env) indexExists(uid string) bool {
	_, err := e.meili.GetIndex(context.Background(), uid)
	return err == nil
}

func TestAdminCLIThroughSocket(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.DataDir = shortDataDir(t) })
	sock := filepath.Join(e.app.Config.DataDir, "admin.sock")
	st, err := os.Stat(sock)
	require.NoError(t, err, "the running service serves the admin socket")
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	assert.NotZero(t, st.Mode()&os.ModeSocket)

	// The owner signs up in the dashboard; the CLI sees the running service's data.
	owner := newBrowser(t, e)
	require.Equal(t, 200, owner.code("POST", "/api/v1/auth/setup", map[string]any{"email": "owner@example.com", "name": "Olive", "password": "password123"}))
	out := e.mustCLI("", "users", "list")
	assert.Regexp(t, `owner@example.com\s+Olive\s+admin\s+yes`, out)

	out = e.mustCLI("", "users", "create", "--email", "ops@example.com", "--name", "Ops", "--role", "admin", "--generate")
	assert.Contains(t, out, "created ops@example.com (admin)")
	assert.NotContains(t, out, "primary administrator")
	pw := strings.TrimSpace(out[strings.LastIndex(out, ":")+1:])
	ops := newBrowser(t, e)
	require.Equal(t, 200, ops.code("POST", "/api/v1/auth/login", map[string]any{"email": "ops@example.com", "password": pw}), "the generated password works")

	// Owner rules hold for the CLI too, with clear errors and exit code 1.
	r := e.cli("", "users", "set-role", "--email", "owner@example.com", "--role", "viewer")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "primary administrator's role cannot be changed")
	r = e.cli("", "users", "delete", "--email", "owner@example.com")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "cannot be deleted")

	// Resetting the owner's password signs every owner session out.
	out = e.mustCLI("new owner password\n", "users", "reset-password", "--email", "owner@example.com", "--password-file", "-")
	assert.Contains(t, out, "signed them out everywhere")
	assert.Equal(t, 401, owner.code("GET", "/api/v1/auth/me", nil))
	require.Equal(t, 200, owner.code("POST", "/api/v1/auth/login", map[string]any{"email": "owner@example.com", "password": "new owner password"}))

	out = e.mustCLI("", "users", "set-owner", "--email", "ops@example.com")
	assert.Contains(t, out, "ops@example.com is now the primary administrator")
	var users []map[string]any
	owner.do("GET", "/api/v1/users", nil, &users)
	for _, u := range users {
		assert.Equal(t, u["email"] == "ops@example.com", u["owner"] == true, "%v", u)
	}

	out = e.mustCLI("", "sessions", "revoke", "--email", "ops@example.com")
	assert.Contains(t, out, "revoked 1 session of ops@example.com")
	assert.Equal(t, 401, ops.code("GET", "/api/v1/auth/me", nil))
	assert.Equal(t, 200, owner.code("GET", "/api/v1/auth/me", nil), "others stay signed in")

	mcpConf := e.app.MCP.Config()
	mcpConf.Enabled = true
	_, err = e.app.MCP.SetConfig(mcpConf)
	require.NoError(t, err)
	e.mustCLI("", "mcp", "disable")
	assert.False(t, e.app.MCP.Config().Enabled)

	// Purge goes through the running engine: the task stops, then goes away
	// with its index.
	table, index := uniq("purge"), uniq("idx")
	seed(table, 20)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 20)
	r = e.cli("", "purge", "connectors")
	assert.Equal(t, 1, r.code, "the confirmation is required")
	assert.Contains(t, r.stderr, "nothing was deleted")
	r = e.cli("", "purge", "connectors", "--yes")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "purge tasks in the same command")
	out = e.mustCLI("purge\n", "purge", "tasks", "--indexes")
	assert.Regexp(t, `tasks\s+1`, out)
	var tasks []model.TaskView
	e.mustDo("GET", "/api/v1/tasks", nil, &tasks, 200)
	assert.Empty(t, tasks)
	assert.False(t, e.app.Engine.IndexBusy("", index))
	eventually(t, 30*time.Second, func() bool { return !e.indexExists(index) }, func() string { return "the index was not deleted" })

	// JSON output.
	r = e.cli("", "--json", "purge", "sessions", "invitations", "--yes")
	require.Equal(t, 0, r.code, r.stderr)
	var res struct {
		Purged []struct {
			Scope string
			Count int
		}
	}
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &res))
	require.Len(t, res.Purged, 2)
	assert.Equal(t, "sessions", res.Purged[0].Scope)
	assert.Equal(t, 401, owner.code("GET", "/api/v1/auth/me", nil), "everyone is signed out")

	// Every CLI action is in the audit log as "cli".
	entries, err := e.app.Audit.List(500, "", "cli")
	require.NoError(t, err)
	actions := map[string]bool{}
	for _, en := range entries {
		actions[en.Action] = true
		assert.Equal(t, "cli", en.UserEmail)
	}
	for _, a := range []string{"cli.users.list", "cli.users.create", "cli.users.reset-password", "cli.users.set-owner", "cli.users.set-role",
		"cli.sessions.revoke", "cli.mcp.disable", "cli.purge"} {
		assert.True(t, actions[a], "audit has %s", a)
	}
}

func TestAdminCLIDirectModeWhileStopped(t *testing.T) {
	dir := shortDataDir(t)
	cfg := config.Config{DataDir: dir, MeiliURL: meiliURL, MeiliAPIKey: masterKey, MeiliGzip: true, MeiliTimeout: 30 * time.Second,
		SecretKey: "secret", StopTimeout: 10 * time.Second, AdminEmail: "boot@example.com", AdminPassword: "password123"}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	start := func() (*app.App, *httptest.Server) {
		a, err := app.New(cfg, log)
		require.NoError(t, err)
		require.NoError(t, a.Start(context.Background()))
		return a, httptest.NewServer(a.Handler)
	}
	stop := func(a *app.App, srv *httptest.Server) {
		srv.Close()
		require.NoError(t, a.Close(context.Background()))
	}
	a, srv := start()
	e := &env{t: t, app: a, srv: srv}
	owner, ok, err := a.Auth.Owner()
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, "boot@example.com", owner.Email, "the CDC_ADMIN_EMAIL admin is the owner")
	_, err = a.Auth.CreateUser("other@example.com", "Other", "admin", "password123")
	require.NoError(t, err)
	_, _, err = a.Auth.CreateSession(owner.ID, "", "")
	require.NoError(t, err)
	table, index := uniq("direct"), uniq("idx")
	seed(table, 5)
	var task model.Task
	raw, _ := json.Marshal(memTask(table, index))
	require.NoError(t, json.Unmarshal(raw, &task))
	_, err = a.Engine.Create(task, false)
	require.NoError(t, err)
	stop(a, srv)
	_, err = os.Stat(filepath.Join(dir, "admin.sock"))
	assert.ErrorIs(t, err, os.ErrNotExist, "the socket is removed on shutdown")

	// The service is stopped: the CLI opens the store itself.
	out := e.mustCLI("", "users", "list")
	assert.Regexp(t, `boot@example.com\s+Administrator\s+admin\s+yes`, out)
	e.mustCLI("", "users", "set-owner", "--email", "other@example.com")
	e.mustCLI("", "users", "set-role", "--email", "boot@example.com", "--role", "moderator")
	out = e.mustCLI("", "sessions", "revoke")
	assert.Contains(t, out, "revoked 1 session")
	out = e.mustCLI("", "purge", "tasks", "audit", "--yes")
	assert.Regexp(t, `tasks\s+1`, out)

	// The next start sees the changes.
	a, srv = start()
	defer stop(a, srv)
	owner, _, _ = a.Auth.Owner()
	assert.Equal(t, "other@example.com", owner.Email)
	u, _, _ := a.Auth.UserByEmail("boot@example.com")
	assert.Equal(t, "moderator", string(u.Role))
	tasks, _ := a.Engine.List("", "", "")
	assert.Empty(t, tasks)
	entries, _ := a.Audit.List(100, "", "")
	require.Len(t, entries, 1, "audit was purged; only the purge itself remains")
	assert.Equal(t, audit.Entry{UserID: "cli", UserEmail: "cli", Action: "cli.purge", Target: "tasks,audit"},
		audit.Entry{UserID: entries[0].UserID, UserEmail: entries[0].UserEmail, Action: entries[0].Action, Target: entries[0].Target})
	assert.Contains(t, entries[0].Details, "purged tasks: 1, audit: ")

	// purge all while running: the data directory is as good as new.
	e.app = a
	out = e.mustCLI("purge\n", "purge", "all")
	assert.Regexp(t, `users\s+2`, out)
	has, _ := a.Auth.HasUsers()
	assert.False(t, has)
	dests, _ := a.Engine.Destinations().List()
	require.Len(t, dests, 1, "the MEILI_URL destination is recreated")
}

func TestOwnerProtectionAPI(t *testing.T) {
	e := newEnv(t, func(c *config.Config) { c.DataDir = shortDataDir(t) })
	owner := newBrowser(t, e)
	var me struct{ User map[string]any }
	code, _ := owner.do("POST", "/api/v1/auth/setup", map[string]any{"email": "owner@example.com", "name": "O", "password": "password123"}, &me)
	require.Equal(t, 200, code)
	assert.Equal(t, true, me.User["owner"])
	ownerID := me.User["id"].(string)
	var other map[string]any
	code, _ = owner.do("POST", "/api/v1/users", map[string]any{"email": "b@example.com", "role": "admin", "password": "password123"}, &other)
	require.Equal(t, 201, code)
	assert.Nil(t, other["owner"])
	b := newBrowser(t, e)
	require.Equal(t, 200, b.code("POST", "/api/v1/auth/login", map[string]any{"email": "b@example.com", "password": "password123"}))

	var errBody map[string]string
	code, _ = b.do("PATCH", "/api/v1/users/"+ownerID, map[string]any{"role": "viewer"}, &errBody)
	assert.Equal(t, 403, code)
	assert.Contains(t, errBody["error"], "primary administrator's role cannot be changed")
	code, _ = b.do("DELETE", "/api/v1/users/"+ownerID, nil, &errBody)
	assert.Equal(t, 403, code)
	assert.Contains(t, errBody["error"], "cannot be deleted")
	code, _ = b.do("POST", "/api/v1/users/"+ownerID+"/password", map[string]any{"password": "hijacked123"}, &errBody)
	assert.Equal(t, 403, code)
	assert.Contains(t, errBody["error"], "admin users reset-password")
	code, _ = b.do("PATCH", "/api/v1/users/"+other["id"].(string), map[string]any{"role": "viewer"}, &errBody)
	assert.Equal(t, 403, code)
	assert.Contains(t, errBody["error"], "your own role")
	assert.Equal(t, 403, e.do("PATCH", "/api/v1/users/"+ownerID, map[string]any{"role": "viewer"}, nil), "the API token is refused too")

	assert.Equal(t, 200, owner.code("PATCH", "/api/v1/users/"+ownerID, map[string]any{"name": "Olive"}), "renaming is fine")
	assert.Equal(t, 200, owner.code("PATCH", "/api/v1/users/"+other["id"].(string), map[string]any{"role": "moderator"}))
	assert.Equal(t, 204, owner.code("POST", "/api/v1/users/"+ownerID+"/password", map[string]any{"password": "owner sets own"}))
	assert.Equal(t, 200, owner.code("GET", "/api/v1/auth/me", nil), "the owner's own session survives")
}
