package diff

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/source/memory"
	"github.com/ja7ad/meilisearch-cdc/internal/transform"
)

// fakeIndex serves documents and applies repairs.
type fakeIndex struct{ docs map[string]map[string]any }

func (f *fakeIndex) GetDocuments(_ context.Context, q *meilisearch.DocumentsQuery, resp *meilisearch.DocumentsResult) error {
	ids := make([]string, 0, len(f.docs))
	for id := range f.docs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	resp.Results = nil
	for i := q.Offset; i < int64(len(ids)) && i < q.Offset+q.Limit; i++ {
		hit := meilisearch.Hit{}
		for k, v := range f.docs[ids[i]] {
			raw, _ := json.Marshal(v)
			hit[k] = raw
		}
		resp.Results = append(resp.Results, hit)
	}
	resp.Total = int64(len(ids))
	return nil
}

func (f *fakeIndex) ApplySync(_ context.Context, ups, repl []transform.Doc, dels []string) ([]model.DLQEntry, error) {
	for _, d := range append(ups, repl...) {
		f.docs[fmt.Sprint(d["id"])] = d
	}
	for _, id := range dels {
		delete(f.docs, id)
	}
	return nil, nil
}

func setup(t *testing.T) (*Checker, *fakeIndex) {
	memory.Reset()
	tab := memory.Table("products")
	for i := 1; i <= 5; i++ {
		tab.Upsert(map[string]any{"id": int64(i), "name": fmt.Sprintf("p%d", i), "price": float64(i)})
	}
	idx := &fakeIndex{docs: map[string]map[string]any{}}
	for i := 1; i <= 5; i++ {
		idx.docs[fmt.Sprint(i)] = map[string]any{"id": i, "name": fmt.Sprintf("p%d", i), "price": i, "_rankingExtra": "ignored"}
	}
	delete(idx.docs, "2")                                      // missing
	idx.docs["4"]["name"] = "stale"                            // mismatched
	idx.docs["99"] = map[string]any{"id": 99, "name": "ghost"} // extra

	tr, err := transform.New(model.Mapping{PrimaryKeyField: "id"}, []string{"id"})
	require.NoError(t, err)
	src, err := sourceNew("products")
	require.NoError(t, err)
	return &Checker{TaskID: "t", IndexUID: "products", Source: src, Tr: tr, Index: idx, Repair: idx, PageSize: 2}, idx
}

func TestDiffFindsDifferences(t *testing.T) {
	c, _ := setup(t)
	rep := c.Run(context.Background(), Options{CompareFields: true})
	require.Equal(t, StatusDone, rep.Status, rep.Error)
	assert.Equal(t, int64(5), rep.SourceCount)
	assert.Equal(t, int64(5), rep.IndexCount)
	assert.Equal(t, int64(1), rep.Missing)
	assert.Equal(t, int64(1), rep.Extra)
	assert.Equal(t, int64(1), rep.Mismatched)
	assert.False(t, rep.InSync)
	assert.Equal(t, []string{"2"}, rep.Samples.Missing)
	assert.Equal(t, []string{"99"}, rep.Samples.Extra)
	require.Len(t, rep.Samples.Mismatched, 1)
	assert.Equal(t, "4", rep.Samples.Mismatched[0].ID)
	assert.Equal(t, []FieldDiff{{Field: "name", Source: "p4", Index: "stale"}}, rep.Samples.Mismatched[0].Fields)
	assert.Nil(t, rep.Repaired)
}

func TestDiffWithoutFieldComparison(t *testing.T) {
	c, _ := setup(t)
	rep := c.Run(context.Background(), Options{})
	assert.Zero(t, rep.Mismatched)
	assert.Empty(t, rep.Samples.Mismatched)
}

func TestDiffRepair(t *testing.T) {
	c, idx := setup(t)
	rep := c.Run(context.Background(), Options{CompareFields: true, RepairMissing: true, RepairMismatched: true, RepairExtra: true})
	require.Equal(t, StatusDone, rep.Status, rep.Error)
	require.NotNil(t, rep.Repaired)
	assert.Equal(t, 2, rep.Repaired.Upserted)
	assert.Equal(t, 1, rep.Repaired.Deleted)
	again := c.Run(context.Background(), Options{CompareFields: true})
	assert.True(t, again.InSync, "%+v", again)
	assert.Len(t, idx.docs, 5)
}

func TestCheckSchema(t *testing.T) {
	tk := model.Task{ID: "t", Index: model.Index{UID: "products"}, Mapping: model.Mapping{
		PrimaryKeyField: "id", TimeFormat: model.TimeRFC3339, Geo: &model.GeoMapping{Lat: "lat", Lng: "lng"}, Exclude: []string{"secret"},
	}}
	tr, _ := transform.New(tk.Mapping, []string{"id"})
	schema := &model.Schema{PrimaryKey: []string{"id"}, Columns: []model.Column{
		{Name: "id", Type: "int4", PrimaryKey: true},
		{Name: "title", Type: "text"},
		{Name: "created_at", Type: "timestamptz"},
		{Name: "photo", Type: "bytea"},
		{Name: "secret", Type: "text"},
		{Name: "lat", Type: "float8"}, {Name: "lng", Type: "float8"},
	}}
	stats := &meilisearch.StatsIndex{NumberOfDocuments: 10, FieldDistribution: map[string]int64{"id": 10, "title": 8, "created_at": 10, "old_column": 3}}
	settings := &meilisearch.Settings{FilterableAttributes: []string{"created_at", "category"}, SearchableAttributes: []string{"*"}}
	rep := CheckSchema(SchemaInput{Task: tk, Schema: schema, Tr: tr, IndexPK: "sku", Stats: stats, Settings: settings})

	assert.False(t, rep.PrimaryKey.OK)
	byName := map[string]FieldReport{}
	for _, f := range rep.Fields {
		byName[f.Name] = f
	}
	assert.Equal(t, "id", rep.Fields[0].Name, "primary key first")
	assert.NotContains(t, byName, "secret")
	assert.InDelta(t, 0.8, byName["title"].Coverage, 0.001)
	assert.NotEmpty(t, byName["old_column"].Issues)
	assert.NotEmpty(t, byName["category"].Issues)
	assert.NotEmpty(t, byName["created_at"].Issues)
	assert.NotEmpty(t, byName["photo"].Issues)
	assert.NotEmpty(t, byName["_geo"].Issues)
	assert.Equal(t, "error", rep.Issues[0].Level)

	empty := CheckSchema(SchemaInput{Task: tk, Schema: schema, Tr: tr})
	assert.True(t, empty.PrimaryKey.OK)
	assert.Equal(t, "info", empty.Issues[len(empty.Issues)-1].Level)
}

// Meilisearch returns 1.4 for 1.4000000000000001: that is in sync, while a
// real change of the value is still a mismatch.
func TestFloatPrecisionIsNotAMismatch(t *testing.T) {
	src := map[string]any{"rating": 1.4000000000000001, "tags": []any{0.30000000000000004}, "dims": map[string]any{"w": 2.6000000000000001}}
	idx := map[string]any{"rating": 1.4, "tags": []any{0.3}, "dims": map[string]any{"w": 2.6}}
	fields := []string{"rating", "tags", "dims"}
	if d := fieldDiffs(src, idx, fields); len(d) != 0 {
		t.Fatalf("float precision reported as differences: %+v", d)
	}
	if hashFields(src, fields) != hashFields(idx, fields) {
		t.Fatal("hashes differ for floats equal at Meilisearch precision")
	}
	idx["rating"] = 1.41
	if d := fieldDiffs(src, idx, fields); len(d) != 1 || d[0].Field != "rating" {
		t.Fatalf("real change not reported: %+v", d)
	}
}
