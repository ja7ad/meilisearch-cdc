package model

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func valid() Task {
	t := Task{Name: "products", Source: Source{Type: SourcePostgres, DSN: "postgres://u:p@h/db", Table: "products"}, Index: Index{UID: "products"}}
	t.ApplyDefaults()
	return t
}

func TestDefaultsAndValidate(t *testing.T) {
	tk := valid()
	require.NoError(t, tk.Validate())
	assert.Equal(t, "public", tk.Source.Schema)
	assert.Equal(t, "id", tk.Mapping.PrimaryKeyField)
	assert.Equal(t, ModeSnapshotStream, tk.Sync.Mode)
	assert.Equal(t, 1000, tk.Sync.BatchSize)
	assert.Equal(t, time.Second, tk.Sync.FlushInterval.D())
	assert.Equal(t, StateStopped, tk.Desired)

	bad := valid()
	bad.Name = ""
	bad.Index.UID = "bad uid!"
	bad.Source.Type = "oracle"
	bad.Sync.Safety.MaxDeleteRatio = 2
	bad.Index.Settings = json.RawMessage(`{`)
	err := bad.Validate()
	require.Error(t, err)
	for _, s := range []string{"name is required", "index.uid", "unsupported source type", "maxDeleteRatio", "valid JSON"} {
		assert.Contains(t, err.Error(), s)
	}

	poll := valid()
	poll.Source = Source{Type: SourceSQLPoll, DSN: "x", Driver: "sqlite", Table: "t"}
	assert.ErrorContains(t, poll.Validate(), "cursorColumn")
	mongo := valid()
	mongo.Source = Source{Type: SourceMongoDB, DSN: "mongodb://x", Table: "c"}
	assert.ErrorContains(t, mongo.Validate(), "database")
	RegisterSourceType("custom")
	custom := valid()
	custom.Source = Source{Type: "custom", Table: "x"}
	assert.NoError(t, custom.Validate())
}

func TestRedactDSN(t *testing.T) {
	cases := map[string]string{
		"postgres://user:secret@db:5432/app?sslmode=disable": "postgres://user:••••••@db:5432/app?sslmode=disable",
		"mongodb://admin:pw@m1,m2/?replicaSet=rs0":           "mongodb://admin:••••••@m1,m2/?replicaSet=rs0",
		"root:pw@tcp(127.0.0.1:3306)/shop":                   "root:••••••@tcp(127.0.0.1:3306)/shop",
		"host=db user=u password='a b' dbname=x":             "host=db user=u password=•••••• dbname=x",
		"postgres://db/app":                                  "postgres://db/app",
		"":                                                   "",
	}
	for in, want := range cases {
		assert.Equal(t, want, RedactDSN(in), in)
	}
}

func TestRedactedAndRestore(t *testing.T) {
	tk := valid()
	tk.Source.SecretAccessKey = "aws"
	r := tk.Redacted()
	assert.NotContains(t, r.Source.DSN, ":p@")
	assert.Equal(t, secretMask, r.Source.SecretAccessKey)
	assert.Equal(t, "postgres://u:p@h/db", tk.Source.DSN, "original untouched")

	r.RestoreSecrets(tk)
	assert.Equal(t, tk.Source.DSN, r.Source.DSN)
	assert.Equal(t, "aws", r.Source.SecretAccessKey)

	// A masked DSN pointing elsewhere is not restored.
	moved := tk.Redacted()
	moved.Source.DSN = "postgres://u:" + secretMask + "@other/db"
	moved.RestoreSecrets(tk)
	assert.Contains(t, moved.Source.DSN, secretMask)
}

func TestDurationJSON(t *testing.T) {
	var s Sync
	require.NoError(t, json.Unmarshal([]byte(`{"flushInterval":"250ms","retry":{"maxBackoff":1500}}`), &s))
	assert.Equal(t, 250*time.Millisecond, s.FlushInterval.D())
	assert.Equal(t, 1500*time.Millisecond, s.Retry.MaxBackoff.D())
	b, _ := json.Marshal(s.FlushInterval)
	assert.Equal(t, `"250ms"`, string(b))
	assert.Error(t, json.Unmarshal([]byte(`{"flushInterval":"soon"}`), &s))
	assert.Equal(t, 5*time.Second, Duration(0).Or(5*time.Second))
}
