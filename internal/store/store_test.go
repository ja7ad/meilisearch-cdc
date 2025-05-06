package store

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type item struct {
	Name   string `json:"name"`
	Secret string `json:"secret"`
}

func open(t *testing.T) *DB {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	require.NoError(t, err)
	t.Cleanup(func() { db.Close() })
	return db
}

func TestBucketCRUD(t *testing.T) {
	db := open(t)
	b, err := NewBucket[item](db, "items")
	require.NoError(t, err)

	_, ok, err := b.Get("a")
	require.NoError(t, err)
	assert.False(t, ok)
	_, err = b.MustGet("a")
	assert.ErrorIs(t, err, ErrNotFound)

	require.NoError(t, b.Put("x/1", item{Name: "one"}))
	require.NoError(t, b.PutMany(map[string]item{"x/2": {Name: "two"}, "y/1": {Name: "three"}}))
	v, err := b.MustGet("x/1")
	require.NoError(t, err)
	assert.Equal(t, "one", v.Name)

	list, err := b.List("x/", 0)
	require.NoError(t, err)
	assert.Equal(t, []item{{Name: "one"}, {Name: "two"}}, list)
	list, _ = b.List("", 1)
	assert.Len(t, list, 1)
	n, _ := b.Count("")
	assert.Equal(t, 3, n)

	require.NoError(t, b.Update("x/1", func(v *item, exists bool) error {
		assert.True(t, exists)
		v.Name = "uno"
		return nil
	}))
	v, _ = b.MustGet("x/1")
	assert.Equal(t, "uno", v.Name)
	err = b.Update("x/1", func(*item, bool) error { return errors.New("abort") })
	assert.Error(t, err)
	v, _ = b.MustGet("x/1")
	assert.Equal(t, "uno", v.Name, "failed update leaves value untouched")

	removed, err := b.DeletePrefix("x/")
	require.NoError(t, err)
	assert.Equal(t, 2, removed)
	require.NoError(t, b.Delete("y/1"))
	require.NoError(t, b.Delete("missing"))
	n, _ = b.Count("")
	assert.Zero(t, n)
}

func TestBucketHooksAndValidation(t *testing.T) {
	db := open(t)
	seal := func(i item) (item, error) { i.Secret = "enc:" + i.Secret; return i, nil }
	unseal := func(i item) (item, error) { i.Secret = strings.TrimPrefix(i.Secret, "enc:"); return i, nil }
	b, err := NewBucket(db, "hooked", WithHooks(seal, unseal), WithValidation(func(i item) error {
		if i.Name == "" {
			return errors.New("name required")
		}
		return nil
	}))
	require.NoError(t, err)
	assert.Error(t, b.Put("k", item{}))
	require.NoError(t, b.Put("k", item{Name: "n", Secret: "s"}))
	v, _ := b.MustGet("k")
	assert.Equal(t, "s", v.Secret)

	raw, err := NewBucket[map[string]string](db, "hooked")
	require.NoError(t, err)
	m, _ := raw.MustGet("k")
	assert.Equal(t, "enc:s", m["secret"], "stored form is sealed")
}

func TestUpdateExistingNeverRecreates(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "s.db"))
	require.NoError(t, err)
	defer db.Close()
	b, err := NewBucket[map[string]string](db, "things")
	require.NoError(t, err)
	require.NoError(t, b.Put("a", map[string]string{"name": "x"}))

	found, err := b.UpdateExisting("a", func(v *map[string]string) error { (*v)["seen"] = "1"; return nil })
	require.NoError(t, err)
	assert.True(t, found)
	v, _, _ := b.Get("a")
	assert.Equal(t, "1", v["seen"])

	require.NoError(t, b.Delete("a"))
	found, err = b.UpdateExisting("a", func(v *map[string]string) error { t.Fatal("called for a missing key"); return nil })
	require.NoError(t, err)
	assert.False(t, found)
	_, ok, _ := b.Get("a")
	assert.False(t, ok, "a deleted record stays deleted")
}
