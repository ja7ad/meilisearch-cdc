// Package couchdb follows the _changes feed of a CouchDB database (also
// Cloudant and other servers speaking the CouchDB replication protocol).
//
// cfg.DSN is the server URL with optional basic auth credentials, for
// example http://admin:secret@localhost:5984. The database is cfg.Table,
// or cfg.Database when Table is empty.
//
// Positions are the opaque update sequences of the _changes feed, kept
// verbatim ("12-g1AAAA..." on CouchDB 2+, "12" on CouchDB 1.x). Every
// stream event carries the sequence of its own change; resuming from it
// continues with the following changes (CouchDB may replay a few, which is
// harmless). Design documents (_design/...) are skipped everywhere and
// attachment stubs (_attachments) are dropped from documents; _rev is kept.
package couchdb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
)

const (
	keyField     = "_id"
	pageSize     = 1000
	sampleSize   = 100
	changesLimit = 1000
	// pollTimeout is how long a longpoll request waits for changes.
	pollTimeout = 30 * time.Second
	// maxRetries bounds consecutive transient failures inside Stream before
	// it gives the error to the engine.
	maxRetries = 5
)

func init() {
	source.Register(model.SourceCouchDB, func(cfg model.Source, taskID string) (source.Source, error) {
		return New(cfg), nil
	})
}

// Source follows one database. It implements source.Source.
type Source struct {
	cfg      model.Source
	db       string
	base     string // server URL without credentials, no trailing slash
	user     string
	password string
	auth     bool
	client   *http.Client
}

// New returns an unopened source.
func New(cfg model.Source) *Source {
	db := cfg.Table
	if db == "" {
		db = cfg.Database
	}
	return &Source{cfg: cfg, db: db, client: &http.Client{}}
}

// dbInfo is the reply of GET /{db}.
type dbInfo struct {
	DocCount  int64           `json:"doc_count"`
	UpdateSeq json.RawMessage `json:"update_seq"`
}

// Open parses the DSN and checks the database exists and is readable.
func (s *Source) Open(ctx context.Context) error {
	if s.cfg.DSN == "" {
		return source.Permanent(errors.New("couchdb: source.dsn is required (http://user:password@host:5984)"))
	}
	if s.db == "" {
		return source.Permanent(errors.New("couchdb: source.table (the database name) is required"))
	}
	u, err := url.Parse(s.cfg.DSN)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return source.Permanent(fmt.Errorf("couchdb: invalid dsn %q: want http(s)://user:password@host:5984", redact(s.cfg.DSN)))
	}
	if u.User != nil {
		s.user = u.User.Username()
		s.password, _ = u.User.Password()
		s.auth = true
		u.User = nil
	}
	u.RawQuery, u.Fragment = "", ""
	s.base = strings.TrimRight(u.String(), "/")
	_, err = s.info(ctx)
	return err
}

func (s *Source) info(ctx context.Context) (*dbInfo, error) {
	var info dbInfo
	if err := s.get(ctx, "", nil, &info); err != nil {
		return nil, err
	}
	return &info, nil
}

// Schema samples up to 100 documents.
func (s *Source) Schema(ctx context.Context) (*model.Schema, error) {
	info, err := s.info(ctx)
	if err != nil {
		return nil, err
	}
	page, err := s.allDocs(ctx, url.Values{"include_docs": {"true"}, "limit": {strconv.Itoa(sampleSize)}})
	if err != nil {
		return nil, err
	}
	var docs []map[string]any
	for _, r := range page.Rows {
		if r.Doc != nil && !isDesign(r.ID) {
			docs = append(docs, cleanDoc(r.Doc))
		}
	}
	return &model.Schema{Columns: inferColumns(docs), PrimaryKey: []string{keyField}, Inferred: true, RowCount: info.DocCount}, nil
}

// CurrentPosition returns the database's current update sequence.
func (s *Source) CurrentPosition(ctx context.Context) (model.Position, error) {
	info, err := s.info(ctx)
	if err != nil {
		return "", err
	}
	seq := seqString(info.UpdateSeq)
	if seq == "" {
		return "", errors.New("couchdb: the database reported no update_seq")
	}
	return model.Position(seq), nil
}

type allDocsRow struct {
	ID  string         `json:"id"`
	Doc map[string]any `json:"doc"`
}

type allDocsPage struct {
	Rows []allDocsRow `json:"rows"`
}

func (s *Source) allDocs(ctx context.Context, q url.Values) (*allDocsPage, error) {
	var page allDocsPage
	if err := s.get(ctx, "/_all_docs", q, &page); err != nil {
		return nil, err
	}
	return &page, nil
}

// Snapshot reads every document with _all_docs, paginated by key.
func (s *Source) Snapshot(ctx context.Context, emit source.EmitFunc) error {
	var startKey string
	first := true
	for {
		q := url.Values{"include_docs": {"true"}, "limit": {strconv.Itoa(pageSize)}}
		if !first {
			k, _ := json.Marshal(startKey)
			q.Set("startkey", string(k))
			q.Set("skip", "1") // the start key itself was read by the previous page
		}
		page, err := s.allDocs(ctx, q)
		if err != nil {
			return err
		}
		batch := make([]model.Event, 0, len(page.Rows))
		for _, r := range page.Rows {
			if r.Doc == nil || isDesign(r.ID) {
				continue
			}
			batch = append(batch, model.Event{Op: model.OpUpsert, Row: cleanDoc(r.Doc), Snapshot: true})
		}
		if len(batch) > 0 {
			if err := emit(ctx, batch); err != nil {
				return err
			}
		}
		if len(page.Rows) < pageSize {
			return nil
		}
		startKey, first = page.Rows[len(page.Rows)-1].ID, false
	}
}

type changeRow struct {
	Seq     json.RawMessage `json:"seq"`
	ID      string          `json:"id"`
	Deleted bool            `json:"deleted"`
	Doc     map[string]any  `json:"doc"`
}

type changesPage struct {
	Results []changeRow     `json:"results"`
	LastSeq json.RawMessage `json:"last_seq"`
}

// Stream long-polls the _changes feed from the given sequence. Transient
// network errors are retried with backoff; authorization errors and a
// deleted database stop the stream with a permanent error.
func (s *Source) Stream(ctx context.Context, from model.Position, emit source.EmitFunc) error {
	since := string(from)
	if since == "" {
		since = "now"
	}
	failures := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		q := url.Values{
			"feed":         {"longpoll"},
			"include_docs": {"true"},
			"style":        {"main_only"},
			"since":        {since},
			"limit":        {strconv.Itoa(changesLimit)},
			"timeout":      {strconv.Itoa(int(pollTimeout / time.Millisecond))},
		}
		var page changesPage
		err := s.get(ctx, "/_changes", q, &page)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if source.IsPermanent(err) || errors.Is(err, source.ErrPositionLost) {
				return err
			}
			failures++
			if failures > maxRetries {
				return err
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(backoff(failures)):
			}
			continue
		}
		failures = 0
		batch := make([]model.Event, 0, len(page.Results))
		for _, c := range page.Results {
			if isDesign(c.ID) {
				continue
			}
			ev := model.Event{Position: model.Position(seqString(c.Seq))}
			if c.Deleted {
				ev.Op, ev.Row = model.OpDelete, map[string]any{keyField: c.ID}
			} else {
				if c.Doc == nil {
					continue // include_docs not honoured; nothing to index
				}
				ev.Op, ev.Row = model.OpUpsert, cleanDoc(c.Doc)
			}
			batch = append(batch, ev)
		}
		if len(batch) > 0 {
			if err := emit(ctx, batch); err != nil {
				return err
			}
		}
		if last := seqString(page.LastSeq); last != "" {
			since = last
		}
	}
}

func backoff(n int) time.Duration {
	d := time.Second << (n - 1)
	if d > 30*time.Second {
		d = 30 * time.Second
	}
	return d
}

// Ack is a no-op: the sequence alone describes progress.
func (s *Source) Ack(context.Context, model.Position) error { return nil }

// Close releases idle connections.
func (s *Source) Close() error {
	s.client.CloseIdleConnections()
	return nil
}

// get issues GET {base}/{db}{path}?{q} and decodes the JSON reply into out.
func (s *Source) get(ctx context.Context, path string, q url.Values, out any) error {
	u := s.base + "/" + url.PathEscape(s.db) + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return source.Permanent(fmt.Errorf("couchdb: %w", err))
	}
	req.Header.Set("Accept", "application/json")
	if s.auth {
		req.SetBasicAuth(s.user, s.password)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("couchdb: GET %s%s: %w", s.db, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s.statusError(resp.StatusCode, path, body)
	}
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("couchdb: decode %s%s: %w", s.db, path, err)
	}
	return nil
}

func (s *Source) statusError(code int, path string, body []byte) error {
	var e struct {
		Error  string `json:"error"`
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal(body, &e)
	detail := strings.TrimSpace(e.Error + ": " + e.Reason)
	if e.Error == "" {
		detail = strings.TrimSpace(string(body))
	}
	switch {
	case code == http.StatusUnauthorized:
		return source.Permanent(fmt.Errorf("couchdb: authentication failed (%s); check the user and password in the dsn", detail))
	case code == http.StatusForbidden:
		return source.Permanent(fmt.Errorf("couchdb: the user may not read database %q (%s); add it to the database _security members", s.db, detail))
	case code == http.StatusNotFound:
		return source.Permanent(fmt.Errorf("couchdb: database %q not found (%s); create it or fix source.table", s.db, detail))
	case code == http.StatusBadRequest && path == "/_changes":
		// The stored sequence is malformed for this database (for example
		// written by another server); only a resync can recover.
		return fmt.Errorf("couchdb: the server rejected the stored sequence (%s): %w", detail, source.ErrPositionLost)
	case code == http.StatusBadRequest:
		return source.Permanent(fmt.Errorf("couchdb: bad request for %s%s (%s)", s.db, path, detail))
	}
	return fmt.Errorf("couchdb: GET %s%s: HTTP %d (%s)", s.db, path, code, detail)
}

// seqString renders a sequence, which is a JSON string on CouchDB 2+ and a
// number on 1.x, without quotes.
func seqString(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return string(raw)
}

func isDesign(id string) bool { return strings.HasPrefix(id, "_design/") }

// cleanDoc drops attachment stubs and normalises values.
func cleanDoc(doc map[string]any) map[string]any {
	out := make(map[string]any, len(doc))
	for k, v := range doc {
		if k == "_attachments" {
			continue
		}
		out[k] = normalizeJSON(v)
	}
	return out
}

func normalizeJSON(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if i, err := strconv.ParseInt(s, 10, 64); err == nil {
			return i
		}
		if u, err := strconv.ParseUint(s, 10, 64); err == nil {
			return u
		}
		if f, err := t.Float64(); err == nil {
			return f
		}
		return s
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = normalizeJSON(e)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = normalizeJSON(e)
		}
		return out
	}
	return source.Normalize(v)
}

// inferColumns builds sorted columns from sampled documents; _id is always present.
func inferColumns(docs []map[string]any) []model.Column {
	types := map[string]string{}
	count := map[string]int{}
	nullable := map[string]bool{}
	for _, d := range docs {
		for k, v := range d {
			count[k]++
			t := typeName(v)
			if v == nil {
				nullable[k] = true
			}
			switch old, ok := types[k]; {
			case !ok, old == "null":
				types[k] = t
			case old != t && t != "null":
				types[k] = "mixed"
			}
		}
	}
	if _, ok := types[keyField]; !ok {
		types[keyField] = "string"
		count[keyField] = len(docs)
	}
	names := make([]string, 0, len(types))
	for k := range types {
		names = append(names, k)
	}
	sort.Strings(names)
	cols := make([]model.Column, 0, len(names))
	for _, n := range names {
		cols = append(cols, model.Column{Name: n, Type: types[n], Nullable: nullable[n] || count[n] < len(docs), PrimaryKey: n == keyField})
	}
	return cols
}

func typeName(v any) string {
	switch v.(type) {
	case nil:
		return "null"
	case bool:
		return "bool"
	case int64, uint64:
		return "int"
	case float64:
		return "float"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return fmt.Sprintf("%T", v)
}

// redact hides the password of a DSN in messages.
func redact(dsn string) string {
	u, err := url.Parse(dsn)
	if err != nil {
		return "<unparsable>"
	}
	if u.User == nil {
		return dsn
	}
	return u.Redacted()
}
