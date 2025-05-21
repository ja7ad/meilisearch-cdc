package transform

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func TestEncodeID(t *testing.T) {
	cases := []struct {
		in        string
		composite bool
		want      string
	}{
		{"42", false, "42"},
		{"550e8400-e29b-41d4", false, "550e8400-e29b-41d4"},
		{"a_b", false, "a__b"},
		{"a/b", false, "a_2Fb"},
		{"", false, "_"},
		{"a-b", true, "a_2Db"},
		{"héllo", false, "h_C3_A9llo"},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, EncodeID(c.in, c.composite), c.in)
	}
}

func TestEncodeIDInjective(t *testing.T) {
	inputs := []string{"a_2Fb", "a/b", "a__b", "a_b", "_", "", "a-b", "a_2Db", "x y", "x_20y"}
	for _, composite := range []bool{false, true} {
		seen := map[string]string{}
		for _, in := range inputs {
			out := EncodeID(in, composite)
			for i := 0; i < len(out); i++ {
				require.True(t, isIDChar(out[i]), "invalid char in %q", out)
			}
			if prev, ok := seen[out]; ok {
				t.Fatalf("collision: %q and %q -> %q", prev, in, out)
			}
			seen[out] = in
		}
	}
}

func TestCompositeIDsDoNotCollide(t *testing.T) {
	tr, err := New(model.Mapping{PrimaryKey: []string{"a", "b"}}, nil)
	require.NoError(t, err)
	c1, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"a": "x-y", "b": "z"}})
	require.NoError(t, err)
	c2, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"a": "x", "b": "y-z"}})
	require.NoError(t, err)
	assert.NotEqual(t, c1.ID, c2.ID)
	assert.Equal(t, c1.ID, c1.Doc["id"])
	_, hasA := c1.Doc["a"]
	assert.False(t, hasA, "key columns are dropped unless keepKeyColumns")
}

func TestLongIDsAreHashed(t *testing.T) {
	tr, _ := New(model.Mapping{}, []string{"id"})
	c, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": strings.Repeat("x", 600)}})
	require.NoError(t, err)
	assert.LessOrEqual(t, len(c.ID), 511)
	assert.True(t, strings.HasPrefix(c.ID, "h-"))
}

func TestIntegerIDsStayNumeric(t *testing.T) {
	tr, _ := New(model.Mapping{}, []string{"id"})
	c, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(7), "name": "a"}})
	require.NoError(t, err)
	assert.Equal(t, int64(7), c.Doc["id"])
	assert.Equal(t, "7", c.ID)

	c, err = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": float64(9)}})
	require.NoError(t, err)
	assert.Equal(t, int64(9), c.Doc["id"])
}

func TestMappingFields(t *testing.T) {
	m := model.Mapping{
		PrimaryKey:      []string{"uid"},
		PrimaryKeyField: "id",
		Include:         []string{"title", "price", "secret", "lat", "lng", "created"},
		Exclude:         []string{"secret"},
		Rename:          map[string]string{"title": "name"},
		Geo:             &model.GeoMapping{Lat: "lat", Lng: "lng"},
		TimeFormat:      model.TimeUnix,
		Static:          map[string]any{"tenant": "acme"},
	}
	tr, err := New(m, nil)
	require.NoError(t, err)
	ts := time.Date(2024, 1, 2, 3, 4, 5, 0, time.UTC)
	c, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{
		"uid": "u-1", "title": "Book", "price": 9.5, "secret": "x", "other": 1,
		"lat": 48.8, "lng": "2.35", "created": ts, "nan": math.NaN(),
	}})
	require.NoError(t, err)
	assert.Equal(t, Doc{
		"id": "u-1", "name": "Book", "price": 9.5, "lat": 48.8, "lng": "2.35",
		"created": ts.Unix(), "tenant": "acme",
		"_geo": map[string]any{"lat": 48.8, "lng": 2.35},
	}, c.Doc)
}

func TestTimeFormatsAndBytes(t *testing.T) {
	ts := time.Date(2024, 1, 2, 3, 4, 5, 6000000, time.FixedZone("x", 3600))
	for format, want := range map[model.TimeFormat]any{
		model.TimeRFC3339: "2024-01-02T02:04:05.006Z",
		model.TimeUnix:    ts.Unix(),
		model.TimeUnixMs:  ts.UnixMilli(),
	} {
		tr, _ := New(model.Mapping{TimeFormat: format}, []string{"id"})
		c, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "t": ts, "b": []byte("hi"), "n": []any{map[string]any{"t": ts}}}})
		require.NoError(t, err)
		assert.Equal(t, want, c.Doc["t"], format)
		assert.Equal(t, "aGk=", c.Doc["b"])
		assert.Equal(t, want, c.Doc["n"].([]any)[0].(map[string]any)["t"])
	}
}

func TestSoftDelete(t *testing.T) {
	tr, _ := New(model.Mapping{SoftDeleteColumn: "deleted_at"}, []string{"id"})
	c, _ := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "deleted_at": time.Now()}})
	assert.Equal(t, model.OpDelete, c.Op)
	c, _ = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "deleted_at": nil}})
	assert.Equal(t, model.OpUpsert, c.Op)
	c, _ = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "deleted_at": false}})
	assert.Equal(t, model.OpUpsert, c.Op)

	tr, _ = New(model.Mapping{SoftDeleteColumn: "status", SoftDeleteValue: "archived"}, []string{"id"})
	c, _ = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "status": "ARCHIVED"}})
	assert.Equal(t, model.OpDelete, c.Op)
	c, _ = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"id": int64(1), "status": "live"}})
	assert.Equal(t, model.OpUpsert, c.Op)
}

func TestMissingKeyAndDeletes(t *testing.T) {
	tr, _ := New(model.Mapping{}, []string{"id"})
	_, err := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"name": "x"}})
	assert.ErrorIs(t, err, ErrMissingKey)
	c, err := tr.Apply(model.Event{Op: model.OpDelete, Row: map[string]any{"id": int64(3)}})
	require.NoError(t, err)
	assert.Equal(t, "3", c.ID)
	assert.Nil(t, c.Doc)
	c, err = tr.Apply(model.Event{Op: model.OpTruncate})
	require.NoError(t, err)
	assert.Equal(t, model.OpTruncate, c.Op)

	_, err = New(model.Mapping{}, nil)
	assert.Error(t, err)
}

func TestKeepKeyColumnsAndMongoID(t *testing.T) {
	tr, _ := New(model.Mapping{}, []string{"_id"})
	c, _ := tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"_id": "65a1f0", "name": "x"}})
	assert.Equal(t, Doc{"id": "65a1f0", "name": "x"}, c.Doc)

	tr, _ = New(model.Mapping{KeepKeyColumns: true}, []string{"_id"})
	c, _ = tr.Apply(model.Event{Op: model.OpUpsert, Row: map[string]any{"_id": "65a1f0"}})
	assert.Equal(t, Doc{"id": "65a1f0", "_id": "65a1f0"}, c.Doc)
}
