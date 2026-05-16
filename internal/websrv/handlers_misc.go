package websrv

import (
	"io"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
)

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

// handleListProviders returns the per-provider catalog the session-header
// model-switch dropdown reads (ADR 0020 §9 / §UX principles — "one source
// for the model catalog"). When no ProviderService is wired, we fall back
// to an empty map rather than 404'ing: the SPA treats "no catalog" as
// "model is display-only," which preserves the pre-Phase-4 UX in
// minimally-configured installs.
func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	if s.providers == nil {
		writeJSON(w, http.StatusOK, map[string]ProviderEntry{})
		return
	}
	out, err := s.providers.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	if out == nil {
		out = map[string]ProviderEntry{}
	}
	writeJSON(w, http.StatusOK, out)
}
