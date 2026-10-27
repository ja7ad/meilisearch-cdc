//go:build integration

package integration

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func openSQLite(t *testing.T, path string) *sql.DB {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(10000)")
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}
