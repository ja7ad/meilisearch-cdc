package api

import (
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"net/url"
	"strings"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/audit"
	"github.com/ja7ad/meilisearch-cdc/internal/auth"
)

const sessionCookie = "cdc_session"

// public routes need no authentication.
var public = map[string]bool{
	"GET /api/v1/health":              true,
	"GET /api/v1/auth/config":         true,
	"POST /api/v1/auth/setup":         true,
	"POST /api/v1/auth/login":         true,
	"POST /api/v1/auth/logout":        true,
	"POST /api/v1/auth/forgot":        true,
	"POST /api/v1/auth/reset":         true,
	"GET /api/v1/auth/invite/{token}": true,
	"POST /api/v1/auth/invite/accept": true,
}

// selfAuth routes check access in the handler: a webhook task with a
// secret accepts signed pushes without a token.
var selfAuth = map[string]bool{
	"POST /api/v1/ingest/{id}": true,
}

// permissions is the role required by each route. Routes not listed need
// viewer for GET and admin for anything else, so new mutating routes are
// locked down until someone decides otherwise.
var permissions = map[string]auth.Role{
	// Operating tasks.
	"POST /api/v1/tasks/test":                     auth.Moderator,
	"POST /api/v1/tasks/batch":                    auth.Moderator, // delete additionally needs admin
	"POST /api/v1/tasks/{id}/{action}":            auth.Moderator,
	"POST /api/v1/tasks/{id}/diff":                auth.Moderator,
	"POST /api/v1/tasks/{id}/diff/repair":         auth.Moderator,
	"POST /api/v1/tasks/{id}/dlq/replay":          auth.Moderator,
	"DELETE /api/v1/tasks/{id}/dlq":               auth.Moderator,
	"POST /api/v1/ingest/{id}":                    auth.Moderator,
	"POST /api/v1/meili/tasks/cancel":             auth.Moderator,
	"POST /api/v1/destinations/{id}/health/check": auth.Moderator,
	// Re-testing a saved connection changes nothing. Testing unsaved values
	// stays with admins, who are the ones editing connections.
	"POST /api/v1/connectors/{id}/test":           auth.Moderator,
	"POST /api/v1/destinations/{id}/test":         auth.Moderator,
	"POST /api/v1/indexes/{uid}/search":           auth.Viewer, // read only
	"POST /api/v1/indexes/{uid}/settings/preview": auth.Viewer,
	// Read endpoints reserved to admins.
	"GET /api/v1/tasks/export":      auth.Admin,
	"GET /api/v1/users":             auth.Admin,
	"GET /api/v1/invitations":       auth.Admin,
	"GET /api/v1/settings/smtp":     auth.Admin,
	"GET /api/v1/settings/telegram": auth.Admin,
	"GET /api/v1/settings/mcp":      auth.Admin,
	"GET /api/v1/mcp/tokens":        auth.Admin,
	"GET /api/v1/audit":             auth.Admin,
	"GET /debug/pprof":              auth.Admin, // profiles reveal internals and cost CPU
	// Self service for any signed in user.
	"GET /api/v1/auth/me":           auth.Viewer,
	"PATCH /api/v1/auth/me":         auth.Viewer,
	"POST /api/v1/auth/me/password": auth.Viewer,
}

// RequiredRole returns the role a route pattern needs.
func RequiredRole(pattern string) auth.Role {
	if r, ok := permissions[pattern]; ok {
		return r
	}
	if strings.HasPrefix(pattern, "GET ") {
		return auth.Viewer
	}
	return auth.Admin
}

// clientIP returns the caller address.
func (s *Server) clientIP(r *http.Request) string {
	if s.TrustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			return strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func (s *Server) secure(r *http.Request) bool {
	return s.CookieSecure || r.TLS != nil || (s.TrustProxy && r.Header.Get("X-Forwarded-Proto") == "https")
}

// principal resolves the caller from the bearer token or session cookie.
func (s *Server) principal(r *http.Request) *auth.Principal {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == r.Header.Get("Authorization") {
		token = ""
	}
	if token == "" && r.URL.Path == "/api/v1/events" {
		token = r.URL.Query().Get("token") // EventSource cannot set headers
	}
	if token != "" {
		if s.adminTokenHash != "" && subtle.ConstantTimeCompare([]byte(auth.TokenHash(token)), []byte(s.adminTokenHash)) == 1 {
			return &auth.Principal{ID: "api-token", Name: "API token", Role: auth.Admin, Kind: "token"}
		}
		return nil
	}
	if c, err := r.Cookie(sessionCookie); err == nil && s.Auth != nil {
		if p, err := s.Auth.ResolveSession(c.Value); err == nil {
			return p
		}
	}
	return nil
}

// authenticate attaches the principal (if any) to the request.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if p := s.principal(r); p != nil {
			r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		}
		next.ServeHTTP(w, r)
	})
}

func forbidden(role auth.Role) error {
	return &HTTPError{Code: http.StatusForbidden, Msg: fmt.Sprintf("requires role %s", role)}
}

// sameOrigin protects cookie authenticated writes against CSRF.
func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = r.Header.Get("Referer")
	}
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, r.Host)
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Flush keeps SSE working through the recorder.
func (w *statusRecorder) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// guard enforces authentication, the role of the route, CSRF protection
// and records successful writes in the audit log.
func (s *Server) guard(pattern string, fn http.HandlerFunc) http.HandlerFunc {
	isPublic := public[pattern]
	role := RequiredRole(pattern)
	return func(w http.ResponseWriter, r *http.Request) {
		p := auth.FromContext(r.Context())
		unsafe := r.Method != http.MethodGet && r.Method != http.MethodHead
		if !isPublic && (!selfAuth[pattern] || p != nil) {
			if p == nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
				return
			}
			if !p.Can(role) {
				writeErr(w, forbidden(role))
				return
			}
		}
		// Browsers attach cookies to cross site requests; tokens are not.
		if unsafe && (p == nil || p.Kind == "session") && r.Header.Get("Origin") != "" && !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
			return
		}
		if unsafe && p != nil && p.Kind == "session" && !sameOrigin(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-origin request refused"})
			return
		}
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		fn(rec, r)
		if unsafe && !isPublic && p != nil && rec.status < 400 && s.Audit != nil {
			s.Audit.Record(audit.Entry{At: time.Now().UTC(), UserID: p.ID, UserEmail: p.Label(), Action: pattern,
				Target: r.URL.Path, IP: s.clientIP(r)})
		}
	}
}

func (s *Server) setSession(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", HttpOnly: true, Secure: s.secure(r),
		SameSite: http.SameSiteLaxMode, MaxAge: int(auth.SessionTTL.Seconds())})
}

func (s *Server) clearSession(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: s.secure(r),
		SameSite: http.SameSiteLaxMode, MaxAge: -1})
}

// pprofRoutes exposes the Go profiler to admins (CDC_PPROF=true).
func (s *Server) pprofRoutes() {
	wrap := func(h http.HandlerFunc) http.Handler {
		return s.authenticate(s.guard("GET /debug/pprof", h))
	}
	s.mux.Handle("/debug/pprof/", wrap(pprof.Index))
	s.mux.Handle("/debug/pprof/cmdline", wrap(pprof.Cmdline))
	s.mux.Handle("/debug/pprof/profile", wrap(pprof.Profile))
	s.mux.Handle("/debug/pprof/symbol", wrap(pprof.Symbol))
	s.mux.Handle("/debug/pprof/trace", wrap(pprof.Trace))
}
