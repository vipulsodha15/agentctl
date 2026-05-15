package websrv

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/agentctl/agentctl/internal/proto"
)

func (s *Server) requireSecrets(w http.ResponseWriter) bool {
	if s.secrets == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "secrets service not wired")
		return false
	}
	return true
}

func (s *Server) handleGetGitHubToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecrets(w) {
		return
	}
	info, err := s.secrets.GetGitHub(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}

type updateGitHubTokenRequest struct {
	Token        string `json:"token"`
	SkipValidate bool   `json:"skip_validate,omitempty"`
}

func (s *Server) handleUpdateGitHubToken(w http.ResponseWriter, r *http.Request) {
	if !s.requireSecrets(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var req updateGitHubTokenRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "invalid JSON body")
		return
	}
	token := strings.TrimSpace(req.Token)
	if token == "" {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "token is required")
		return
	}
	info, err := s.secrets.UpdateGitHub(r.Context(), token, !req.SkipValidate)
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, info)
}
