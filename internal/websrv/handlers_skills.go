package websrv

import (
	"io"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
)

func (s *Server) requireSkills(w http.ResponseWriter) bool {
	if s.skills == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "skills service not yet wired")
		return false
	}
	return true
}

func (s *Server) handleListInstalledSkills(w http.ResponseWriter, r *http.Request) {
	if !s.requireSkills(w) {
		return
	}
	body, err := s.skills.ListInstalled(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleSessionSkills(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireSkills(w) {
		return
	}
	body, err := s.skills.ListForSession(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, body)
}

func (s *Server) handleAddSkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireSkills(w) {
		return
	}
	contentType := r.Header.Get("Content-Type")
	body, err := s.skills.Add(r.Context(), contentType, http.MaxBytesReader(w, r.Body, 64<<20))
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusCreated, body)
}

func (s *Server) handleImportSkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireSkills(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	resp, err := s.skills.Import(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, resp)
}

func (s *Server) handleValidateSkill(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireSkills(w) {
		return
	}
	resp, err := s.skills.Validate(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeRawJSON(w, http.StatusOK, resp)
}

func (s *Server) handleExportSkill(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireSkills(w) {
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.tar"`)
	if err := s.skills.Export(r.Context(), name, w); err != nil {
		// Headers may already be flushed; best-effort log only.
		s.logger.Warn("web.export_skill_failed", "name", name, "error", err.Error())
	}
}

func (s *Server) handleRemoveSkill(w http.ResponseWriter, r *http.Request, name string) {
	if !s.requireSkills(w) {
		return
	}
	force := r.URL.Query().Get("force") == "1" || r.URL.Query().Get("force") == "true"
	if err := s.skills.Remove(r.Context(), name, force); err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
