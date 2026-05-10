package websrv

import (
	"errors"
	"io"
	"net/http"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	q := r.URL.Query()
	repo := q.Get("repo")
	format := q.Get("format")
	if format == "" {
		format = "unified"
	}
	stream, err := s.manager.Diff(r.Context(), id, sm.DiffRequest{Repo: repo, Format: format})
	if err != nil {
		writeDiffError(w, err)
		return
	}
	streamPatch(w, stream)
}

func (s *Server) handleExportPatch(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	var req proto.ExportPatchRequest
	_ = decodeJSON(r, &req)
	stream, err := s.manager.ExportPatch(r.Context(), id, sm.DiffRequest{Repo: req.Repo, Format: "unified"})
	if err != nil {
		writeDiffError(w, err)
		return
	}
	filename := req.Repo
	if filename == "" {
		filename = "session"
	}
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+".patch\"")
	streamPatch(w, stream)
}

func (s *Server) handleExportPush(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	var req proto.ExportPushRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	if req.Branch == "" {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "branch required")
		return
	}
	res, err := s.manager.ExportPush(r.Context(), id, sm.PushRequest{
		Repo: req.Repo, Branch: req.Branch, Message: req.Message,
	})
	if err != nil {
		writeDiffError(w, err)
		return
	}
	writeJSON(w, 0, proto.ExportPushResponse{
		Success: res.Success, Repo: res.Repo, Branch: res.Branch,
		Output: res.Output, Error: res.Error,
	})
}

func streamPatch(w http.ResponseWriter, stream sm.DiffStream) {
	defer func() { _ = stream.Close() }()
	w.Header().Set("Content-Type", "application/octet-stream")
	flusher, _ := w.(http.Flusher)
	for {
		ch, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			return
		}
		if ch.End {
			if flusher != nil {
				flusher.Flush()
			}
			continue
		}
		if len(ch.Data) > 0 {
			_, _ = w.Write(ch.Data)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (s *Server) handleListSessionRepos(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	repos, err := s.manager.SessionRepos(r.Context(), id)
	if err != nil {
		writeDiffError(w, err)
		return
	}
	writeJSON(w, 0, proto.ListSessionReposResponse{Repos: repos})
}

func writeDiffError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sm.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
	case errors.Is(err, sm.ErrDiffUnavailable):
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
	}
}
