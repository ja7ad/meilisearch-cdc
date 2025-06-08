package sink

import (
	"bytes"
	"encoding/json"
	"math"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// Documents are encoded as NDJSON by hand: transform produces a small set
// of value types, so a direct encoder avoids encoding/json's reflection,
// map key sorting and intermediate buffers. Unknown types fall back to
// encoding/json.

var bufPool = sync.Pool{New: func() any { return new(bytes.Buffer) }}

func getBuf() *bytes.Buffer {
	b := bufPool.Get().(*bytes.Buffer)
	b.Reset()
	return b
}

func putBuf(b *bytes.Buffer) {
	if b.Cap() <= 64<<20 { // do not keep huge buffers alive
		bufPool.Put(b)
	}
}

// encodeNDJSON appends one JSON object per line.
func encodeNDJSON(buf *bytes.Buffer, docs []transform.Doc) error {
	b := buf.AvailableBuffer()
	for _, d := range docs {
		var err error
		if b, err = appendObject(b, d); err != nil {
			return err
		}
		b = append(b, '\n')
	}
	buf.Write(b)
	return nil
}

func appendObject(b []byte, m map[string]any) ([]byte, error) {
	b = append(b, '{')
	first := true
	for k, v := range m {
		if !first {
			b = append(b, ',')
		}
		first = false
		b = appendString(b, k)
		b = append(b, ':')
		var err error
		if b, err = appendValue(b, v); err != nil {
			return b, err
		}
	}
	return append(b, '}'), nil
}

func appendValue(b []byte, v any) ([]byte, error) {
	switch t := v.(type) {
	case nil:
		return append(b, "null"...), nil
	case string:
		return appendString(b, t), nil
	case bool:
		return strconv.AppendBool(b, t), nil
	case int64:
		return strconv.AppendInt(b, t, 10), nil
	case int:
		return strconv.AppendInt(b, int64(t), 10), nil
	case uint64:
		return strconv.AppendUint(b, t, 10), nil
	case float64:
		return appendFloat(b, t), nil
	case map[string]any:
		return appendObject(b, t)
	case []any:
		b = append(b, '[')
		for i, e := range t {
			if i > 0 {
				b = append(b, ',')
			}
			var err error
			if b, err = appendValue(b, e); err != nil {
				return b, err
			}
		}
		return append(b, ']'), nil
	case []string:
		b = append(b, '[')
		for i, e := range t {
			if i > 0 {
				b = append(b, ',')
			}
			b = appendString(b, e)
		}
		return append(b, ']'), nil
	case time.Time:
		return appendString(b, t.UTC().Format(time.RFC3339Nano)), nil
	}
	raw, err := json.Marshal(v)
	return append(b, raw...), err
}

// appendFloat formats like encoding/json: NaN and infinities become null
// (Meilisearch rejects them), integral values have no exponent below 1e21.
func appendFloat(b []byte, f float64) []byte {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return append(b, "null"...)
	}
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	return strconv.AppendFloat(b, f, format, -1, 64)
}

const hex = "0123456789abcdef"

// safe marks bytes that need no escaping in a JSON string.
var safe = func() (s [utf8.RuneSelf]bool) {
	for i := ' '; i < utf8.RuneSelf; i++ {
		s[i] = i != '"' && i != '\\'
	}
	return
}()

// appendString quotes s, replacing invalid UTF-8 with U+FFFD as
// encoding/json does (Meilisearch rejects invalid UTF-8).
func appendString(b []byte, s string) []byte {
	b = append(b, '"')
	start := 0
	for i := 0; i < len(s); {
		c := s[i]
		if c < utf8.RuneSelf {
			if safe[c] {
				i++
				continue
			}
			b = append(b, s[start:i]...)
			switch c {
			case '"', '\\':
				b = append(b, '\\', c)
			case '\n':
				b = append(b, '\\', 'n')
			case '\r':
				b = append(b, '\\', 'r')
			case '\t':
				b = append(b, '\\', 't')
			default:
				b = append(b, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
			}
			i++
			start = i
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			b = append(b, s[start:i]...)
			b = append(b, `�`...)
			i++
			start = i
			continue
		}
		i += size
	}
	b = append(b, s[start:]...)
	return append(b, '"')
}
