// Package api serves the REST API, the live event stream and the dashboard.
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/meilisearch/meilisearch-go"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/ja7ad/meilisearch-cdc/internal/alerts"
	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/indexmgr"
	"github.com/ja7ad/meilisearch-cdc/internal/mcpserver"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
	"github.com/ja7ad/meilisearch-cdc/internal/notify"
	"github.com/ja7ad/meilisearch-cdc/internal/signature"
	"github.com/ja7ad/meilisearch-cdc/internal/source"
	"github.com/ja7ad/meilisearch-cdc/internal/source/webhook"
	"github.com/ja7ad/meilisearch-cdc/internal/store"
)

// Deps are the services the API serves.
type Deps struct {
	Engine *engine.Engine
	Auth   *auth.Service
	Notify *notify.Service
	Alerts *alerts.Manager
	Audit  *audit.Log
	// MCPService serves /mcp and its settings; nil disables MCP.
	MCPService *mcpserver.Service
	// AdminToken is a static bearer token with the admin role (machines).
	AdminToken string
	Web        fs.FS
	// WebBuild versions the service worker cache (a hash of Web).
	WebBuild string
	Log      *slog.Logger
	// CookieSecure forces the Secure cookie flag (behind TLS terminating proxies).
	CookieSecure bool
	// TrustProxy uses X-Forwarded-For / X-Forwarded-Proto.
	TrustProxy bool
	// Pprof exposes /debug/pprof for admins.
	Pprof bool
}

// Server is the HTTP API.
type Server struct {
	Deps
	adminTokenHash string // the admin token is only kept as a hash
	eng            *engine.Engine
	mux            *http.ServeMux
	log            *slog.Logger
	ipLimit        *auth.Limiter
	idLimit        *auth.Limiter
}

// New builds the server.
func New(d Deps) *Server {
	s := &Server{Deps: d, eng: d.Engine, log: d.Log, mux: http.NewServeMux(),
		ipLimit: auth.NewLimiter(30, 15*time.Minute), idLimit: auth.NewLimiter(10, 15*time.Minute)}
	if s.log == nil {
		s.log = slog.Default()
	}
	if s.AdminToken != "" {
		s.adminTokenHash = auth.TokenHash(s.AdminToken)
		s.AdminToken = ""
	}
	s.routes()
	return s
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.mux.ServeHTTP(w, r)
}

// HTTPError carries a status code.
type HTTPError struct {
	Code    int
	Msg     string
	Details []string
}

func (e *HTTPError) Error() string { return e.Msg }

func badRequest(format string, args ...any) error {
	return &HTTPError{Code: http.StatusBadRequest, Msg: fmt.Sprintf(format, args...)}
}

// handler adapts a typed function to http.HandlerFunc: the request body is
// decoded into Req and the result encoded as JSON.
func handler[Req, Resp any](status int, fn func(r *http.Request, req Req) (Resp, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req Req
		if r.Body != nil && r.ContentLength != 0 && (r.Method == http.MethodPost || r.Method == http.MethodPut || r.Method == http.MethodPatch || r.Method == http.MethodDelete) {
			body := http.MaxBytesReader(w, r.Body, 16<<20)
			dec := json.NewDecoder(body)
			if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeErr(w, badRequest("invalid JSON body: %v", err))
				return
			}
		}
		resp, err := fn(r, req)
		if err != nil {
			writeErr(w, err)
			return
		}
		writeJSON(w, status, resp)
	}
}

// decodeJSON reads a JSON body of at most 1 MiB; an empty body is allowed.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(v)
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

// none is used for handlers without a body or result.
type none struct{}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if status == http.StatusNoContent {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	code := http.StatusInternalServerError
	body := map[string]any{"error": err.Error()}
	var he *HTTPError
	var me *meilisearch.Error
	switch {
	case errors.As(err, &he):
		code = he.Code
		if len(he.Details) > 0 {
			body["details"] = he.Details
		}
	case errors.Is(err, engine.ErrNotFound), errors.Is(err, store.ErrNotFound), indexmgr.IsNotFound(err):
		code = http.StatusNotFound
	case errors.Is(err, engine.ErrConflict), errors.Is(err, indexmgr.ErrPrimaryKeyMismatch):
		code = http.StatusConflict
	case errors.As(err, &me) && me.StatusCode >= 400 && me.StatusCode < 500:
		code = me.StatusCode
		body["error"] = me.APIError.Message
		body["code"] = me.APIError.Code
	case isValidation(err):
		code = http.StatusUnprocessableEntity
		body["details"] = strings.Split(err.Error(), "\n")
	}
	writeJSON(w, code, body)
}

// validationError marks model validation failures.
type validationError struct{ error }

func (v validationError) Unwrap() error { return v.error }

func isValidation(err error) bool {
	var v validationError
	return errors.As(err, &v)
}

func (s *Server) routes() {
	api := http.NewServeMux()
	h := func(pattern string, fn http.HandlerFunc) { api.Handle(pattern, s.guard(pattern, fn)) }

	h("GET /api/v1/health", handler(http.StatusOK, s.health))
	h("GET /api/v1/overview", handler(http.StatusOK, func(r *http.Request, _ none) (engine.Overview, error) {
		return s.eng.Overview(r.Context(), source.Registered()), nil
	}))

	// Tasks.
	h("GET /api/v1/tasks", handler(http.StatusOK, func(r *http.Request, _ none) ([]model.TaskView, error) {
		q := r.URL.Query()
		return s.eng.List(q.Get("state"), q.Get("q"), q.Get("tag"))
	}))
	h("POST /api/v1/tasks", handler(http.StatusCreated, func(r *http.Request, t model.Task) (model.TaskView, error) {
		v, err := s.eng.Create(t, r.URL.Query().Get("start") == "true")
		return v, asValidation(err)
	}))
	h("POST /api/v1/tasks/test", handler(http.StatusOK, func(r *http.Request, t model.Task) (engine.TestResult, error) {
		return s.eng.TestConnection(r.Context(), t), nil
	}))
	h("GET /api/v1/tasks/export", handler(http.StatusOK, func(_ *http.Request, _ none) ([]model.Task, error) {
		return s.eng.Export()
	}))
	h("POST /api/v1/tasks/import", handler(http.StatusOK, func(_ *http.Request, ts []model.Task) (engine.ImportResult, error) {
		return s.eng.Import(ts), nil
	}))
	h("POST /api/v1/tasks/batch", handler(http.StatusOK, s.batch))
	h("GET /api/v1/tasks/{id}", handler(http.StatusOK, func(r *http.Request, _ none) (model.TaskView, error) {
		return s.eng.View(r.PathValue("id"))
	}))
	h("PUT /api/v1/tasks/{id}", handler(http.StatusOK, func(r *http.Request, t model.Task) (model.TaskView, error) {
		v, err := s.eng.Update(r.Context(), r.PathValue("id"), t)
		return v, asValidation(err)
	}))
	h("DELETE /api/v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if err := s.eng.Delete(r.Context(), r.PathValue("id"), q.Get("dropSource") == "true", q.Get("deleteIndex")); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h("POST /api/v1/tasks/{id}/{action}", handler(http.StatusOK, func(r *http.Request, _ none) (model.TaskView, error) {
		switch a := r.PathValue("action"); a {
		case "start", "stop", "pause", "resume", "restart", "resync", "reset-checkpoint":
			return s.eng.Action(r.Context(), r.PathValue("id"), a)
		}
		return model.TaskView{}, &HTTPError{Code: http.StatusNotFound, Msg: "unknown action"}
	}))
	h("GET /api/v1/tasks/{id}/logs", handler(http.StatusOK, func(r *http.Request, _ none) ([]model.LogEntry, error) {
		if _, err := s.eng.Get(r.PathValue("id")); err != nil {
			return nil, err
		}
		return s.eng.Logs(r.PathValue("id"), intParam(r, "limit", 200)), nil
	}))
	h("GET /api/v1/tasks/{id}/metrics", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		if _, err := s.eng.Get(r.PathValue("id")); err != nil {
			return nil, err
		}
		cp, _ := s.eng.Checkpoint(r.PathValue("id"))
		return map[string]any{"points": s.eng.Series(r.PathValue("id")), "checkpoint": cp}, nil
	}))
	h("POST /api/v1/ingest/{id}", s.ingest)
	s.qualityRoutes(h)
	s.indexRoutes(h)
	s.meiliRoutes(h)
	s.accountRoutes(h, api)
	s.alertRoutes(h)
	s.mcpRoutes(h)
	s.connectorRoutes(h)
	s.destinationRoutes(h)
	h("GET /api/v1/events", s.events)

	s.mux.Handle("/api/", s.authenticate(api))
	s.mux.Handle("/metrics", s.authenticate(s.guard("GET /metrics", promhttp.HandlerFor(s.eng.Metrics().Registry, promhttp.HandlerOpts{}).ServeHTTP)))
	if s.MCPService != nil {
		s.mux.Handle("/mcp", s.MCPService.Handler())
	}
	if s.Pprof {
		s.pprofRoutes()
	}
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	if s.Web != nil {
		files := http.FileServerFS(s.Web)
		s.mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			switch r.URL.Path {
			case "/", "/index.html":
				w.Header().Set("Cache-Control", "no-cache")
				w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; connect-src 'self'; worker-src 'self'; manifest-src 'self'")
			case "/sw.js":
				s.serviceWorker(w, r)
				return
			case "/manifest.webmanifest":
				w.Header().Set("Content-Type", "application/manifest+json")
				w.Header().Set("Cache-Control", "no-cache")
			}
			if strings.HasPrefix(r.URL.Path, "/assets/") && s.WebBuild != "" {
				// Embedded files carry no modification time, so the build hash is
				// the validator: browsers revalidate and get a cheap 304 until the
				// next release. Not "immutable": the file names are not versioned.
				etag := `"` + s.WebBuild + `"`
				w.Header().Set("ETag", etag)
				w.Header().Set("Cache-Control", "no-cache")
				if r.Header.Get("If-None-Match") == etag {
					w.WriteHeader(http.StatusNotModified)
					return
				}
			}
			files.ServeHTTP(w, r)
		}))
	}
}

// serviceWorker serves sw.js with the build hash filled in. It is never
// cached by the browser's HTTP cache, so updates are picked up at once.
func (s *Server) serviceWorker(w http.ResponseWriter, r *http.Request) {
	src, err := fs.ReadFile(s.Web, "sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Service-Worker-Allowed", "/")
	_, _ = w.Write(bytes.Replace(src, []byte("__BUILD__"), []byte(s.WebBuild), 1))
}

// ingest accepts changes for a webhook task and answers once Meilisearch
// confirmed them, so a 200 means the documents are durable and searchable.
//
// A task with a webhook secret only accepts pushes signed with it (and then
// needs no token); a task without one needs a moderator token or session.
func (s *Server) ingest(w http.ResponseWriter, r *http.Request) {
	t, err := s.eng.Get(r.PathValue("id"))
	signed := err == nil && t.Source.Type == model.SourceWebhook && t.Source.WebhookSecret != ""
	if !signed && auth.FromContext(r.Context()) == nil {
		// Anonymous callers learn nothing about other tasks.
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
		return
	}
	if err != nil {
		writeErr(w, err)
		return
	}
	if t.Source.Type != model.SourceWebhook {
		writeErr(w, badRequest("task %q is not a webhook task", t.ID))
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeErr(w, badRequest("read the body: %v", err))
		return
	}
	if signed {
		plain, err := s.eng.Reveal(t)
		if err != nil {
			writeErr(w, err)
			return
		}
		if err := signature.Verify(r.Header, plain.Source.WebhookSecret, time.Now(), body); err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}
	} else if !auth.FromContext(r.Context()).Can(auth.Moderator) {
		writeErr(w, forbidden(auth.Moderator))
		return
	}
	events, err := webhook.ParsePayload(bytes.NewReader(body))
	if err != nil {
		writeErr(w, badRequest("%v", err))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	pos, err := webhook.Push(ctx, t.ID, events)
	switch {
	case errors.Is(err, webhook.ErrNotRunning):
		writeErr(w, &HTTPError{Code: http.StatusServiceUnavailable, Msg: "the task is not running; start it and retry"})
	case errors.Is(err, context.DeadlineExceeded):
		writeErr(w, &HTTPError{Code: http.StatusGatewayTimeout, Msg: "changes were not confirmed in time; retry (writes are idempotent)"})
	case err != nil:
		writeErr(w, err)
	default:
		writeJSON(w, http.StatusOK, map[string]any{"accepted": len(events), "position": pos})
	}
}

func asValidation(err error) error {
	if err == nil || errors.Is(err, engine.ErrConflict) || errors.Is(err, engine.ErrNotFound) {
		return err
	}
	return validationError{err}
}

func intParam(r *http.Request, name string, def int) int {
	if v, err := strconv.Atoi(r.URL.Query().Get(name)); err == nil && v > 0 {
		return v
	}
	return def
}

func (s *Server) health(r *http.Request, _ none) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	// The health of the default destination, for load balancers and the UI.
	meili := map[string]any{"status": "unavailable"}
	if id, err := s.eng.Destinations().DefaultID(); err != nil {
		meili["status"], meili["error"] = "unconfigured", err.Error()
	} else if h := s.eng.Destinations().Health(ctx, id); h.Healthy {
		meili["status"], meili["version"], meili["destination"] = "available", h.Version, id
	} else {
		meili["error"], meili["destination"] = h.Error, id
	}
	return map[string]any{"status": "ok", "meilisearch": meili}, nil
}

type batchReq struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"`
}

func (s *Server) batch(r *http.Request, req batchReq) (map[string]any, error) {
	switch req.Action {
	case "start", "stop", "pause", "resume", "restart", "resync", "delete":
	default:
		return nil, badRequest("unknown action %q", req.Action)
	}
	if len(req.IDs) == 0 {
		return nil, badRequest("ids is required")
	}
	if req.Action == "delete" && !auth.FromContext(r.Context()).Can(auth.Admin) {
		return nil, forbidden(auth.Admin)
	}
	return map[string]any{"results": s.eng.Batch(r.Context(), req.IDs, req.Action)}, nil
}

// events streams status, log, task and overview events (SSE).
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	st := s.eng.StatusBus.Subscribe(256)
	defer st.Close()
	lg := s.eng.LogBus.Subscribe(256)
	defer lg.Close()
	tk := s.eng.TaskBus.Subscribe(64)
	defer tk.Close()
	dh := s.eng.HealthBus.Subscribe(64)
	defer dh.Close()
	var alertC <-chan alerts.Event
	if s.Alerts != nil {
		al := s.Alerts.Bus.Subscribe(64)
		defer al.Close()
		alertC = al.C
	}

	send := func(event string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b); err != nil {
			return false
		}
		fl.Flush()
		return true
	}
	_, _ = fmt.Fprint(w, "retry: 3000\n\n")
	send("overview", s.eng.Overview(r.Context(), source.Registered()))
	if views, err := s.eng.List("", "", ""); err == nil {
		for _, v := range views {
			send("status", v.Status)
		}
	}

	// Throttle status events to ~2/s per task.
	lastSent := map[string]time.Time{}
	pending := map[string]model.Status{}
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	overview := time.NewTicker(2 * time.Second)
	defer overview.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case v := <-st.C:
			if time.Since(lastSent[v.TaskID]) >= 500*time.Millisecond {
				lastSent[v.TaskID] = time.Now()
				delete(pending, v.TaskID)
				if !send("status", v) {
					return
				}
			} else {
				pending[v.TaskID] = v
			}
		case <-tick.C:
			for id, v := range pending {
				lastSent[id] = time.Now()
				if !send("status", v) {
					return
				}
			}
			clear(pending)
		case v := <-lg.C:
			if !send("log", v) {
				return
			}
		case v := <-tk.C:
			if !send("task", v) {
				return
			}
		case v := <-dh.C:
			if !send("destination.health", v) {
				return
			}
		case v := <-alertC:
			if !send("alert", v) {
				return
			}
		case <-overview.C:
			if !send("overview", s.eng.Overview(r.Context(), source.Registered())) {
				return
			}
		}
	}
}
