// Package audit records security relevant actions.
package audit

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Entry is one audited action.
type Entry struct {
	At        time.Time `json:"at"`
	UserID    string    `json:"userId"`
	UserEmail string    `json:"userEmail"`
	Action    string    `json:"action"`
	Target    string    `json:"target,omitempty"`
	Details   string    `json:"details,omitempty"`
	IP        string    `json:"ip,omitempty"`
}

// Log stores entries, keeping the most recent Max.
type Log struct {
	b      *store.Bucket[Entry]
	Max    int
	writes atomic.Int64
}

// New opens the audit bucket.
func New(db *store.DB) (*Log, error) {
	b, err := store.NewBucket[Entry](db, "audit")
	return &Log{b: b, Max: 20000}, err
}

func suffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Record stores e. Failures are ignored: auditing never blocks an action.
func (l *Log) Record(e Entry) {
	if l == nil {
		return
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	_ = l.b.Put(fmt.Sprintf("%020d-%s", e.At.UnixNano(), suffix()), e)
	if l.writes.Add(1)%500 == 0 {
		l.prune()
	}
}

func (l *Log) prune() {
	n, err := l.b.Count("")
	if err != nil || n <= l.Max {
		return
	}
	drop := n - l.Max
	var keys []string
	_ = l.b.ForEach("", func(k string, _ Entry) bool {
		keys = append(keys, k)
		return len(keys) < drop
	})
	for _, k := range keys {
		_ = l.b.Delete(k)
	}
}

// Purge deletes every entry and returns the count.
func (l *Log) Purge() (int, error) { return l.b.Clear() }

// List returns up to limit entries, newest first, optionally filtered.
func (l *Log) List(limit int, action, user string) ([]Entry, error) {
	var all []Entry
	err := l.b.ForEach("", func(_ string, e Entry) bool {
		if (action == "" || e.Action == action) && (user == "" || e.UserEmail == user || e.UserID == user) {
			all = append(all, e)
		}
		return true
	})
	out := make([]Entry, 0, min(limit, len(all)))
	for i := len(all) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, all[i])
	}
	return out, err
}
