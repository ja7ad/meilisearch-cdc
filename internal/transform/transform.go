// Package transform turns source events into Meilisearch documents.
package transform

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

// Doc is a Meilisearch document.
type Doc = map[string]any

// Change is a transformed event ready for the sink.
type Change struct {
	Op       model.Op
	ID       string // document id as sent to Meilisearch, stringified
	Doc      Doc    // nil for deletes and truncates
	Position model.Position
	Time     time.Time
	Snapshot bool
	Partial  bool // see model.Event.Partial
}

// ErrMissingKey is returned when a row lacks a primary key column.
var ErrMissingKey = errors.New("row has no value for a primary key column")

// maxIDBytes is Meilisearch's document id limit.
const maxIDBytes = 511

// Transformer applies a model.Mapping. It is immutable and safe for
// concurrent use.
type Transformer struct {
	m       model.Mapping
	pk      []string
	include map[string]bool
	exclude map[string]bool
	pkCols  map[string]bool
}

// New compiles a mapping. sourcePK is used when the mapping does not name
// its own primary key columns.
func New(m model.Mapping, sourcePK []string) (*Transformer, error) {
	pk := m.PrimaryKey
	if len(pk) == 0 {
		pk = sourcePK
	}
	if len(pk) == 0 {
		return nil, errors.New("no primary key: the source has none and mapping.primaryKey is empty")
	}
	if m.PrimaryKeyField == "" {
		m.PrimaryKeyField = "id"
	}
	t := &Transformer{m: m, pk: pk, pkCols: map[string]bool{}}
	for _, c := range pk {
		t.pkCols[c] = true
	}
	if len(m.Include) > 0 {
		t.include = map[string]bool{}
		for _, c := range m.Include {
			t.include[c] = true
		}
	}
	t.exclude = map[string]bool{}
	for _, c := range m.Exclude {
		t.exclude[c] = true
	}
	return t, nil
}

// PrimaryKey returns the source key columns.
func (t *Transformer) PrimaryKey() []string { return t.pk }

// PrimaryKeyField returns the document id field.
func (t *Transformer) PrimaryKeyField() string { return t.m.PrimaryKeyField }

// FieldName returns the document field a source column is written to, and
// false when the column is not copied.
func (t *Transformer) FieldName(col string) (string, bool) {
	if t.pkCols[col] && !t.m.KeepKeyColumns && (len(t.pk) != 1 || col != t.m.PrimaryKeyField) {
		return "", false
	}
	if t.include != nil && !t.include[col] && !t.pkCols[col] {
		return "", false
	}
	if t.exclude[col] {
		return "", false
	}
	if r, ok := t.m.Rename[col]; ok && r != "" {
		return r, true
	}
	return col, true
}

// Apply transforms one event.
func (t *Transformer) Apply(e model.Event) (Change, error) {
	ch := Change{Op: e.Op, Position: e.Position, Time: e.Time, Snapshot: e.Snapshot, Partial: e.Partial}
	if e.Op == model.OpTruncate {
		return ch, nil
	}
	idv, id, err := t.id(e.Row)
	if err != nil {
		return ch, err
	}
	ch.ID = id
	if e.Op == model.OpDelete {
		return ch, nil
	}
	if t.softDeleted(e.Row) {
		ch.Op = model.OpDelete
		return ch, nil
	}
	doc := make(Doc, len(e.Row)+len(t.m.Static)+2)
	for col, v := range e.Row {
		name, ok := t.FieldName(col)
		if !ok {
			continue
		}
		doc[name] = t.value(v)
	}
	if t.m.Geo != nil {
		lat, okLat := toFloat(e.Row[t.m.Geo.Lat])
		lng, okLng := toFloat(e.Row[t.m.Geo.Lng])
		if okLat && okLng {
			doc["_geo"] = map[string]any{"lat": lat, "lng": lng}
		} else {
			// An explicit null removes a stale location on upsert.
			doc["_geo"] = nil
		}
	}
	for k, v := range t.m.Static {
		doc[k] = v
	}
	doc[t.m.PrimaryKeyField] = idv
	ch.Doc = doc
	return ch, nil
}

func (t *Transformer) softDeleted(row map[string]any) bool {
	if t.m.SoftDeleteColumn == "" {
		return false
	}
	v, ok := row[t.m.SoftDeleteColumn]
	if !ok || v == nil {
		return false
	}
	if t.m.SoftDeleteValue == "" {
		// Any non-null value deletes (deleted_at style), but false does not.
		if b, ok := v.(bool); ok {
			return b
		}
		return true
	}
	return strings.EqualFold(scalarString(v), t.m.SoftDeleteValue)
}

// id returns the id value written into the document (a number for single
// integer keys, a string otherwise) and its string form.
func (t *Transformer) id(row map[string]any) (any, string, error) {
	if len(t.pk) == 1 {
		v, ok := row[t.pk[0]]
		if !ok || v == nil {
			return nil, "", fmt.Errorf("%w %q", ErrMissingKey, t.pk[0])
		}
		switch n := v.(type) {
		case int64:
			return n, strconv.FormatInt(n, 10), nil
		case uint64:
			return n, strconv.FormatUint(n, 10), nil
		case float64:
			if n == math.Trunc(n) && math.Abs(n) < 1<<53 {
				return int64(n), strconv.FormatInt(int64(n), 10), nil
			}
		}
		s := limitID(EncodeID(scalarString(v), false))
		return s, s, nil
	}
	parts := make([]string, len(t.pk))
	for i, c := range t.pk {
		v, ok := row[c]
		if !ok || v == nil {
			return nil, "", fmt.Errorf("%w %q", ErrMissingKey, c)
		}
		parts[i] = EncodeID(scalarString(v), true)
	}
	s := limitID(strings.Join(parts, "-"))
	return s, s, nil
}

// EncodeID escapes a key value into Meilisearch's id alphabet
// [a-zA-Z0-9_-]. Values made of [a-zA-Z0-9-] are kept as is (integers,
// UUIDs, slugs). Otherwise '_' becomes "__" and every other byte outside the
// alphabet becomes "_XX" (hex). Raw ids never contain '_' and escaped ids
// always do, so the mapping is injective. For composite keys (composite =
// true) '-' is escaped too, since it joins the parts.
func EncodeID(s string, composite bool) string {
	if s == "" {
		return "_"
	}
	clean := true
	for i := 0; i < len(s); i++ {
		if !isIDChar(s[i]) || s[i] == '_' || (composite && s[i] == '-') {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_':
			b.WriteString("__")
		case (composite && c == '-') || !isIDChar(c):
			b.WriteByte('_')
			b.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

func isIDChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

// limitID replaces ids longer than Meilisearch allows with a stable hash.
func limitID(s string) string {
	if len(s) <= maxIDBytes {
		return s
	}
	sum := sha256.Sum256([]byte(s))
	return "h-" + hex.EncodeToString(sum[:])
}

func scalarString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	case time.Time:
		return t.UTC().Format(time.RFC3339Nano)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	}
	return fmt.Sprint(v)
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int64:
		return float64(n), true
	case uint64:
		return float64(n), true
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil
	}
	return 0, false
}

// value converts a normalised source value into a JSON friendly value.
// Nested slices and maps are copied only when something inside them needs
// converting; otherwise they are shared with the row (rows are never
// mutated after they are emitted).
func (t *Transformer) value(v any) any {
	switch x := v.(type) {
	case time.Time:
		switch t.m.TimeFormat {
		case model.TimeUnix:
			return x.Unix()
		case model.TimeUnixMs:
			return x.UnixMilli()
		}
		return x.UTC().Format(time.RFC3339Nano)
	case []byte:
		return base64.StdEncoding.EncodeToString(x)
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return nil
		}
		return x
	case []any:
		if !needsConversion(x) {
			return x
		}
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = t.value(e)
		}
		return out
	case map[string]any:
		if !needsConversion(x) {
			return x
		}
		out := make(map[string]any, len(x))
		for k, e := range x {
			out[k] = t.value(e)
		}
		return out
	}
	return v
}

// needsConversion reports whether v contains a value value() rewrites.
func needsConversion(v any) bool {
	switch x := v.(type) {
	case time.Time, []byte:
		return true
	case float64:
		return math.IsNaN(x) || math.IsInf(x, 0)
	case []any:
		for _, e := range x {
			if needsConversion(e) {
				return true
			}
		}
	case map[string]any:
		for _, e := range x {
			if needsConversion(e) {
				return true
			}
		}
	}
	return false
}
