package api

import (
	"errors"
	"net/http"

	"github.com/ja7ad/meilisearch-cdc/internal/discover"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func connectorErr(w http.ResponseWriter, err error) {
	var inUse *engine.ErrInUse
	if errors.As(err, &inUse) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "usedBy": inUse.UsedBy})
		return
	}
	writeErr(w, asValidation(err))
}

func (s *Server) connectorRoutes(h route) {
	h("GET /api/v1/connectors", handler(http.StatusOK, func(r *http.Request, _ none) ([]engine.ConnectorView, error) {
		return s.eng.Connectors()
	}))
	h("GET /api/v1/connectors/{id}", handler(http.StatusOK, func(r *http.Request, _ none) (engine.ConnectorView, error) {
		return s.eng.Connector(r.PathValue("id"))
	}))
	h("POST /api/v1/connectors", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode[model.Connector](w, r)
		if !ok {
			return
		}
		v, err := s.eng.CreateConnector(c)
		if err != nil {
			connectorErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})
	h("PUT /api/v1/connectors/{id}", func(w http.ResponseWriter, r *http.Request) {
		c, ok := decode[model.Connector](w, r)
		if !ok {
			return
		}
		v, err := s.eng.UpdateConnector(r.Context(), r.PathValue("id"), c)
		if err != nil {
			connectorErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	h("DELETE /api/v1/connectors/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.eng.DeleteConnector(r.PathValue("id")); err != nil {
			connectorErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	// Testing an unsaved connector; with an id, masked secrets are taken
	// from the stored connector so editing does not require retyping them.
	h("POST /api/v1/connectors/test", handler(http.StatusOK, func(r *http.Request, c model.Connector) (discover.Result, error) {
		if c.ID != "" {
			if prev, err := s.eng.RevealConnector(c.ID); err == nil {
				c.RestoreSecrets(prev)
			}
		}
		c.Config.Type = c.Type
		// Unsaved values: not recorded as the connector's status.
		return discover.Run(r.Context(), c.Config), nil
	}))
	h("POST /api/v1/connectors/{id}/test", handler(http.StatusOK, func(r *http.Request, _ none) (discover.Result, error) {
		return s.eng.TestConnector(r.Context(), r.PathValue("id"))
	}))
	h("GET /api/v1/connectors/{id}/tables", handler(http.StatusOK, func(r *http.Request, _ none) ([]discover.Table, error) {
		c, err := s.eng.RevealConnector(r.PathValue("id"))
		if err != nil {
			return nil, err
		}
		c.Config.Type = c.Type
		res := discover.Run(r.Context(), c.Config)
		if !res.OK {
			return nil, &HTTPError{Code: http.StatusBadGateway, Msg: res.Error}
		}
		return discover.Filter(res.Tables, r.URL.Query().Get("q")), nil
	}))
}
