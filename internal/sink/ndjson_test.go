package sink

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

func sampleDocs(n int) []transform.Doc {
	docs := make([]transform.Doc, n)
	for i := range docs {
		docs[i] = transform.Doc{
			"id": int64(i), "name": "Product \"quoted\" \\ name\n", "price": 19.99, "big": uint64(math.MaxUint64), "in_stock": true,
			"nothing": nil, "tags": []any{"a", int64(1), 2.5, false, nil}, "meta": map[string]any{"color": "red", "nested": map[string]any{"x": int64(1)}},
			"unicode": "héllo — 世界  ", "ctrl": "a\x01b\x1f", "names": []string{"x", "y"}, "small": 1e-9, "huge": 1e22, "zero": 0.0,
			"when": time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC), "other": struct{ A int }{A: 3},
		}
	}
	return docs
}

func TestEncodeNDJSONMatchesEncodingJSON(t *testing.T) {
	docs := sampleDocs(3)
	var buf bytes.Buffer
	require.NoError(t, encodeNDJSON(&buf, docs))
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	require.Len(t, lines, 3)
	for i, line := range lines {
		var got, want map[string]any
		require.NoError(t, json.Unmarshal([]byte(line), &got), line)
		raw, err := json.Marshal(docs[i])
		require.NoError(t, err)
		require.NoError(t, json.Unmarshal(raw, &want))
		assert.Equal(t, want, got)
	}
}

func TestEncodeEdgeCases(t *testing.T) {
	enc := func(v any) string {
		b, err := appendValue(nil, v)
		require.NoError(t, err)
		return string(b)
	}
	assert.Equal(t, "null", enc(math.NaN()))
	assert.Equal(t, "null", enc(math.Inf(1)))
	assert.Equal(t, "1", enc(1.0))
	assert.Equal(t, "0.1", enc(0.1))
	assert.Equal(t, "1e+21", enc(1e21))
	assert.Equal(t, `"�"`, enc(string([]byte{0xff})), "invalid UTF-8 is replaced")
	assert.Equal(t, `"<a&b>"`, enc("<a&b>"))
	assert.Equal(t, `{}`, enc(map[string]any{}))
	assert.Equal(t, `[]`, enc([]any{}))
	assert.True(t, json.Valid([]byte(enc("\u0000\u007f "))))
}

func BenchmarkEncodeNDJSON(b *testing.B) {
	docs := sampleDocs(1000)
	b.ReportAllocs()
	for b.Loop() {
		buf := getBuf()
		if err := encodeNDJSON(buf, docs); err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(buf.Len()))
		putBuf(buf)
	}
}

func BenchmarkEncodingJSON(b *testing.B) {
	docs := sampleDocs(1000)
	b.ReportAllocs()
	for b.Loop() {
		raw, err := json.Marshal(docs)
		if err != nil {
			b.Fatal(err)
		}
		b.SetBytes(int64(len(raw)))
	}
}
