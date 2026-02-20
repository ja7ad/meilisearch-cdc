//go:build integration

package integration

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ja7ad/meilisearch-cdc/internal/config"
	"github.com/ja7ad/meilisearch-cdc/internal/diff"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
	"github.com/ja7ad/meilisearch-cdc/internal/source/memory"
)

func memTask(table, index string) map[string]any {
	return map[string]any{
		"name":   "memory " + table,
		"source": map[string]any{"type": "memory", "table": table},
		"index": map[string]any{
			"uid":            index,
			"manageSettings": true,
			"settings":       map[string]any{"filterableAttributes": []string{"category", "price"}, "sortableAttributes": []string{"price"}},
		},
		"sync": map[string]any{"batchSize": 200, "flushInterval": "50ms", "safety": map[string]any{"zeroDowntimeResync": true}},
	}
}

func seed(table string, n int) *memory.Tab {
	tab := memory.Table(table)
	for i := 1; i <= n; i++ {
		tab.Upsert(map[string]any{"id": int64(i), "name": fmt.Sprintf("product %d", i), "category": []string{"a", "b"}[i%2], "price": float64(i)})
	}
	return tab
}

func TestSnapshotThenStream(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("snap"), uniq("idx")
	tab := seed(table, 1500)

	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	require.NotEmpty(t, v.ID)
	assert.Equal(t, model.StateRunning, v.Desired)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 1500)

	s, err := e.meili.Index(index).GetSettings(context.Background())
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"category", "price"}, s.FilterableAttributes)
	idx, err := e.meili.GetIndex(context.Background(), index)
	require.NoError(t, err)
	assert.Equal(t, "id", idx.PrimaryKey)

	// Live changes.
	tab.Upsert(map[string]any{"id": int64(1), "name": "renamed", "category": "a", "price": 1.0})
	tab.Upsert(map[string]any{"id": int64(5000), "name": "new", "category": "b", "price": 9.0})
	tab.Delete(int64(2))
	e.waitDocs(index, 1500)
	eventually(t, 30*time.Second, func() bool {
		d := e.doc(index, "1")
		return d != nil && d["name"] == "renamed" && e.doc(index, "2") == nil && e.doc(index, "5000") != nil
	}, func() string { return "live changes not applied" })

	// Counters move when the writer confirms the Meilisearch task, which can
	// trail the documents becoming visible by a poll interval.
	var st model.Status
	eventually(t, 10*time.Second, func() bool {
		st = e.status(v.ID)
		return st.Counters.Upserted >= 1502 && st.Counters.Deleted >= 1
	}, func() string { return fmt.Sprintf("counters: %+v", st.Counters) })
	assert.Equal(t, int64(1500), st.Snapshot.Done)
	assert.NotEmpty(t, st.Checkpoint)

	// Filtering works thanks to managed settings.
	res, err := e.meili.Index(index).Search(context.Background(), "", &meilisearch.SearchRequest{Filter: "category = a AND price < 10", Limit: 100})
	require.NoError(t, err)
	assert.NotEmpty(t, res.Hits)

	// Meilisearch tasks are tagged with the CDC task id.
	tasks, err := e.meili.GetTasks(context.Background(), &meilisearch.TasksQuery{IndexUIDS: []string{index}, Types: []meilisearch.TaskType{meilisearch.TaskTypeDocumentAdditionOrUpdate}, Limit: 1})
	require.NoError(t, err)
	require.NotEmpty(t, tasks.Results)
	assert.Equal(t, "cdc:"+v.ID, tasks.Results[0].CustomMetadata)
}

func TestStopResumeFromCheckpoint(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("resume"), uniq("idx")
	tab := seed(table, 100)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 100)

	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/pause", nil, &v, 200)
	assert.Equal(t, model.StatePaused, v.Status.State)
	for i := 101; i <= 150; i++ {
		tab.Upsert(map[string]any{"id": int64(i), "name": "while paused"})
	}
	time.Sleep(300 * time.Millisecond)
	assert.Equal(t, int64(100), e.docCount(index), "paused task must not write")

	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/resume", nil, nil, 200)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 150)
	assert.Equal(t, int64(100), e.status(v.ID).Snapshot.Done, "resume streams from the checkpoint, no new snapshot")
}

func TestRestartServicePreservesTasks(t *testing.T) {
	dir := t.TempDir()
	table, index := uniq("persist"), uniq("idx")
	tab := seed(table, 20)
	e := newEnv(t, func(c *config.Config) { c.DataDir = dir })
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 20)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	e.srv.Close()
	require.NoError(t, e.app.Close(ctx))

	tab.Upsert(map[string]any{"id": int64(21), "name": "after restart"})
	e2 := newEnv(t, func(c *config.Config) { c.DataDir = dir })
	e2.waitState(v.ID, model.StateStreaming)
	e2.waitDocs(index, 21)
	var got model.TaskView
	e2.mustDo("GET", "/api/v1/tasks/"+v.ID, nil, &got, 200)
	assert.GreaterOrEqual(t, got.Status.Counters.Upserted, int64(20), "counters survive restarts")
}

func TestZeroDowntimeResync(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("resync"), uniq("idx")
	seed(table, 300)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 300)

	// A ghost document that does not exist in the source.
	info, err := e.meili.Index(index).AddDocuments(context.Background(), []map[string]any{{"id": 999999, "name": "ghost"}}, nil)
	require.NoError(t, err)
	_, err = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)
	require.NoError(t, err)
	e.waitDocs(index, 301)

	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/resync", nil, nil, 200)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 300)
	assert.Nil(t, e.doc(index, "999999"))

	var indexes []map[string]any
	e.mustDo("GET", "/api/v1/indexes", nil, &indexes, 200)
	for _, ix := range indexes {
		assert.NotContains(t, ix["uid"], index+"__cdc_shadow", "shadow index cleaned up")
	}
	s, _ := e.meili.Index(index).GetSettings(context.Background())
	assert.ElementsMatch(t, []string{"category", "price"}, s.FilterableAttributes, "settings carried over by the swap")
}

func TestPoisonDocumentAndDLQReplay(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("dlq"), uniq("idx")
	tab := seed(table, 10)
	tab.Upsert(map[string]any{"id": int64(11), "name": "bad geo", "_geo": "not-a-geo"})
	// Meilisearch rejects a malformed _vectors with the generic "internal"
	// code; it must still be isolated instead of retried forever.
	tab.Upsert(map[string]any{"id": int64(12), "name": "bad vectors", "_vectors": "bad"})
	task := memTask(table, index)
	// Meilisearch validates _geo only when it is filterable or sortable.
	task["index"].(map[string]any)["settings"] = map[string]any{"filterableAttributes": []string{"_geo"}}
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 10)

	var dlq []model.DLQEntry
	eventually(t, 30*time.Second, func() bool {
		e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/dlq", nil, &dlq, 200)
		return len(dlq) == 2
	}, func() string { return fmt.Sprintf("dlq has %d entries", len(dlq)) })
	byDoc := map[string]model.DLQEntry{}
	for _, d := range dlq {
		byDoc[d.DocID] = d
	}
	assert.Contains(t, byDoc["11"].Code, "geo")
	assert.Contains(t, byDoc["12"].Error, "expected a map")
	assert.Equal(t, int64(2), e.status(v.ID).Counters.DLQ)
	e.waitDocs(index, 10) // the good documents were not held back

	// Still broken: replay keeps them.
	var rr map[string]int
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/dlq/replay", map[string]any{}, &rr, 200)
	assert.Equal(t, map[string]int{"replayed": 0, "failed": 2}, rr)

	var dr map[string]int
	e.mustDo("DELETE", "/api/v1/tasks/"+v.ID+"/dlq", map[string]any{"ids": []string{dlq[0].ID, dlq[1].ID}}, &dr, 200)
	assert.Equal(t, 2, dr["deleted"])
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/dlq", nil, &dlq, 200)
	assert.Empty(t, dlq)
}

func TestSafetyHolds(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("safety"), uniq("idx")
	tab := seed(table, 50)
	task := memTask(table, index)
	task["sync"] = map[string]any{"flushInterval": "50ms", "safety": map[string]any{"maxDeleteRatio": 0.5, "minDocsForDeleteGuard": 10}}
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 50)

	tab.Truncate()
	st := e.waitState(v.ID, model.StateHeld)
	assert.Contains(t, st.Error, "truncated")
	assert.Equal(t, int64(50), e.docCount(index), "the index is untouched")

	// Mass delete guard.
	table2, index2 := uniq("safety2"), uniq("idx")
	tab2 := seed(table2, 100)
	task2 := memTask(table2, index2)
	task2["sync"] = map[string]any{"batchSize": 1000, "flushInterval": "500ms", "safety": map[string]any{"maxDeleteRatio": 0.5, "minDocsForDeleteGuard": 10}}
	var v2 model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task2, &v2, 201)
	e.waitState(v2.ID, model.StateStreaming)
	e.waitDocs(index2, 100)
	for i := 1; i <= 80; i++ {
		tab2.Delete(int64(i))
	}
	st = e.waitState(v2.ID, model.StateHeld)
	assert.Contains(t, st.Error, "delete")
	assert.Equal(t, int64(100), e.docCount(index2))
}

func TestPrimaryKeyMismatchFails(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("pk"), uniq("idx")
	seed(table, 5)
	info, err := e.meili.CreateIndex(context.Background(), &meilisearch.IndexConfig{Uid: index, PrimaryKey: "sku"})
	require.NoError(t, err)
	_, _ = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)
	info, err = e.meili.Index(index).AddDocuments(context.Background(), []map[string]any{{"sku": "a"}}, nil)
	require.NoError(t, err)
	_, _ = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)

	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	st := e.waitState(v.ID, model.StateFailed)
	assert.Contains(t, st.Error, "primary key mismatch")
	assert.Equal(t, int64(1), e.docCount(index))
}

func TestDiffAndRepair(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("diff"), uniq("idx")
	seed(table, 200)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 200)
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/pause", nil, nil, 200)

	ctx := context.Background()
	ix := e.meili.Index(index)
	t1, _ := ix.DeleteDocument(ctx, "5", nil)
	t2, _ := ix.UpdateDocuments(ctx, []map[string]any{{"id": 7, "name": "tampered"}}, nil)
	t3, _ := ix.AddDocuments(ctx, []map[string]any{{"id": 424242, "name": "extra"}}, nil)
	for _, ti := range []*meilisearch.TaskInfo{t1, t2, t3} {
		_, err := e.meili.WaitForTask(ctx, ti.TaskUID, 50*time.Millisecond)
		require.NoError(t, err)
	}

	runDiff := func(path string, body any) diff.Report {
		e.mustDo("POST", "/api/v1/tasks/"+v.ID+path, body, nil, 202)
		var rep diff.Report
		eventually(t, 60*time.Second, func() bool {
			e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/diff", nil, &rep, 200)
			return rep.Status == diff.StatusDone || rep.Status == diff.StatusFailed
		}, func() string { return "diff did not finish" })
		require.Equal(t, diff.StatusDone, rep.Status, rep.Error)
		return rep
	}
	rep := runDiff("/diff", map[string]any{"compareFields": true})
	assert.Equal(t, int64(1), rep.Missing)
	assert.Equal(t, int64(1), rep.Extra)
	assert.Equal(t, int64(1), rep.Mismatched)
	assert.Equal(t, []string{"5"}, rep.Samples.Missing)
	require.Len(t, rep.Samples.Mismatched, 1)
	assert.Equal(t, "7", rep.Samples.Mismatched[0].ID)

	rep = runDiff("/diff/repair", map[string]any{"missing": true, "mismatched": true, "extra": true})
	require.NotNil(t, rep.Repaired)
	assert.Equal(t, 2, rep.Repaired.Upserted)
	assert.Equal(t, 1, rep.Repaired.Deleted)
	rep = runDiff("/diff", map[string]any{"compareFields": true})
	assert.True(t, rep.InSync, "%+v", rep)
}

func TestSchemaCheckAndTestConnection(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("schema"), uniq("idx")
	seed(table, 3)
	task := memTask(table, index)

	var tr struct {
		OK         bool             `json:"ok"`
		Sample     []map[string]any `json:"sample"`
		PrimaryKey string           `json:"primaryKey"`
		Schema     model.Schema     `json:"schema"`
	}
	e.mustDo("POST", "/api/v1/tasks/test", task, &tr, 200)
	assert.True(t, tr.OK)
	assert.Len(t, tr.Sample, 3)
	assert.Equal(t, "id", tr.PrimaryKey)

	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 3)
	var rep diff.SchemaReport
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/schema-check", nil, &rep, 200)
	assert.True(t, rep.PrimaryKey.OK)
	names := map[string]diff.FieldReport{}
	for _, f := range rep.Fields {
		names[f.Name] = f
	}
	assert.True(t, names["name"].InSource && names["name"].InIndex)
	assert.Contains(t, names["category"].Settings, "filterable")
	assert.InDelta(t, 1.0, names["price"].Coverage, 0.001)

	bad := memTask(table, index)
	bad["source"] = map[string]any{"type": "postgres", "dsn": "postgres://nobody:x@127.0.0.1:1/none", "table": "x"}
	e.mustDo("POST", "/api/v1/tasks/test", bad, &tr, 200)
	assert.False(t, tr.OK)
}

func TestValidationAuthAndConflicts(t *testing.T) {
	e := newEnv(t)
	var body map[string]any
	assert.Equal(t, 422, e.do("POST", "/api/v1/tasks", map[string]any{"name": ""}, &body))
	assert.NotEmpty(t, body["details"])

	tok := e.token
	e.token = ""
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/api/v1/tasks", nil, nil))
	assert.Equal(t, http.StatusUnauthorized, e.do("GET", "/metrics", nil, nil))
	e.token = tok
	assert.Equal(t, 200, e.do("GET", "/metrics", nil, nil))
	assert.Equal(t, 404, e.do("GET", "/api/v1/tasks/nope", nil, nil))

	table, index := uniq("conf"), uniq("idx")
	seed(table, 1)
	e.mustDo("POST", "/api/v1/tasks", memTask(table, index), nil, 201)
	other := memTask(table, index)
	other["mapping"] = map[string]any{"primaryKeyField": "sku"}
	assert.Equal(t, http.StatusConflict, e.do("POST", "/api/v1/tasks", other, nil), "two tasks, one index, different keys")

	// Secrets are encrypted at rest and redacted by the API.
	pg := memTask(table, uniq("idx"))
	pg["source"] = map[string]any{"type": "postgres", "dsn": "postgres://u:topsecret@db/app", "table": "t"}
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks", pg, &v, 201)
	assert.NotContains(t, v.Source.DSN, "topsecret")
	v.Name = "renamed"
	var upd model.TaskView
	e.mustDo("PUT", "/api/v1/tasks/"+v.ID, v.Task, &upd, 200)
	stored, err := e.app.Engine.Get(v.ID)
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(stored.Source.DSN, "enc:v2:"), "encrypted at rest and in memory")
	full, err := e.app.Engine.Reveal(stored)
	require.NoError(t, err)
	assert.Equal(t, "postgres://u:topsecret@db/app", full.Source.DSN, "masked secret kept on update")
	assert.Equal(t, "renamed", full.Name)
}

func TestBatchActionsAndImportExport(t *testing.T) {
	e := newEnv(t)
	var ids []string
	for i := 0; i < 3; i++ {
		table, index := uniq("batch"), uniq("idx")
		seed(table, 5)
		var v model.TaskView
		e.mustDo("POST", "/api/v1/tasks", memTask(table, index), &v, 201)
		ids = append(ids, v.ID)
	}
	var res struct {
		Results []struct {
			ID string `json:"id"`
			OK bool   `json:"ok"`
		} `json:"results"`
	}
	e.mustDo("POST", "/api/v1/tasks/batch", map[string]any{"ids": ids, "action": "start"}, &res, 200)
	require.Len(t, res.Results, 3)
	for _, r := range res.Results {
		assert.True(t, r.OK)
	}
	for _, id := range ids {
		e.waitState(id, model.StateStreaming)
	}
	e.mustDo("POST", "/api/v1/tasks/batch", map[string]any{"ids": ids, "action": "stop"}, &res, 200)
	for _, id := range ids {
		assert.Equal(t, model.StateStopped, e.status(id).State)
	}

	var exported []model.Task
	e.mustDo("GET", "/api/v1/tasks/export", nil, &exported, 200)
	assert.Len(t, exported, 3)
	e.mustDo("POST", "/api/v1/tasks/batch", map[string]any{"ids": ids, "action": "delete"}, &res, 200)
	var list []model.TaskView
	e.mustDo("GET", "/api/v1/tasks", nil, &list, 200)
	assert.Empty(t, list)
	var imp map[string]any
	e.mustDo("POST", "/api/v1/tasks/import", exported, &imp, 200)
	assert.EqualValues(t, 3, imp["created"])
}

func TestIndexManagementAPI(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("ixapi"), uniq("idx")
	seed(table, 30)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 30)

	var info map[string]any
	e.mustDo("GET", "/api/v1/indexes/"+index, nil, &info, 200)
	assert.Equal(t, []any{v.ID}, info["managedBy"])

	var preview struct {
		Changes []map[string]any `json:"changes"`
	}
	e.mustDo("POST", "/api/v1/indexes/"+index+"/settings/preview", map[string]any{"sortableAttributes": []string{"price", "name"}}, &preview, 200)
	require.Len(t, preview.Changes, 1)
	assert.Equal(t, "sortableAttributes", preview.Changes[0]["key"])
	assert.Equal(t, 422, e.do("POST", "/api/v1/indexes/"+index+"/settings/preview", map[string]any{"nope": 1}, nil))

	var applied map[string]any
	e.mustDo("PATCH", "/api/v1/indexes/"+index+"/settings", map[string]any{"sortableAttributes": []string{"price", "name"}}, &applied, 200)
	assert.Equal(t, "succeeded", applied["status"])

	var sr map[string]any
	e.mustDo("POST", "/api/v1/indexes/"+index+"/search", map[string]any{"q": "product", "sort": []string{"name:asc"}, "limit": 5, "filter": "price > 3"}, &sr, 200)
	assert.Len(t, sr["hits"], 5)

	var docs map[string]any
	e.mustDo("GET", "/api/v1/indexes/"+index+"/documents?limit=7", nil, &docs, 200)
	assert.Len(t, docs["results"], 7)
	assert.EqualValues(t, 30, docs["total"])

	assert.Equal(t, http.StatusConflict, e.do("PUT", "/api/v1/indexes/"+index+"/primary-key", map[string]any{"primaryKey": "name"}, nil))
	assert.Equal(t, 400, e.do("DELETE", "/api/v1/indexes/"+index+"?confirm=wrong", nil, nil))
	assert.Equal(t, http.StatusConflict, e.do("DELETE", "/api/v1/indexes/"+index+"?confirm="+index, nil, nil), "busy index")

	var mt map[string]any
	e.mustDo("GET", "/api/v1/meili/tasks?indexUid="+index+"&limit=5", nil, &mt, 200)
	assert.NotEmpty(t, mt["results"])
	var ov map[string]any
	e.mustDo("GET", "/api/v1/overview", nil, &ov, 200)
	assert.Equal(t, true, ov["meilisearch"].(map[string]any)["healthy"])

	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/stop", nil, nil, 200)
	e.mustDo("DELETE", "/api/v1/indexes/"+index+"?confirm="+index, nil, nil, 202)
}

func TestServerSentEvents(t *testing.T) {
	e := newEnv(t)
	req, _ := http.NewRequest("GET", e.srv.URL+"/api/v1/events?token="+e.token, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, "text/event-stream", resp.Header.Get("Content-Type"))

	table, index := uniq("sse"), uniq("idx")
	seed(table, 5)
	go func() {
		time.Sleep(300 * time.Millisecond)
		e.do("POST", "/api/v1/tasks?start=true", memTask(table, index), nil)
	}()
	seen := map[string]bool{}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			seen[strings.TrimPrefix(line, "event: ")] = true
		}
		if strings.HasPrefix(line, "data: ") && seen["status"] {
			var st model.Status
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &st) == nil && st.State == model.StateStreaming {
				break
			}
		}
	}
	assert.True(t, seen["overview"])
	assert.True(t, seen["task"])
	assert.True(t, seen["log"])
	assert.True(t, seen["status"])
}

func TestDeclarativeTasksFile(t *testing.T) {
	table, index := uniq("decl"), uniq("idx")
	seed(table, 12)
	file := filepath.Join(t.TempDir(), "tasks.yaml")
	t.Setenv("DECL_TABLE", table)
	require.NoError(t, os.WriteFile(file, []byte(fmt.Sprintf(`
tasks:
  - id: declared
    name: Declared task
    desiredState: running
    source: {type: memory, table: "${DECL_TABLE}"}
    index: {uid: %s}
    sync: {flushInterval: 50ms}
`, index)), 0o600))
	e := newEnv(t, func(c *config.Config) { c.TasksFile = file })
	e.waitState("declared", model.StateStreaming)
	e.waitDocs(index, 12)
}

func TestSQLPollWithSQLite(t *testing.T) {
	e := newEnv(t)
	path := filepath.Join(t.TempDir(), "shop.db")
	db := openSQLite(t, path)
	_, err := db.Exec(`CREATE TABLE products (id INTEGER PRIMARY KEY, name TEXT, price REAL, version INTEGER NOT NULL)`)
	require.NoError(t, err)
	for i := 1; i <= 50; i++ {
		_, err := db.Exec(`INSERT INTO products VALUES (?, ?, ?, ?)`, i, fmt.Sprintf("p%d", i), float64(i), i)
		require.NoError(t, err)
	}
	index := uniq("idx")
	task := map[string]any{
		"name":   "sqlite poll",
		"source": map[string]any{"type": "sqlpoll", "driver": "sqlite", "dsn": path, "table": "products", "cursorColumn": "version", "pollInterval": "100ms"},
		"index":  map[string]any{"uid": index},
		"sync":   map[string]any{"flushInterval": "50ms"},
	}
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 50)
	_, err = db.Exec(`UPDATE products SET name = 'changed', version = 1000 WHERE id = 3`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO products VALUES (51, 'new', 1.0, 1001)`)
	require.NoError(t, err)
	e.waitDocs(index, 51)
	eventually(t, 30*time.Second, func() bool {
		d := e.doc(index, "3")
		return d != nil && d["name"] == "changed"
	}, func() string { return "update not synced" })
}

func TestWebhookIngest(t *testing.T) {
	e := newEnv(t)
	index := uniq("idx")
	task := map[string]any{
		"name":    "webhook orders",
		"source":  map[string]any{"type": "webhook", "table": "orders"},
		"index":   map[string]any{"uid": index},
		"mapping": map[string]any{"primaryKey": []string{"id"}},
		"sync":    map[string]any{"flushInterval": "20ms"},
	}
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks", task, &v, 201)
	assert.Equal(t, http.StatusServiceUnavailable, e.do("POST", "/api/v1/ingest/"+v.ID, map[string]any{"row": map[string]any{"id": 1}}, nil))

	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/start", nil, nil, 200)
	e.waitState(v.ID, model.StateStreaming)
	var res map[string]any
	e.mustDo("POST", "/api/v1/ingest/"+v.ID, []map[string]any{
		{"op": "upsert", "row": map[string]any{"id": 1, "total": 9.5}},
		{"row": map[string]any{"id": 2, "total": 3}},
	}, &res, 200)
	assert.EqualValues(t, 2, res["accepted"])
	// A 200 means the documents are already confirmed in Meilisearch.
	assert.Equal(t, int64(2), e.docCount(index))
	e.mustDo("POST", "/api/v1/ingest/"+v.ID, map[string]any{"op": "delete", "row": map[string]any{"id": 1}}, nil, 200)
	assert.Equal(t, int64(1), e.docCount(index))
	assert.Equal(t, 400, e.do("POST", "/api/v1/ingest/"+v.ID, "not json", nil))
}

func TestTaskIndexSettings(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("settings"), uniq("idx")
	seed(table, 10)
	task := memTask(table, index)
	desired := map[string]any{
		"filterableAttributes": []any{map[string]any{"attributePatterns": []string{"category"}, "features": map[string]any{"facetSearch": true, "filter": map[string]any{"equality": true, "comparison": false}}}, "price"},
		"sortableAttributes":   []string{"price"},
		"searchableAttributes": []string{"name", "category"},
		"synonyms":             map[string]any{"book": []string{"novel"}},
		"typoTolerance":        map[string]any{"enabled": true, "minWordSizeForTypos": map[string]any{"oneTypo": 4, "twoTypos": 9}},
		"facetSearch":          false,
		"pagination":           map[string]any{"maxTotalHits": 5000},
	}
	task["index"] = map[string]any{"uid": index, "manageSettings": true, "settings": desired}
	assert.Equal(t, 422, e.do("POST", "/api/v1/tasks", map[string]any{"name": "bad", "source": task["source"],
		"index": map[string]any{"uid": index, "settings": map[string]any{"filterableAtributes": []string{"x"}}}}, nil), "typos in settings are rejected")

	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	var ts engine.TaskSettings
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/settings", nil, &ts, 200)
	assert.True(t, ts.IndexExists)
	assert.True(t, ts.InSync, "changes: %+v", ts.Changes)
	var live map[string]any
	e.mustDo("GET", "/api/v1/indexes/"+index+"/settings", nil, &live, 200)
	assert.Equal(t, false, live["facetSearch"])
	assert.Contains(t, fmt.Sprint(live["filterableAttributes"]), "attributePatterns")

	// Editing a stopped task applies the new settings right away.
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/stop", nil, nil, 200)
	desired["stopWords"] = []string{"the", "a"}
	var full model.TaskView
	e.mustDo("GET", "/api/v1/tasks/"+v.ID, nil, &full, 200)
	full.Index.Settings, _ = json.Marshal(desired)
	e.mustDo("PUT", "/api/v1/tasks/"+v.ID, full.Task, nil, 200)
	e.mustDo("GET", "/api/v1/indexes/"+index+"/settings", nil, &live, 200)
	assert.ElementsMatch(t, []any{"the", "a"}, live["stopWords"])

	// Out of band changes show up as drift and can be re-applied.
	info, err := e.meili.Index(index).UpdateSortableAttributes(context.Background(), &[]string{"name"})
	require.NoError(t, err)
	_, err = e.meili.WaitForTask(context.Background(), info.TaskUID, 50*time.Millisecond)
	require.NoError(t, err)
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/settings", nil, &ts, 200)
	require.False(t, ts.InSync)
	require.Len(t, ts.Changes, 1)
	assert.Equal(t, "sortableAttributes", ts.Changes[0].Key)
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/settings/apply", nil, &ts, 200)
	assert.True(t, ts.InSync)

	// Zero-downtime resync keeps granular settings on the swapped index.
	e.mustDo("POST", "/api/v1/tasks/"+v.ID+"/resync", nil, nil, 200)
	e.waitState(v.ID, model.StateStreaming)
	e.mustDo("GET", "/api/v1/tasks/"+v.ID+"/settings", nil, &ts, 200)
	assert.True(t, ts.InSync, "changes after resync: %+v", ts.Changes)
}

// Lag is the delay of the last confirmed change; once a task has been
// quiet with nothing in flight it reads as caught up, so lag alerts do not
// fire on idle tables.
func TestIdleTaskLagResets(t *testing.T) {
	e := newEnv(t)
	table, index := uniq("lag"), uniq("idx")
	tab := seed(table, 5)
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", memTask(table, index), &v, 201)
	e.waitState(v.ID, model.StateStreaming)
	e.waitDocs(index, 5)

	tab.Upsert(map[string]any{"id": int64(6), "name": "late", "version": int64(1)})
	eventually(t, 20*time.Second, func() bool { return e.status(v.ID).LagMs > 0 }, func() string { return "no lag measured" })
	eventually(t, 30*time.Second, func() bool { return e.status(v.ID).LagMs == 0 }, func() string {
		return fmt.Sprintf("lag still %dms after the task went quiet", e.status(v.ID).LagMs)
	})
}

// A webhook task with a signing secret accepts signed pushes without a token
// and refuses unsigned, stale, tampered and token-only requests.
func TestWebhookSignedIngest(t *testing.T) {
	e := newEnv(t)
	index := uniq("idx")
	const secret = "whsec_integration_secret_0123"
	task := map[string]any{
		"name":    "signed webhook",
		"source":  map[string]any{"type": "webhook", "table": "orders", "webhookSecret": "short"},
		"index":   map[string]any{"uid": index},
		"mapping": map[string]any{"primaryKey": []string{"id"}},
		"sync":    map[string]any{"flushInterval": "20ms"},
	}
	assert.Equal(t, 422, e.do("POST", "/api/v1/tasks", task, nil), "secrets shorter than 16 characters are refused")
	task["source"].(map[string]any)["webhookSecret"] = secret
	var v model.TaskView
	e.mustDo("POST", "/api/v1/tasks?start=true", task, &v, 201)
	assert.Equal(t, model.SecretMask, v.Source.WebhookSecret, "the secret is never returned")
	e.waitState(v.ID, model.StateStreaming)

	push := func(body string, hdr func(h http.Header)) int {
		req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/ingest/"+v.ID, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		hdr(req.Header)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		resp.Body.Close()
		return resp.StatusCode
	}
	body := `{"op":"upsert","row":{"id":1,"total":9.5}}`
	now := time.Now()
	assert.Equal(t, 200, push(body, func(h http.Header) { signature.SetHeaders(h, secret, now, []byte(body)) }), "signed, no token")
	assert.Equal(t, int64(1), e.docCount(index))

	assert.Equal(t, 401, push(body, func(http.Header) {}), "unsigned")
	assert.Equal(t, 401, push(body, func(h http.Header) { h.Set("Authorization", "Bearer "+e.token) }), "a token alone is not enough once a secret is set")
	assert.Equal(t, 401, push(body, func(h http.Header) { signature.SetHeaders(h, "whsec_wrong_secret_value", now, []byte(body)) }), "wrong secret")
	assert.Equal(t, 401, push(`{"op":"delete","row":{"id":1}}`, func(h http.Header) { signature.SetHeaders(h, secret, now, []byte(body)) }), "tampered body")
	assert.Equal(t, 401, push(body, func(h http.Header) { signature.SetHeaders(h, secret, now.Add(-10*time.Minute), []byte(body)) }), "stale")
	assert.Equal(t, int64(1), e.docCount(index))

	// Anonymous callers learn nothing about tasks without a secret.
	req, _ := http.NewRequest("POST", e.srv.URL+"/api/v1/ingest/no-such-task", strings.NewReader(body))
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, 401, resp.StatusCode)

	// Editing with the masked value keeps the secret; removing it brings back token auth.
	var full model.TaskView
	e.mustDo("GET", "/api/v1/tasks/"+v.ID, nil, &full, 200)
	full.Name = "signed webhook (renamed)"
	e.mustDo("PUT", "/api/v1/tasks/"+v.ID, full.Task, nil, 200)
	// The edit restarts the task; pushes answer 503 until it streams again.
	settled := func(f func() int) int {
		code := 0
		eventually(t, 20*time.Second, func() bool { code = f(); return code != 503 }, func() string { return "task did not restart" })
		return code
	}
	assert.Equal(t, 200, settled(func() int {
		return push(body, func(h http.Header) { signature.SetHeaders(h, secret, time.Now(), []byte(body)) })
	}), "secret kept after edit")

	full.Source.WebhookSecret = ""
	e.mustDo("PUT", "/api/v1/tasks/"+v.ID, full.Task, nil, 200)
	assert.Equal(t, 200, settled(func() int { return push(body, func(h http.Header) { h.Set("Authorization", "Bearer "+e.token) }) }))
	assert.Equal(t, 401, push(body, func(h http.Header) { signature.SetHeaders(h, secret, time.Now(), []byte(body)) }), "no secret: signatures do not authenticate")
}
