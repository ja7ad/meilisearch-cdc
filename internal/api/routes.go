package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/meilisearch/meilisearch-go"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/diff"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

type route func(pattern string, fn http.HandlerFunc)

type idsReq struct {
	IDs []string `json:"ids"`
}

func (s *Server) qualityRoutes(h route) {
	h("GET /api/v1/tasks/{id}/schema-check", handler(http.StatusOK, func(r *http.Request, _ none) (diff.SchemaReport, error) {
		return s.eng.SchemaCheck(r.Context(), r.PathValue("id"))
	}))
	h("POST /api/v1/tasks/{id}/diff", handler(http.StatusAccepted, func(r *http.Request, o diff.Options) (map[string]string, error) {
		o.RepairMissing, o.RepairMismatched, o.RepairExtra = false, false, false
		if err := s.eng.StartDiff(r.PathValue("id"), o); err != nil {
			return nil, err
		}
		return map[string]string{"status": "running"}, nil
	}))
	h("GET /api/v1/tasks/{id}/diff", handler(http.StatusOK, func(r *http.Request, _ none) (any, error) {
		rep, ok, err := s.eng.DiffReport(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		if !ok {
			return map[string]any{"status": "none"}, nil
		}
		return rep, nil
	}))
	h("POST /api/v1/tasks/{id}/diff/repair", handler(http.StatusAccepted, func(r *http.Request, o diff.Options) (map[string]string, error) {
		if !o.RepairMissing && !o.RepairMismatched && !o.RepairExtra {
			return nil, badRequest("choose at least one of missing, mismatched, extra")
		}
		o.CompareFields = true
		if err := s.eng.StartDiff(r.PathValue("id"), o); err != nil {
			return nil, err
		}
		return map[string]string{"status": "running"}, nil
	}))
	h("GET /api/v1/tasks/{id}/settings", handler(http.StatusOK, func(r *http.Request, _ none) (engine.TaskSettings, error) {
		return s.eng.TaskSettings(r.Context(), r.PathValue("id"))
	}))
	h("POST /api/v1/tasks/{id}/settings/apply", handler(http.StatusOK, func(r *http.Request, _ none) (engine.TaskSettings, error) {
		return s.eng.ApplyTaskSettings(r.Context(), r.PathValue("id"))
	}))
	h("GET /api/v1/tasks/{id}/dlq", handler(http.StatusOK, func(r *http.Request, _ none) ([]model.DLQEntry, error) {
		return s.eng.DLQ(r.PathValue("id"), intParam(r, "limit", 100))
	}))
	h("POST /api/v1/tasks/{id}/dlq/replay", handler(http.StatusOK, func(r *http.Request, req idsReq) (map[string]int, error) {
		ok, failed, err := s.eng.ReplayDLQ(r.Context(), r.PathValue("id"), req.IDs)
		return map[string]int{"replayed": ok, "failed": failed}, err
	}))
	h("DELETE /api/v1/tasks/{id}/dlq", handler(http.StatusOK, func(r *http.Request, req idsReq) (map[string]int, error) {
		if _, err := s.eng.Get(r.PathValue("id")); err != nil {
			return nil, err
		}
		n, err := s.eng.DiscardDLQ(r.PathValue("id"), req.IDs)
		return map[string]int{"deleted": n}, err
	}))
}

type indexInfo struct {
	UID               string    `json:"uid"`
	DestinationID     string    `json:"destinationId"`
	PrimaryKey        string    `json:"primaryKey"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
	NumberOfDocuments int64     `json:"numberOfDocuments"`
	IsIndexing        bool      `json:"isIndexing"`
	ManagedBy         []string  `json:"managedBy"`
	// ManagedByTasks names the tasks of ManagedBy, for display.
	ManagedByTasks []engine.TaskRef `json:"managedByTasks"`
	Shadow         bool             `json:"shadow"`
}

type taskRef struct {
	TaskUID int64  `json:"taskUid"`
	Status  string `json:"status,omitempty"`
	Error   string `json:"error,omitempty"`
}

func ref(info *meilisearch.TaskInfo, err error) (taskRef, error) {
	if err != nil {
		return taskRef{}, err
	}
	return taskRef{TaskUID: info.TaskUID, Status: string(info.Status)}, nil
}

type targetKey struct{}

// withTarget resolves ?destination= (default when empty) for index and
// Meilisearch routes.
func (s *Server) withTarget(fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tg, err := s.eng.Target(r.URL.Query().Get("destination"))
		if err != nil {
			code := http.StatusNotFound
			if errors.Is(err, dest.ErrNoDefault) {
				code = http.StatusConflict
			}
			writeErr(w, &HTTPError{Code: code, Msg: err.Error()})
			return
		}
		fn(w, r.WithContext(context.WithValue(r.Context(), targetKey{}, tg)))
	}
}

func target(r *http.Request) *dest.Target { return r.Context().Value(targetKey{}).(*dest.Target) }

// indexGroup is the index list of one Meilisearch instance.
type indexGroup struct {
	DestinationID   string      `json:"destinationId"`
	DestinationName string      `json:"destinationName"`
	IsDefault       bool        `json:"isDefault"`
	Healthy         bool        `json:"healthy"`
	Error           string      `json:"error,omitempty"`
	Indexes         []indexInfo `json:"indexes"`
}

func (s *Server) listIndexes(ctx context.Context, tg *dest.Target) ([]indexInfo, error) {
	out := []indexInfo{}
	names := map[string]engine.TaskRef{}
	if views, err := s.eng.List("", "", ""); err == nil {
		for _, v := range views {
			names[v.ID] = engine.TaskRef{ID: v.ID, Name: v.Name, State: v.Status.State}
		}
	}
	stats, _ := tg.Client.GetStats(ctx, nil)
	for offset := int64(0); ; offset += 100 {
		res, err := tg.Client.ListIndexes(ctx, &meilisearch.IndexesQuery{Limit: 100, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, ix := range res.Results {
			in := indexInfo{UID: ix.UID, DestinationID: tg.ID, PrimaryKey: ix.PrimaryKey, CreatedAt: ix.CreatedAt, UpdatedAt: ix.UpdatedAt,
				ManagedBy: nonNil(s.eng.ManagedBy(tg.ID, ix.UID)), Shadow: indexmgr.IsShadow(ix.UID)}
			in.ManagedByTasks = make([]engine.TaskRef, len(in.ManagedBy))
			for i, id := range in.ManagedBy {
				if ref, ok := names[id]; ok {
					in.ManagedByTasks[i] = ref
				} else {
					in.ManagedByTasks[i] = engine.TaskRef{ID: id, Name: id}
				}
			}
			if stats != nil {
				st := stats.Indexes[ix.UID]
				in.NumberOfDocuments, in.IsIndexing = st.NumberOfDocuments, st.IsIndexing
			}
			out = append(out, in)
		}
		if int64(len(res.Results)) < 100 {
			return out, nil
		}
	}
}

// indexGroups lists the indexes of every destination in parallel; an
// unreachable instance reports its error instead of failing the list.
func (s *Server) indexGroups(ctx context.Context) ([]indexGroup, error) {
	all, err := s.eng.Destinations().List()
	if err != nil {
		return nil, err
	}
	out := make([]indexGroup, len(all))
	var wg sync.WaitGroup
	for i, d := range all {
		out[i] = indexGroup{DestinationID: d.ID, DestinationName: d.Name, IsDefault: d.IsDefault, Indexes: []indexInfo{}}
		wg.Add(1)
		go func() {
			defer wg.Done()
			g := &out[i]
			tg, err := s.eng.Target(d.ID)
			if err == nil {
				ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
				defer cancel()
				var ix []indexInfo
				if ix, err = s.listIndexes(ctx, tg); err == nil {
					g.Healthy, g.Indexes = true, ix
				}
			}
			if err != nil {
				g.Error = err.Error()
			}
		}()
	}
	wg.Wait()
	return out, nil
}

func (s *Server) indexRoutes(hh route) {
	h := func(pattern string, fn http.HandlerFunc) { hh(pattern, s.withTarget(fn)) }
	c := func(r *http.Request) meilisearch.ServiceManager { return target(r).Client }
	listOne := handler(http.StatusOK, func(r *http.Request, _ none) ([]indexInfo, error) {
		return s.listIndexes(r.Context(), target(r))
	})
	// ?destination=* lists the indexes of every instance, grouped.
	hh("GET /api/v1/indexes", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("destination") == "*" {
			handler(http.StatusOK, func(r *http.Request, _ none) ([]indexGroup, error) {
				return s.indexGroups(r.Context())
			})(w, r)
			return
		}
		s.withTarget(listOne)(w, r)
	})
	h("POST /api/v1/indexes", handler(http.StatusAccepted, func(r *http.Request, req struct {
		UID        string `json:"uid"`
		PrimaryKey string `json:"primaryKey"`
	}) (taskRef, error) {
		if req.UID == "" {
			return taskRef{}, badRequest("uid is required")
		}
		return ref(c(r).CreateIndex(r.Context(), &meilisearch.IndexConfig{Uid: req.UID, PrimaryKey: req.PrimaryKey}))
	}))
	h("GET /api/v1/indexes/{uid}", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		uid := r.PathValue("uid")
		ix, err := c(r).GetIndex(r.Context(), uid)
		if err != nil {
			return nil, err
		}
		st, err := c(r).Index(uid).GetStats(r.Context(), nil)
		if err != nil {
			return nil, err
		}
		return map[string]any{"uid": ix.UID, "primaryKey": ix.PrimaryKey, "createdAt": ix.CreatedAt, "updatedAt": ix.UpdatedAt,
			"stats": st, "managedBy": nonNil(s.eng.ManagedBy(target(r).ID, uid))}, nil
	}))
	h("DELETE /api/v1/indexes/{uid}", handler(http.StatusAccepted, func(r *http.Request, _ none) (taskRef, error) {
		uid := r.PathValue("uid")
		if r.URL.Query().Get("confirm") != uid {
			return taskRef{}, badRequest("confirm must equal the index uid")
		}
		if s.eng.IndexBusy(target(r).ID, uid) {
			return taskRef{}, &HTTPError{Code: http.StatusConflict, Msg: "a running task writes to this index; stop it first"}
		}
		return ref(c(r).DeleteIndex(r.Context(), uid))
	}))
	h("GET /api/v1/indexes/{uid}/settings", handler(http.StatusOK, func(r *http.Request, _ none) (json.RawMessage, error) {
		return target(r).Indexes.LiveSettings(r.Context(), r.PathValue("uid"))
	}))
	h("POST /api/v1/indexes/{uid}/settings/preview", handler(http.StatusOK, func(r *http.Request, raw json.RawMessage) (map[string]any, error) {
		changes, err := target(r).Indexes.Diff(r.Context(), r.PathValue("uid"), raw)
		if err != nil {
			return nil, asSettingsErr(err)
		}
		if changes == nil {
			changes = []indexmgr.Change{}
		}
		return map[string]any{"changes": changes}, nil
	}))
	h("PATCH /api/v1/indexes/{uid}/settings", handler(http.StatusOK, func(r *http.Request, raw json.RawMessage) (taskRef, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		t, err := target(r).Indexes.ApplySettings(ctx, r.PathValue("uid"), raw)
		if t == nil {
			return taskRef{}, asSettingsErr(err)
		}
		out := taskRef{TaskUID: t.UID, Status: string(t.Status)}
		if err != nil {
			out.Error = err.Error()
		}
		return out, nil
	}))
	h("POST /api/v1/indexes/{uid}/settings/reset", handler(http.StatusAccepted, func(r *http.Request, _ none) (taskRef, error) {
		return ref(c(r).Index(r.PathValue("uid")).ResetSettings(r.Context()))
	}))
	h("PUT /api/v1/indexes/{uid}/primary-key", handler(http.StatusAccepted, func(r *http.Request, req struct {
		PrimaryKey string `json:"primaryKey"`
	}) (taskRef, error) {
		uid := r.PathValue("uid")
		if req.PrimaryKey == "" {
			return taskRef{}, badRequest("primaryKey is required")
		}
		n, err := target(r).Indexes.DocumentCount(r.Context(), uid)
		if err != nil {
			return taskRef{}, err
		}
		if n > 0 {
			return taskRef{}, &HTTPError{Code: http.StatusConflict, Msg: "the primary key of an index holding documents cannot change; resync into a new index instead"}
		}
		return ref(c(r).Index(uid).UpdateIndex(r.Context(), &meilisearch.UpdateIndexRequestParams{PrimaryKey: req.PrimaryKey}))
	}))
	h("POST /api/v1/indexes/{uid}/search", handler(http.StatusOK, func(r *http.Request, req searchReq) (*json.RawMessage, error) {
		sr := &meilisearch.SearchRequest{
			Filter: req.Filter, Sort: req.Sort, Limit: req.Limit, Offset: req.Offset, Facets: req.Facets,
			AttributesToHighlight: req.AttributesToHighlight, ShowRankingScore: req.ShowRankingScore,
			Hybrid: req.Hybrid, MatchingStrategy: meilisearch.MatchingStrategy(req.MatchingStrategy),
		}
		if sr.Limit == 0 {
			sr.Limit = 20
		}
		if req.Filter == "" {
			sr.Filter = nil
		}
		return c(r).Index(r.PathValue("uid")).SearchRaw(r.Context(), req.Q, sr)
	}))
	h("GET /api/v1/indexes/{uid}/documents", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		var res meilisearch.DocumentsResult
		offset, _ := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
		err := c(r).Index(r.PathValue("uid")).GetDocuments(r.Context(), &meilisearch.DocumentsQuery{
			Limit: int64(intParam(r, "limit", 20)), Offset: offset,
		}, &res)
		if err != nil {
			return nil, err
		}
		if res.Results == nil {
			res.Results = meilisearch.Hits{}
		}
		return map[string]any{"results": res.Results, "total": res.Total, "offset": res.Offset, "limit": res.Limit}, nil
	}))
	h("POST /api/v1/indexes/swap", handler(http.StatusAccepted, func(r *http.Request, req struct {
		A      string `json:"a"`
		B      string `json:"b"`
		Rename bool   `json:"rename"`
	}) (taskRef, error) {
		if req.A == "" || req.B == "" || req.A == req.B {
			return taskRef{}, badRequest("two different index uids are required")
		}
		if s.eng.IndexBusy(target(r).ID, req.A) || s.eng.IndexBusy(target(r).ID, req.B) {
			return taskRef{}, &HTTPError{Code: http.StatusConflict, Msg: "stop the tasks writing to these indexes before swapping"}
		}
		return ref(c(r).SwapIndexes(r.Context(), []*meilisearch.SwapIndexesParams{{Indexes: []string{req.A, req.B}, Rename: req.Rename}}))
	}))
	h("POST /api/v1/indexes/{uid}/compact", handler(http.StatusAccepted, func(r *http.Request, _ none) (taskRef, error) {
		return ref(c(r).Index(r.PathValue("uid")).Compact(r.Context()))
	}))
}

type searchReq struct {
	Q                     string                           `json:"q"`
	Filter                any                              `json:"filter"`
	Sort                  []string                         `json:"sort"`
	Limit                 int64                            `json:"limit"`
	Offset                int64                            `json:"offset"`
	Facets                []string                         `json:"facets"`
	AttributesToHighlight []string                         `json:"attributesToHighlight"`
	ShowRankingScore      bool                             `json:"showRankingScore"`
	MatchingStrategy      string                           `json:"matchingStrategy"`
	Hybrid                *meilisearch.SearchRequestHybrid `json:"hybrid"`
}

func asSettingsErr(err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "invalid settings") {
		return &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return err
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (s *Server) meiliRoutes(hh route) {
	h := func(pattern string, fn http.HandlerFunc) { hh(pattern, s.withTarget(fn)) }
	c := func(r *http.Request) meilisearch.ServiceManager { return target(r).Client }
	h("GET /api/v1/meili/tasks", handler(http.StatusOK, func(r *http.Request, _ none) (*meilisearch.TaskResult, error) {
		q := r.URL.Query()
		tq := &meilisearch.TasksQuery{Limit: int64(intParam(r, "limit", 50))}
		if v := q.Get("indexUid"); v != "" {
			tq.IndexUIDS = strings.Split(v, ",")
		}
		if v := q.Get("status"); v != "" {
			for _, st := range strings.Split(v, ",") {
				tq.Statuses = append(tq.Statuses, meilisearch.TaskStatus(st))
			}
		}
		if v := q.Get("type"); v != "" {
			for _, t := range strings.Split(v, ",") {
				tq.Types = append(tq.Types, meilisearch.TaskType(t))
			}
		}
		if v, err := strconv.ParseInt(q.Get("from"), 10, 64); err == nil {
			tq.From = v
		}
		return c(r).GetTasks(r.Context(), tq)
	}))
	h("POST /api/v1/meili/tasks/cancel", handler(http.StatusAccepted, func(r *http.Request, req struct {
		UIDs []int64 `json:"uids"`
	}) (taskRef, error) {
		if len(req.UIDs) == 0 {
			return taskRef{}, badRequest("uids is required")
		}
		return ref(c(r).CancelTasks(r.Context(), &meilisearch.CancelTasksQuery{UIDS: req.UIDs}))
	}))
	h("GET /api/v1/meili/stats", handler(http.StatusOK, func(r *http.Request, _ none) (*meilisearch.Stats, error) {
		return c(r).GetStats(r.Context(), nil)
	}))
	h("POST /api/v1/meili/dumps", handler(http.StatusAccepted, func(r *http.Request, _ none) (taskRef, error) {
		return ref(c(r).CreateDump(r.Context()))
	}))
	h("POST /api/v1/meili/snapshots", handler(http.StatusAccepted, func(r *http.Request, _ none) (taskRef, error) {
		return ref(c(r).CreateSnapshot(r.Context()))
	}))
	h("GET /api/v1/meili/experimental-features", handler(http.StatusOK, func(r *http.Request, _ none) (*meilisearch.ExperimentalFeaturesResult, error) {
		return c(r).ExperimentalFeatures().Get(r.Context())
	}))
	h("PATCH /api/v1/meili/experimental-features", handler(http.StatusOK, func(r *http.Request, req meilisearch.ExperimentalFeaturesBase) (*meilisearch.ExperimentalFeaturesResult, error) {
		f := c(r).ExperimentalFeatures()
		f.ExperimentalFeaturesBase = req
		return f.Update(r.Context())
	}))
}
