package websrv

import (
	"io"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
)

// handleListProviders backs GET /v1/providers — the catalog the SPA filters
// session-create and agent-create dropdowns on. The body shape is fixed by
// ADR 0020 §9; the data is sourced from secrets.EnabledProviders +
// config.toml's [model] and [pricing.tables.models] (single source of
// truth, no parallel catalog). When the providers service isn't wired
// (test rigs without agentd) we return an empty map rather than 503 so
// the SPA still renders.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	if s.providers == nil {
		writeJSON(w, http.StatusOK, ProvidersResponse{})
		return
	}
	writeJSON(w, http.StatusOK, s.providers.Catalog())
}

func (s *Server) handleGetUsage(w http.ResponseWriter, r *http.Request) {
	if s.usage == nil {
		unavailable(w, "GetUsage", "M5")
		return
	}
	q := r.URL.Query()
	body, err := s.usage.GetUsage(r.Context(), q.Get("since"), q.Get("session_id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleDoctor(w http.ResponseWriter, r *http.Request) {
	if s.doctor == nil {
		unavailable(w, "Doctor", "M5")
		return
	}
	body, err := s.doctor.Run(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.updater == nil {
		unavailable(w, "Update", "M4")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	resp, err := s.updater.Update(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, resp)
}
