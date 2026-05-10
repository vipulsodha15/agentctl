package websrv

import (
	"io"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
)

func (s *Server) requireMCPs(w http.ResponseWriter) bool {
	if s.mcps == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "MCP registry not yet wired")
		return false
	}
	return true
}

func (s *Server) handleListMCPs(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPs(w) {
		return
	}
	body, err := s.mcps.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleAddMCP(w http.ResponseWriter, r *http.Request) {
	if !s.requireMCPs(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	resp, err := s.mcps.Add(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleUpdateMCP(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireMCPs(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	resp, err := s.mcps.Update(r.Context(), name, body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRemoveMCP(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireMCPs(w) {
		return
	}
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if err := s.mcps.Remove(r.Context(), name, force); err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeRawJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
