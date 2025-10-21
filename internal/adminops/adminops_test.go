package adminops_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/adminops"
	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/mcpserver"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/secret"
	"github.com/ja7ad/meilisearch-cdc/internal/store"

	_ "github.com/ja7ad/meilisearch-cdc/internal/source/memory"
)

type fixture struct {
	t   *testing.T
	db  *store.DB
	ops *adminops.Ops
	d   adminops.Deps
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "cdc.db"))
	require.NoError(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	cipher, err := secret.New("test secret", "")
	require.NoError(t, err)
	dests, err := dest.New(db, cipher, log)
	require.NoError(t, err)
	eng, err := engine.New(engine.Config{Destinations: dests, DB: db, Cipher: cipher, Logger: log, StopTimeout: time.Second})
	require.NoError(t, err)
	users, err := auth.NewService(db)
	require.NoError(t, err)
	notifier, err := notify.NewService(db, cipher, "")
	require.NoError(t, err)
	al, err := audit.New(db)
	require.NoError(t, err)
	am, err := alerts.New(db, nil, notifier, nil, log)
	require.NoError(t, err)
	mcp, err := mcpserver.New(db, eng, am, al, log)
	require.NoError(t, err)
	t.Cleanup(func() {
		eng.Shutdown(context.Background())
		db.Close()
	})
	d := adminops.Deps{Auth: users, Engine: eng, Notify: notifier, Alerts: am, Audit: al, MCP: mcp, DB: db}
	return &fixture{t: t, db: db, ops: adminops.New(d), d: d}
}

func (f *fixture) do(req adminops.Request) adminops.Result {
	f.t.Helper()
	res, err := f.ops.Do(context.Background(), req)
	require.NoError(f.t, err, "%s", req.Command)
	return res
}

func (f *fixture) fail(req adminops.Request) error {
	f.t.Helper()
	_, err := f.ops.Do(context.Background(), req)
	require.Error(f.t, err, "%s", req.Command)
	return err
}

// raw opens a bucket without its owner's types, to seed or count records.
func (f *fixture) raw(name string) *store.Bucket[map[string]any] {
	b, err := store.NewBucket[map[string]any](f.db, name)
	require.NoError(f.t, err)
	return b
}

func (f *fixture) count(name string) int {
	n, err := f.raw(name).Count("")
	require.NoError(f.t, err)
	return n
}

func (f *fixture) seed(name string, keys ...string) {
	for _, k := range keys {
		require.NoError(f.t, f.raw(name).Put(k, map[string]any{"seeded": true}))
	}
}

// populate stores something in every scope.
func (f *fixture) populate() (taskID string) {
	t := f.t
	owner, err := f.d.Auth.Setup("owner@x.io", "Owner", "password123")
	require.NoError(t, err)
	_, _, err = f.d.Auth.CreateSession(owner.ID, "", "")
	require.NoError(t, err)
	_, _, err = f.d.Auth.CreateInvitation("guest@x.io", auth.Viewer, owner.Email)
	require.NoError(t, err)
	_, err = f.d.Engine.Destinations().Create(model.Destination{Name: "Main", URL: "http://127.0.0.1:1"})
	require.NoError(t, err)
	var task model.Task
	require.NoError(t, json.Unmarshal([]byte(`{"name":"memory","source":{"type":"memory","table":"purge_t"},"index":{"uid":"purge_idx"}}`), &task))
	v, err := f.d.Engine.Create(task, false)
	require.NoError(t, err)
	_, err = f.d.Engine.CreateConnector(model.Connector{Name: "conn", Type: model.SourceType("memory")})
	require.NoError(t, err)
	f.seed("checkpoints", v.ID)
	f.seed("status", v.ID)
	f.seed("dlq", v.ID+"/0001", v.ID+"/0002")
	f.seed("diffs", v.ID)
	_, err = f.d.Alerts.Create(alerts.Rule{Name: "failed", Enabled: true, Condition: alerts.Condition{Type: "task_state", States: []model.State{model.StateFailed}},
		Channels: alerts.Channels{WebhookURL: "https://hooks.example.com/x"}})
	require.NoError(t, err)
	f.seed("alert_events", "e1", "e2")
	f.seed("alert_states", "r|t")
	_, _, err = f.d.MCP.CreateToken("agent", auth.Viewer, 0, owner.Email)
	require.NoError(t, err)
	_, err = f.d.MCP.SetConfig(mcpserver.Config{Enabled: true})
	require.NoError(t, err)
	_, err = f.d.Notify.SetSMTP(notify.SMTPConfig{Host: "smtp.example.com", Port: 587, From: "cdc@example.com"})
	require.NoError(t, err)
	f.d.Audit.Record(audit.Entry{Action: "seed"})
	return v.ID
}

func TestPurgeEachScopeClearsOnlyItsData(t *testing.T) {
	buckets := []string{"tasks", "status", "checkpoints", "dlq", "diffs", "connectors", "destinations", "alert_rules", "alert_events",
		"alert_states", "mcp_tokens", "mcp_config", "settings_smtp", "sessions", "invitations", "users", "user_emails", "audit"}
	cases := []struct {
		scope   string
		cleared []string
		count   int
	}{
		{"tasks", []string{"tasks", "status", "checkpoints", "dlq", "diffs"}, 1},
		{"checkpoints", []string{"checkpoints"}, 1},
		{"dlq", []string{"dlq"}, 2},
		{"diffs", []string{"diffs"}, 1},
		{"logs", nil, 1}, // "task created", in memory only
		{"alert-rules", []string{"alert_rules", "alert_states"}, 1},
		{"alert-events", []string{"alert_events", "alert_states"}, 2},
		{"mcp-tokens", []string{"mcp_tokens"}, 1},
		{"settings", []string{"mcp_config", "settings_smtp"}, 2},
		{"sessions", []string{"sessions"}, 1},
		{"invitations", []string{"invitations"}, 1},
		{"users", []string{"users", "user_emails", "sessions", "invitations"}, 1},
		{"audit", nil, 0}, // checked below: only the purge's own entry remains
	}
	for _, c := range cases {
		t.Run(c.scope, func(t *testing.T) {
			f := newFixture(t)
			f.populate()
			before := map[string]int{}
			for _, b := range buckets {
				before[b] = f.count(b)
				require.NotZero(t, before[b], "fixture seeds %s", b)
			}
			res := f.do(adminops.Request{Command: adminops.Purge, Scopes: []string{c.scope}})
			require.Len(t, res.Purged, 1)
			assert.Equal(t, c.scope, res.Purged[0].Scope)
			if c.scope != "audit" {
				assert.Equal(t, c.count, res.Purged[0].Count)
			}
			for _, b := range buckets {
				switch {
				case b == "audit" && c.scope == "audit":
					assert.Equal(t, 1, f.count(b), "only the purge itself is recorded")
				case b == "audit":
					assert.Equal(t, before[b]+1, f.count(b), "the purge is audited")
				case contains(c.cleared, b):
					assert.Zero(t, f.count(b), "%s clears %s", c.scope, b)
				default:
					assert.Equal(t, before[b], f.count(b), "%s keeps %s", c.scope, b)
				}
			}
		})
	}
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestPurgeConnectorsAndDestinationsNeedTasksPurged(t *testing.T) {
	f := newFixture(t)
	f.populate()
	for _, scope := range []string{"connectors", "destinations"} {
		err := f.fail(adminops.Request{Command: adminops.Purge, Scopes: []string{scope}})
		assert.Contains(t, err.Error(), "purge tasks in the same command")
		assert.NotZero(t, f.count(scope), "nothing was purged")
	}
	res := f.do(adminops.Request{Command: adminops.Purge, Scopes: []string{"destinations", "connectors", "tasks"}})
	scopes := []string{}
	for _, p := range res.Purged {
		scopes = append(scopes, p.Scope)
	}
	assert.Equal(t, []string{"tasks", "connectors", "destinations"}, scopes, "tasks go first")
	assert.Zero(t, f.count("connectors"))
	assert.Zero(t, f.count("connector_tests"))
	assert.Zero(t, f.count("destinations"))
	assert.Zero(t, f.count("tasks"))
}

func TestPurgeAllLeavesNothing(t *testing.T) {
	f := newFixture(t)
	f.populate()
	f.seed("password_resets", "r1")
	res := f.do(adminops.Request{Command: adminops.Purge, Scopes: []string{"all"}})
	assert.Len(t, res.Purged, len(adminops.Scopes))
	for _, b := range []string{"tasks", "connectors", "destinations", "users", "password_resets", "mcp_config", "settings_smtp", "alert_rules"} {
		assert.Zero(t, f.count(b), b)
	}
	assert.Equal(t, 1, f.count("audit"), "the purge itself")
	has, _ := f.d.Auth.HasUsers()
	assert.False(t, has, "the setup page shows again")
	assert.False(t, f.d.MCP.Config().Enabled)
	assert.False(t, f.d.Notify.EmailConfigured(), "cached settings are dropped")
}

func TestPurgeRejectsBadScopes(t *testing.T) {
	f := newFixture(t)
	assert.Contains(t, f.fail(adminops.Request{Command: adminops.Purge}).Error(), "at least one scope")
	assert.Contains(t, f.fail(adminops.Request{Command: adminops.Purge, Scopes: []string{"nope"}}).Error(), `unknown scope "nope"`)
	assert.Contains(t, f.fail(adminops.Request{Command: adminops.Purge, Scopes: []string{"dlq"}, Indexes: true}).Error(), "--indexes")
	got, err := adminops.ParseScopes([]string{"audit", "tasks", "audit"})
	require.NoError(t, err)
	assert.Equal(t, []string{"tasks", "audit"}, got)
}

func TestPurgeCheckpointsRestartsRunningTasks(t *testing.T) {
	f := newFixture(t)
	id := f.populate()
	require.NoError(t, f.d.Engine.Start(context.Background()))
	require.NoError(t, f.d.Engine.StartTask(id))
	f.do(adminops.Request{Command: adminops.Purge, Scopes: []string{"checkpoints"}})
	v, err := f.d.Engine.View(id)
	require.NoError(t, err)
	assert.Equal(t, model.StateRunning, v.Desired, "the task keeps running")
	logs := f.d.Engine.Logs(id, 0)
	require.NotEmpty(t, logs)
	n := f.do(adminops.Request{Command: adminops.Purge, Scopes: []string{"logs"}}).Purged[0].Count
	assert.NotZero(t, n)
	assert.Empty(t, f.d.Engine.Logs("", 0))
}

func TestUserCommandsAndOwnerRules(t *testing.T) {
	f := newFixture(t)
	res := f.do(adminops.Request{Command: adminops.UsersCreate, Email: "first@x.io", Name: "First", Role: "admin", Password: "password123"})
	require.NotNil(t, res.User)
	assert.True(t, res.User.Owner, "the first admin becomes the owner, even when created by the CLI")
	f.do(adminops.Request{Command: adminops.UsersCreate, Email: "second@x.io", Role: "admin", Password: "password123"})
	f.do(adminops.Request{Command: adminops.UsersCreate, Email: "mod@x.io", Role: "moderator", Password: "password123"})
	assert.Contains(t, f.fail(adminops.Request{Command: adminops.UsersCreate, Email: "bad@x.io", Role: "root", Password: "password123"}).Error(), "unknown role")
	assert.Contains(t, f.fail(adminops.Request{Command: adminops.UsersCreate, Email: "weak@x.io", Role: "viewer", Password: "short"}).Error(), "password")

	list := f.do(adminops.Request{Command: adminops.UsersList}).Users
	require.Len(t, list, 3)
	assert.Equal(t, "first@x.io", list[0].Email, "oldest first")

	err := f.fail(adminops.Request{Command: adminops.UsersSetRole, Email: "first@x.io", Role: "viewer"})
	assert.ErrorContains(t, err, "primary administrator's role cannot be changed")
	assert.ErrorContains(t, f.fail(adminops.Request{Command: adminops.UsersDelete, Email: "first@x.io"}), "cannot be deleted")
	assert.ErrorContains(t, f.fail(adminops.Request{Command: adminops.UsersSetOwner, Email: "mod@x.io"}), "must be an admin")
	assert.ErrorContains(t, f.fail(adminops.Request{Command: adminops.UsersSetRole, Email: "nobody@x.io", Role: "viewer"}), "no user with email nobody@x.io")
	assert.ErrorContains(t, f.fail(adminops.Request{Command: adminops.UsersDelete}), "--email is required")

	res = f.do(adminops.Request{Command: adminops.UsersSetOwner, Email: "SECOND@x.io"})
	assert.True(t, res.User.Owner)
	res = f.do(adminops.Request{Command: adminops.UsersSetRole, Email: "first@x.io", Role: "viewer"})
	assert.Equal(t, auth.Viewer, res.User.Role, "the former owner is an ordinary user")
	f.do(adminops.Request{Command: adminops.UsersDelete, Email: "first@x.io"})
	_, ok, _ := f.d.Auth.UserByEmail("first@x.io")
	assert.False(t, ok)

	entries, err := f.d.Audit.List(100, "", adminops.Actor)
	require.NoError(t, err)
	actions := map[string]int{}
	for _, e := range entries {
		actions[e.Action]++
	}
	assert.Equal(t, 5, actions["cli.users.create"], "failures are audited too")
	assert.Equal(t, 2, actions["cli.users.set-owner"], "one refused, one done")
	assert.NotZero(t, actions["cli.users.list"])
}

func TestResetPasswordRevokesSessions(t *testing.T) {
	f := newFixture(t)
	owner, err := f.d.Auth.Setup("owner@x.io", "O", "password123")
	require.NoError(t, err)
	other, err := f.d.Auth.CreateUser("b@x.io", "B", auth.Viewer, "password123")
	require.NoError(t, err)
	t1, _, _ := f.d.Auth.CreateSession(owner.ID, "", "")
	t2, _, _ := f.d.Auth.CreateSession(owner.ID, "", "")
	t3, _, _ := f.d.Auth.CreateSession(other.ID, "", "")

	// The CLI resets the primary administrator's password, which no API caller can.
	f.do(adminops.Request{Command: adminops.UsersResetPassword, Email: "owner@x.io", Password: "brand new password"})
	for _, tok := range []string{t1, t2} {
		_, err := f.d.Auth.ResolveSession(tok)
		assert.ErrorIs(t, err, auth.ErrInvalidToken)
	}
	_, err = f.d.Auth.ResolveSession(t3)
	assert.NoError(t, err, "other users stay signed in")
	_, err = f.d.Auth.Authenticate("owner@x.io", "brand new password")
	assert.NoError(t, err)

	res := f.do(adminops.Request{Command: adminops.SessionsRevoke, Email: "b@x.io"})
	assert.Equal(t, 1, res.Count)
	_, _, _ = f.d.Auth.CreateSession(owner.ID, "", "")
	_, _, _ = f.d.Auth.CreateSession(other.ID, "", "")
	res = f.do(adminops.Request{Command: adminops.SessionsRevoke})
	assert.Equal(t, 2, res.Count)
	assert.Zero(t, f.count("sessions"))
}

func TestMCPCommands(t *testing.T) {
	f := newFixture(t)
	_, err := f.d.MCP.SetConfig(mcpserver.Config{Enabled: true})
	require.NoError(t, err)
	_, _, err = f.d.MCP.CreateToken("a", auth.Viewer, 0, "x")
	require.NoError(t, err)
	_, _, err = f.d.MCP.CreateToken("b", auth.Admin, 0, "x")
	require.NoError(t, err)
	f.do(adminops.Request{Command: adminops.MCPDisable})
	assert.False(t, f.d.MCP.Config().Enabled)
	assert.NotEmpty(t, f.d.MCP.Config().Policy[auth.Viewer].Tools, "the policy stays")
	assert.Equal(t, 2, f.do(adminops.Request{Command: adminops.MCPRevokeTokens}).Count)
	toks, _ := f.d.MCP.Tokens()
	assert.Empty(t, toks)
	assert.ErrorContains(t, f.fail(adminops.Request{Command: "nope"}), "unknown command")
}

func TestSocketRoundTrip(t *testing.T) {
	f := newFixture(t)
	dir, err := os.MkdirTemp("", "cdcsock")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, adminops.SocketName)

	_, err = adminops.Call(context.Background(), path, adminops.Request{Command: adminops.UsersList})
	assert.ErrorIs(t, err, adminops.ErrNoServer)

	l, err := adminops.Listen(path)
	require.NoError(t, err)
	st, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), st.Mode().Perm())
	srv := &http.Server{Handler: f.ops.Handler()}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	_, err = adminops.Listen(path)
	assert.ErrorContains(t, err, "another service is listening")

	res, err := adminops.Call(context.Background(), path, adminops.Request{Command: adminops.UsersCreate, Email: "a@x.io", Role: "admin", Password: "password123"})
	require.NoError(t, err)
	assert.True(t, res.User.Owner)
	_, err = adminops.Call(context.Background(), path, adminops.Request{Command: adminops.UsersDelete, Email: "a@x.io"})
	assert.ErrorContains(t, err, "primary administrator cannot be deleted")

	// A socket left by a crashed service is replaced.
	srv.Close()
	stale, err := net.Listen("unix", path)
	require.NoError(t, err)
	stale.(*net.UnixListener).SetUnlinkOnClose(false)
	stale.Close()
	_, err = os.Stat(path)
	require.NoError(t, err, "the stale socket file exists")
	l2, err := adminops.Listen(path)
	require.NoError(t, err)
	l2.Close()
}

// A data directory too deep for a unix socket address still gets a working
// admin socket, in a private directory both the service and the CLI find.
func TestSocketPathLongDataDir(t *testing.T) {
	short := t.TempDir()
	assert.Equal(t, filepath.Join(short, adminops.SocketName), adminops.SocketPath(short))

	long := filepath.Join(t.TempDir(), strings.Repeat("deep-directory/", 8))
	require.NoError(t, os.MkdirAll(long, 0o700))
	p := adminops.SocketPath(long)
	assert.LessOrEqual(t, len(p), 103)
	assert.Equal(t, p, adminops.SocketPath(long), "stable")
	assert.NotEqual(t, p, adminops.SocketPath(long+"x"))

	l, err := adminops.Listen(p)
	require.NoError(t, err)
	defer l.Close()
	fi, err := os.Stat(filepath.Dir(p))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o700), fi.Mode().Perm())

	_, err = adminops.Call(context.Background(), adminops.SocketPath(t.TempDir()+strings.Repeat("/other", 20)), adminops.Request{})
	assert.ErrorIs(t, err, adminops.ErrNoServer, "no service for that data directory")
}
