package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestLoadEnvAndFlags(t *testing.T) {
	t.Setenv("MEILI_URL", "http://meili:7700")
	t.Setenv("CDC_STOP_TIMEOUT", "5s")
	t.Setenv("MEILI_GZIP", "false")
	c, err := Load([]string{"-listen", ":9999"})
	require.NoError(t, err)
	assert.Equal(t, ":9999", c.Listen)
	assert.Equal(t, "http://meili:7700", c.MeiliURL)
	assert.Equal(t, 5*time.Second, c.StopTimeout)
	assert.False(t, c.MeiliGzip)
	assert.Equal(t, filepath.Join("./data", "cdc.db"), c.DBPath())

	c, err = Load([]string{"-meili-url", ""})
	require.NoError(t, err, "MEILI_URL is optional: destinations can be managed in the dashboard")
	assert.Empty(t, c.MeiliURL)
}

func TestLoadTasks(t *testing.T) {
	t.Setenv("PG_DSN", "postgres://u:p@db/app")
	path := filepath.Join(t.TempDir(), "tasks.yaml")
	require.NoError(t, os.WriteFile(path, []byte(`
tasks:
  - id: products
    name: Products
    desiredState: running
    source: {type: postgres, dsn: "${PG_DSN}", table: products}
    index:
      uid: products
      settings: {filterableAttributes: [price]}
    sync: {flushInterval: 250ms, safety: {maxDeleteRatio: 0.2}}
`), 0o600))
	tasks, err := LoadTasks(path)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	tk := tasks[0]
	assert.Equal(t, "postgres://u:p@db/app", tk.Source.DSN)
	assert.Equal(t, model.StateRunning, tk.Desired)
	assert.Equal(t, 250*time.Millisecond, tk.Sync.FlushInterval.D())
	assert.JSONEq(t, `{"filterableAttributes":["price"]}`, string(tk.Index.Settings))
	assert.Equal(t, 0.2, tk.Sync.Safety.MaxDeleteRatio)

	require.NoError(t, os.WriteFile(path, []byte("tasks:\n  - name: no id\n"), 0o600))
	_, err = LoadTasks(path)
	assert.ErrorContains(t, err, "id is required")
}
