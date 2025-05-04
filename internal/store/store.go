// Package store persists tasks, checkpoints and reports in an embedded
// bbolt database through type safe buckets.
package store

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	bolt "go.etcd.io/bbolt"
	berrors "go.etcd.io/bbolt/errors"
)

// DB is the embedded database.
type DB struct {
	bolt *bolt.DB
}

// ErrLocked is returned when another process holds the database open.
var ErrLocked = errors.New("the database is locked by another process")

// Open opens (or creates) the database file.
func Open(path string) (*DB, error) { return OpenTimeout(path, 5*time.Second) }

// OpenTimeout is Open waiting at most timeout for the file lock.
func OpenTimeout(path string, timeout time.Duration) (*DB, error) {
	b, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: timeout, FreelistType: bolt.FreelistMapType})
	if errors.Is(err, berrors.ErrTimeout) {
		return nil, fmt.Errorf("open store %s: %w", path, ErrLocked)
	}
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	return &DB{bolt: b}, nil
}

// Close closes the database.
func (d *DB) Close() error { return d.bolt.Close() }

// Clear empties every bucket and returns the number of keys removed.
func (d *DB) Clear() (int, error) {
	n := 0
	err := d.bolt.Update(func(tx *bolt.Tx) error {
		var names [][]byte
		if err := tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			n += b.Stats().KeyN
			names = append(names, append([]byte(nil), name...))
			return nil
		}); err != nil {
			return err
		}
		for _, name := range names {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		return nil
	})
	return n, err
}

// Hook transforms a value on its way into or out of the database, for
// example to encrypt secrets.
type Hook[T any] func(T) (T, error)

// Bucket stores values of a single type T keyed by string. Using one
// Bucket per type makes it impossible to read a value as the wrong type.
type Bucket[T any] struct {
	db       *DB
	name     []byte
	onWrite  Hook[T]
	onRead   Hook[T]
	validate func(T) error
}

// Option configures a bucket.
type Option[T any] func(*Bucket[T])

// WithHooks sets transforms applied before writing and after reading.
func WithHooks[T any](onWrite, onRead Hook[T]) Option[T] {
	return func(b *Bucket[T]) { b.onWrite, b.onRead = onWrite, onRead }
}

// WithValidation rejects invalid values on Put.
func WithValidation[T any](fn func(T) error) Option[T] {
	return func(b *Bucket[T]) { b.validate = fn }
}

// NewBucket creates the bucket if needed.
func NewBucket[T any](db *DB, name string, opts ...Option[T]) (*Bucket[T], error) {
	b := &Bucket[T]{db: db, name: []byte(name)}
	for _, o := range opts {
		o(b)
	}
	err := db.bolt.Update(func(tx *bolt.Tx) error {
		_, err := tx.CreateBucketIfNotExists(b.name)
		return err
	})
	return b, err
}

// ErrNotFound is returned by MustGet for missing keys.
var ErrNotFound = errors.New("not found")

func (b *Bucket[T]) encode(v T) ([]byte, error) {
	if b.validate != nil {
		if err := b.validate(v); err != nil {
			return nil, err
		}
	}
	if b.onWrite != nil {
		var err error
		if v, err = b.onWrite(v); err != nil {
			return nil, err
		}
	}
	return json.Marshal(v)
}

func (b *Bucket[T]) decode(raw []byte) (T, error) {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, fmt.Errorf("decode %s: %w", b.name, err)
	}
	if b.onRead != nil {
		return b.onRead(v)
	}
	return v, nil
}

// Get returns the value for key and whether it exists.
func (b *Bucket[T]) Get(key string) (T, bool, error) {
	var (
		v     T
		found bool
		err   error
	)
	verr := b.db.bolt.View(func(tx *bolt.Tx) error {
		raw := tx.Bucket(b.name).Get([]byte(key))
		if raw == nil {
			return nil
		}
		found = true
		v, err = b.decode(raw)
		return err
	})
	if verr != nil {
		return v, false, verr
	}
	return v, found, nil
}

// MustGet is Get returning ErrNotFound for missing keys.
func (b *Bucket[T]) MustGet(key string) (T, error) {
	v, ok, err := b.Get(key)
	if err == nil && !ok {
		err = fmt.Errorf("%s %q: %w", b.name, key, ErrNotFound)
	}
	return v, err
}

// Put stores v under key.
func (b *Bucket[T]) Put(key string, v T) error {
	raw, err := b.encode(v)
	if err != nil {
		return err
	}
	return b.db.bolt.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(b.name).Put([]byte(key), raw)
	})
}

// PutMany stores several values in one transaction.
func (b *Bucket[T]) PutMany(values map[string]T) error {
	enc := make(map[string][]byte, len(values))
	for k, v := range values {
		raw, err := b.encode(v)
		if err != nil {
			return err
		}
		enc[k] = raw
	}
	return b.db.bolt.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(b.name)
		for k, raw := range enc {
			if err := bk.Put([]byte(k), raw); err != nil {
				return err
			}
		}
		return nil
	})
}

// Update atomically reads, modifies and writes key. fn receives the current
// value (zero when missing) and whether it existed.
func (b *Bucket[T]) Update(key string, fn func(v *T, exists bool) error) error {
	return b.db.bolt.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(b.name)
		var (
			v      T
			exists bool
		)
		if raw := bk.Get([]byte(key)); raw != nil {
			exists = true
			var err error
			if v, err = b.decode(raw); err != nil {
				return err
			}
		}
		if err := fn(&v, exists); err != nil {
			return err
		}
		raw, err := b.encode(v)
		if err != nil {
			return err
		}
		return bk.Put([]byte(key), raw)
	})
}

// UpdateExisting changes the value at key in place and reports whether it
// existed. A missing key is left missing: a record deleted concurrently is
// never written back (as an empty value) by a late update.
func (b *Bucket[T]) UpdateExisting(key string, fn func(v *T) error) (bool, error) {
	found := false
	err := b.db.bolt.Update(func(tx *bolt.Tx) error {
		bk := tx.Bucket(b.name)
		raw := bk.Get([]byte(key))
		if raw == nil {
			return nil
		}
		found = true
		v, err := b.decode(raw)
		if err != nil {
			return err
		}
		if err := fn(&v); err != nil {
			return err
		}
		if raw, err = b.encode(v); err != nil {
			return err
		}
		return bk.Put([]byte(key), raw)
	})
	return found, err
}

// Delete removes key. Missing keys are not an error.
func (b *Bucket[T]) Delete(key string) error {
	return b.db.bolt.Update(func(tx *bolt.Tx) error {
		return tx.Bucket(b.name).Delete([]byte(key))
	})
}

// ForEach visits values whose key starts with prefix, in key order, until fn
// returns false. fn runs inside a read transaction: it must not write to the
// store (collect keys and write afterwards). A write would wait for the read
// transaction whenever the file has to grow, and never finish.
func (b *Bucket[T]) ForEach(prefix string, fn func(key string, v T) bool) error {
	return b.db.bolt.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(b.name).Cursor()
		p := []byte(prefix)
		for k, raw := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, raw = c.Next() {
			v, err := b.decode(raw)
			if err != nil {
				return err
			}
			if !fn(string(k), v) {
				return nil
			}
		}
		return nil
	})
}

// List returns every value whose key starts with prefix, up to limit (0 = all).
func (b *Bucket[T]) List(prefix string, limit int) ([]T, error) {
	var out []T
	err := b.ForEach(prefix, func(_ string, v T) bool {
		out = append(out, v)
		return limit <= 0 || len(out) < limit
	})
	return out, err
}

// Count returns the number of keys starting with prefix.
func (b *Bucket[T]) Count(prefix string) (int, error) {
	n := 0
	err := b.db.bolt.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(b.name).Cursor()
		p := []byte(prefix)
		for k, _ := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, _ = c.Next() {
			n++
		}
		return nil
	})
	return n, err
}

// Clear removes every key and returns the count.
func (b *Bucket[T]) Clear() (int, error) { return b.DeletePrefix("") }

// DeletePrefix removes every key starting with prefix and returns the count.
func (b *Bucket[T]) DeletePrefix(prefix string) (int, error) {
	n := 0
	err := b.db.bolt.Update(func(tx *bolt.Tx) error {
		c := tx.Bucket(b.name).Cursor()
		p := []byte(prefix)
		for k, _ := c.Seek(p); k != nil && bytes.HasPrefix(k, p); k, _ = c.Seek(p) {
			if err := c.Delete(); err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
