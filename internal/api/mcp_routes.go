package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/ja7ad/meilisearch-cdc/internal/auth"
	"github.com/ja7ad/meilisearch-cdc/internal/mcpserver"
)

func mcpErr(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, mcpserver.ErrNotFound):
		return &HTTPError{Code: http.StatusNotFound, Msg: err.Error()}
	case errors.Is(err, mcpserver.ErrInvalid):
		return &HTTPError{Code: http.StatusUnprocessableEntity, Msg: err.Error()}
	}
	return err
}

type tokenResp struct {
	Token string          `json:"token"`
	Info  mcpserver.Token `json:"info"`
}

func (s *Server) mcpRoutes(h route) {
	if s.MCPService == nil {
		return
	}
	m := s.MCPService
	view := func(c mcpserver.Config) map[string]any {
		return map[string]any{"enabled": c.Enabled, "endpoint": "/mcp", "policy": c.Policy, "tools": m.Tools(), "resources": m.Resources()}
	}
	h("GET /api/v1/settings/mcp", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		return view(m.Config()), nil
	}))
	h("PUT /api/v1/settings/mcp", handler(http.StatusOK, func(r *http.Request, c mcpserver.Config) (map[string]any, error) {
		saved, err := m.SetConfig(c)
		return view(saved), mcpErr(err)
	}))
	h("POST /api/v1/settings/mcp/reset-policy", handler(http.StatusOK, func(r *http.Request, _ none) (map[string]any, error) {
		c := m.Config()
		c.Policy = mcpserver.DefaultPolicy()
		saved, err := m.SetConfig(c)
		return view(saved), mcpErr(err)
	}))
	h("GET /api/v1/mcp/tokens", handler(http.StatusOK, func(r *http.Request, _ none) ([]mcpserver.Token, error) {
		return m.Tokens()
	}))
	h("POST /api/v1/mcp/tokens", handler(http.StatusCreated, func(r *http.Request, req struct {
		Name      string    `json:"name"`
		Role      auth.Role `json:"role"`
		ExpiresIn string    `json:"expiresIn"`
	}) (tokenResp, error) {
		var ttl time.Duration
		if req.ExpiresIn != "" {
			d, err := time.ParseDuration(req.ExpiresIn)
			if err != nil {
				return tokenResp{}, badRequest("expiresIn: %v", err)
			}
			ttl = d
		}
		raw, t, err := m.CreateToken(req.Name, req.Role, ttl, auth.FromContext(r.Context()).Label())
		return tokenResp{Token: raw, Info: t}, mcpErr(err)
	}))
	h("POST /api/v1/mcp/tokens/{id}/rotate", handler(http.StatusOK, func(r *http.Request, _ none) (tokenResp, error) {
		raw, t, err := m.RotateToken(r.PathValue("id"), auth.FromContext(r.Context()).Label())
		return tokenResp{Token: raw, Info: t}, mcpErr(err)
	}))
	h("DELETE /api/v1/mcp/tokens/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := m.RevokeToken(r.PathValue("id")); err != nil {
			writeErr(w, mcpErr(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
