package api

import (
	"net/http"

	"github.com/ja7ad/meilisearch-cdc/internal/dest"
	"github.com/ja7ad/meilisearch-cdc/internal/engine"
	"github.com/ja7ad/meilisearch-cdc/internal/model"
)

func (s *Server) destinationRoutes(h route) {
	h("GET /api/v1/destinations", handler(http.StatusOK, func(r *http.Request, _ none) ([]engine.DestinationView, error) {
		return s.eng.DestinationViews(r.Context())
	}))
	h("GET /api/v1/destinations/{id}", handler(http.StatusOK, func(r *http.Request, _ none) (engine.DestinationView, error) {
		return s.eng.DestinationView(r.Context(), r.PathValue("id"))
	}))
	h("POST /api/v1/destinations", func(w http.ResponseWriter, r *http.Request) {
		d, ok := decode[model.Destination](w, r)
		if !ok {
			return
		}
		v, err := s.eng.CreateDestination(r.Context(), d)
		if err != nil {
			connectorErr(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, v)
	})
	h("PUT /api/v1/destinations/{id}", func(w http.ResponseWriter, r *http.Request) {
		d, ok := decode[model.Destination](w, r)
		if !ok {
			return
		}
		v, err := s.eng.UpdateDestination(r.Context(), r.PathValue("id"), d)
		if err != nil {
			connectorErr(w, err)
			return
		}
		writeJSON(w, http.StatusOK, v)
	})
	h("DELETE /api/v1/destinations/{id}", func(w http.ResponseWriter, r *http.Request) {
		if err := s.eng.DeleteDestination(r.PathValue("id")); err != nil {
			connectorErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	h("POST /api/v1/destinations/{id}/default", handler(http.StatusOK, func(r *http.Request, _ none) (engine.DestinationView, error) {
		if err := s.eng.SetDefaultDestination(r.PathValue("id")); err != nil {
			return engine.DestinationView{}, err
		}
		return s.eng.DestinationView(r.Context(), r.PathValue("id"))
	}))
	h("POST /api/v1/destinations/{id}/health/check", handler(http.StatusOK, func(r *http.Request, _ none) (dest.Health, error) {
		return s.eng.CheckDestination(r.Context(), r.PathValue("id"))
	}))
	h("POST /api/v1/destinations/test", handler(http.StatusOK, func(r *http.Request, d model.Destination) (dest.TestResult, error) {
		return s.eng.Destinations().Test(r.Context(), d), nil
	}))
	h("POST /api/v1/destinations/{id}/test", handler(http.StatusOK, func(r *http.Request, _ none) (dest.TestResult, error) {
		d, err := s.eng.Destinations().Get(r.PathValue("id"))
		if err != nil {
			return dest.TestResult{}, &HTTPError{Code: http.StatusNotFound, Msg: err.Error()}
		}
		d.APIKey = model.SecretMask // use the stored key
		return s.eng.Destinations().Test(r.Context(), d), nil
	}))
}
