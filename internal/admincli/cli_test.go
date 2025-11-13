package admincli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/app"
	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

type result struct {
	code           int
	stdout, stderr string
}

func run(dir, stdin string, args ...string) result {
	var out, errb bytes.Buffer
	code := Run(context.Background(), append([]string{"-data-dir", dir}, args...), IO{Stdin: strings.NewReader(stdin), Stdout: &out, Stderr: &errb})
	return result{code, out.String(), errb.String()}
}

// dataDir returns a data directory the service has initialised.
func dataDir(t *testing.T) string {
	dir := t.TempDir()
	cfg := config.FromEnv()
	cfg.DataDir, cfg.MeiliURL, cfg.AdminEmail = dir, "", ""
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, a.Close(context.Background()))
	return dir
}

func TestHelpAndUsage(t *testing.T) {
	dir := t.TempDir()
	r := run(dir, "", "--help")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "users reset-password")
	assert.Contains(t, r.stdout, "purge <scope>...")
	assert.Empty(t, r.stderr)

	r = run(dir, "")
	assert.Equal(t, 2, r.code)
	assert.Contains(t, r.stderr, "Usage: meilisearch-cdc admin")

	r = run(dir, "", "users", "reset-password", "--help")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "-generate")
	assert.Contains(t, r.stdout, "-password-file")

	r = run(dir, "", "help", "purge")
	assert.Equal(t, 0, r.code)
	assert.Contains(t, r.stdout, "alert-events")
	assert.Contains(t, r.stdout, "the secret key file stays")

	r = run(dir, "", "users")
	assert.Equal(t, 2, r.code)
	assert.Contains(t, r.stderr, "users set-owner")
	assert.Equal(t, 0, run(dir, "", "users", "--help").code)

	r = run(dir, "", "users", "frobnicate")
	assert.Equal(t, 2, r.code)
	assert.Contains(t, r.stderr, `unknown command "users frobnicate"`)
	assert.Equal(t, 2, run(dir, "", "nope").code)
	assert.Equal(t, 2, run(dir, "", "users", "list", "extra").code)
	assert.Equal(t, 2, run(dir, "", "users", "delete").code, "--email is required")
}

func TestDirectModeCommands(t *testing.T) {
	dir := dataDir(t)

	r := run(dir, "", "users", "list")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "No users")

	r = run(dir, "a very good password\n", "users", "create", "--email", "boss@example.com", "--name", "Boss", "--role", "admin")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "created boss@example.com (admin) as the primary administrator")

	r = run(dir, "", "users", "create", "--email", "v@example.com", "--role", "viewer", "--generate", "-json")
	require.Equal(t, 0, r.code, r.stderr)
	var created struct {
		User     map[string]any `json:"user"`
		Password string         `json:"password"`
	}
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &created))
	assert.Len(t, created.Password, 24)
	assert.Equal(t, "viewer", created.User["role"])

	pwFile := filepath.Join(t.TempDir(), "pw")
	require.NoError(t, os.WriteFile(pwFile, []byte("from a file 123\n"), 0o600))
	r = run(dir, "", "users", "reset-password", "--email", "v@example.com", "--password-file", pwFile)
	require.Equal(t, 0, r.code, r.stderr)
	assert.Contains(t, r.stdout, "signed them out everywhere")
	assert.Equal(t, 2, run(dir, "", "users", "reset-password", "--email", "v@example.com", "--password-file", pwFile, "--generate").code)
	r = run(dir, "", "users", "reset-password", "--email", "v@example.com")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "no password given")

	r = run(dir, "", "users", "set-role", "--email", "boss@example.com", "--role", "viewer")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "primary administrator's role cannot be changed")

	r = run(dir, "", "-json", "users", "list")
	require.Equal(t, 0, r.code, r.stderr)
	var list struct{ Users []map[string]any }
	require.NoError(t, json.Unmarshal([]byte(r.stdout), &list))
	require.Len(t, list.Users, 2)
	assert.Equal(t, true, list.Users[0]["owner"])

	r = run(dir, "", "users", "list")
	assert.Regexp(t, `boss@example.com\s+Boss\s+admin\s+yes`, r.stdout)
}

func TestPurgeConfirmation(t *testing.T) {
	dir := dataDir(t)
	require.Equal(t, 0, run(dir, "password123\n", "users", "create", "--email", "a@example.com", "--role", "admin").code)

	r := run(dir, "no\n", "purge", "users")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, `Type "purge" to confirm`)
	assert.Contains(t, r.stderr, "nothing was deleted")
	assert.Contains(t, run(dir, "", "users", "list").stdout, "a@example.com")

	r = run(dir, "", "purge", "users", "sessions", "--bogus")
	assert.Equal(t, 2, r.code)

	r = run(dir, "purge\n", "purge", "users", "sessions")
	require.Equal(t, 0, r.code, r.stderr)
	assert.Regexp(t, `sessions\s+0`, r.stdout)
	assert.Regexp(t, `users\s+1`, r.stdout)
	assert.Contains(t, run(dir, "", "users", "list").stdout, "No users")

	r = run(dir, "", "purge", "all", "--yes")
	require.Equal(t, 0, r.code, r.stderr)
	assert.FileExists(t, filepath.Join(dir, "secret.key"), "purge all keeps the key")
}

func TestLockedStore(t *testing.T) {
	dir := dataDir(t)
	// A service that holds the store but serves no admin socket.
	db, err := store.Open(filepath.Join(dir, "cdc.db"))
	require.NoError(t, err)
	defer db.Close()
	r := run(dir, "", "users", "list")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "is locked by a running service")

	r = run(t.TempDir(), "", "users", "list")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "no database at")
}

func TestNeedsTheServiceKey(t *testing.T) {
	t.Setenv("CDC_SECRET_KEY", "")
	t.Setenv("CDC_SECRET_KEY_FILE", "")
	dir := t.TempDir()
	cfg := config.FromEnv()
	cfg.DataDir, cfg.MeiliURL, cfg.AdminEmail, cfg.SecretKey = dir, "", "", "the service key"
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.NoError(t, a.Close(context.Background()))

	r := run(dir, "", "users", "list")
	assert.Equal(t, 1, r.code)
	assert.Contains(t, r.stderr, "no secret key")
	assert.NoFileExists(t, filepath.Join(dir, "secret.key"), "the CLI does not invent a key")
	r = run(dir, "", "-secret-key", "the service key", "users", "list")
	assert.Equal(t, 0, r.code, r.stderr)
}
