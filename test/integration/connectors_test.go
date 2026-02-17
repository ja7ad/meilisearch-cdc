//go:build integration

package integration

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/discover"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestConnectors(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "shop.db")
	db := openSQLite(t, path)
	for _, q := range []string{
		`CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT, version INTEGER NOT NULL)`,
		`CREATE TABLE orders (id INTEGER PRIMARY KEY, total REAL, version INTEGER NOT NULL)`,
		`CREATE VIEW cheap AS SELECT * FROM products`,
	} {
		_, err := db.Exec(q)
		require.NoError(t, err)
	}
	for i := 1; i <= 30; i++ {
		_, err := db.Exec(`INSERT INTO products VALUES (?, ?, ?)`, i, fmt.Sprintf("p%d", i), i)
		require.NoError(t, err)
	}

	// Test an unsaved connector: connection, version and tables.
	conn := map[string]any{"name": "Shop SQLite", "type": "sqlpoll", "tags": []string{"shop"}, "config": map[string]any{"driver": "sqlite", "dsn": path}}
	var res discover.Result
	e.mustDo("POST", "/api/v1/connectors/test", conn, &res, 200)
	require.True(t, res.OK, res.Error)
	assert.NotEmpty(t, res.Version)
	names := map[string]string{}
	for _, tb := range res.Tables {
		names[tb.Name] = tb.Kind
	}
	assert.Equal(t, map[string]string{"products": "table", "orders": "table", "cheap": "view"}, names)
	var bad discover.Result
	e.mustDo("POST", "/api/v1/connectors/test", map[string]any{"name": "x", "type": "postgres", "config": map[string]any{"dsn": "postgres://u:p@127.0.0.1:1/db"}}, &bad, 200)
	assert.False(t, bad.OK)
	assert.NotEmpty(t, bad.Error)

	assert.Equal(t, 422, e.do("POST", "/api/v1/connectors", map[string]any{"name": "", "type": "postgres"}, nil))
	var c engine.ConnectorView
	e.mustDo("POST", "/api/v1/connectors", conn, &c, 201)
	var tables []discover.Table
	e.mustDo("GET", "/api/v1/connectors/"+c.ID+"/tables?q=ord", nil, &tables, 200)
	require.Len(t, tables, 1)
	assert.Equal(t, "orders", tables[0].Name)

	// Saving tests the connector in the background; the list shows the result.
	eventually(t, 20*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/connectors/"+c.ID, nil, &c, 200)
		return c.LastTest != nil
	}, func() string { return "no lastTest after create" })
	assert.True(t, c.LastTest.OK, c.LastTest.Error)
	assert.Positive(t, c.LastTest.Tables)
	first := c.LastTest.At
	var tested discover.Result
	e.mustDo("POST", "/api/v1/connectors/"+c.ID+"/test", nil, &tested, 200)
	require.True(t, tested.OK)
	var list []engine.ConnectorView
	e.mustDo("GET", "/api/v1/connectors", nil, &list, 200)
	require.NotNil(t, list[0].LastTest)
	assert.True(t, list[0].LastTest.At.After(first), "a manual test refreshes the stored result")

	// A task referencing the connector stores no connection fields.
	index := uniq("idx")
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", map[string]any{
		"name":   "products via connector",
		"source": map[string]any{"connectorId": c.ID, "table": "products", "cursorColumn": "version", "pollInterval": "100ms"},
		"index":  map[string]any{"uid": index},
		"sync":   map[string]any{"flushInterval": "50ms"},
	}, &v, 201)
	assert.Equal(t, model.SourceSQLPoll, v.Source.Type)
	assert.Equal(t, path, v.Source.DSN, "the view shows the merged connection")
	stored, err := e.app.Engine.Get(v.ID)
	require.NoError(t, err)
	assert.Empty(t, stored.Source.DSN, "credentials live only in the connector")
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 30)

	e.mustDo("GET", "/api/v1/connectors/"+c.ID, nil, &c, 200)
	require.Len(t, c.UsedBy, 1)
	assert.Equal(t, v.ID, c.UsedBy[0].ID)

	// Rotating the connection (here: moving the database file) restarts
	// the running task with the new settings.
	moved := filepath.Join(dir, "moved.db")
	require.NoError(t, db.Close())
	require.NoError(t, os.Rename(path, moved))
	conn["config"] = map[string]any{"driver": "sqlite", "dsn": moved}
	e.mustDo("PUT", "/api/v1/connectors/"+c.ID, conn, &c, 200)
	db2 := openSQLite(t, moved)
	_, err = db2.Exec(`INSERT INTO products VALUES (31, 'after rotation', 1000)`)
	require.NoError(t, err)
	e.waitDocs(index, 31)
	var logs []model.LogEntry
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/logs", nil, &logs, 200)
	found := false
	for _, l := range logs {
		if l.Message == `connector "Shop SQLite" changed: restarting` {
			found = true
		}
	}
	assert.True(t, found)

	// A connector in use cannot be deleted or change type.
	var conflict map[string]any
	assert.Equal(t, 409, e.do("DELETE", "/api/v1/connectors/"+c.ID, nil, &conflict))
	assert.Len(t, conflict["usedBy"], 1)
	conn["type"] = "mysql"
	conn["config"] = map[string]any{"dsn": "root:pw@tcp(localhost:3306)/x"}
	assert.Equal(t, 409, e.do("PUT", "/api/v1/connectors/"+c.ID, conn, nil))

	e.mustDo("DELETE", "/api/v1/tasks/"+v.ID, nil, nil, 204)
	e.mustDo("DELETE", "/api/v1/connectors/"+c.ID, nil, nil, 204)
	assert.Equal(t, 404, e.do("GET", "/api/v1/connectors/"+c.ID, nil, nil))
}

func TestConnectorSecrets(t *testing.T) {
	e := newEnv(t)
	var c engine.ConnectorView
	e.mustDo("POST", "/api/v1/connectors", map[string]any{"name": "Prod PG", "type": "postgres",
		"config": map[string]any{"dsn": "postgres://app:s3cret@db.internal:5432/shop"}}, &c, 201)
	assert.Equal(t, "postgres://app:••••••@db.internal:5432/shop", c.Config.DSN)

	// Editing with the masked DSN keeps the stored password.
	e.mustDo("PUT", "/api/v1/connectors/"+c.ID, map[string]any{"name": "Prod PG (eu)", "type": "postgres",
		"config": map[string]any{"dsn": c.Config.DSN}}, &c, 200)
	plain, err := e.app.Engine.RevealConnector(c.ID)
	require.NoError(t, err)
	assert.Equal(t, "postgres://app:s3cret@db.internal:5432/shop", plain.Config.DSN)
	assert.Equal(t, "Prod PG (eu)", plain.Name)

	// Viewers cannot see more than the redacted form either way.
	var list []engine.ConnectorView
	e.mustDo("GET", "/api/v1/connectors", nil, &list, 200)
	require.Len(t, list, 1)
	assert.NotContains(t, list[0].Config.DSN, "s3cret")
}
