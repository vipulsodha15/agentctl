package websrv

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/sm"
)

// taskStageSessionName matches the deterministic name tm.SessionRuntime gives
// each stage-backed session (see internal/tm/session_runtime.go). These are
// owned by tasks and surfaced through the Tasks UI, so the Sessions list
// excludes them.
var taskStageSessionName = regexp.MustCompile(`^task-task_[A-Z0-9]+-stage-\d+$`)

func (s *Server) requireManager(w http.ResponseWriter) bool {
	if s.manager == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "session manager unavailable")
		return false
	}
	return true
}

func (s *Server) handleListSessions(w http.ResponseWriter, r *http.Request) {
	if !s.requireManager(w) {
		return
	}
	list, err := s.manager.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	filtered := list[:0]
	for _, sum := range list {
		if taskStageSessionName.MatchString(sum.Name) {
			continue
		}
		filtered = append(filtered, sum)
	}
	list = filtered
	if s.usage != nil && len(list) > 0 {
		ids := make([]string, 0, len(list))
		for _, sum := range list {
			ids = append(ids, sum.ID)
		}
		if totals, terr := s.usage.RunningTotals(r.Context(), ids); terr == nil {
			for i, sum := range list {
				if v, ok := totals[sum.ID]; ok {
					c := v
					list[i].CostUSD = &c
				}
			}
		}
	}
	writeJSON(w, 0, proto.ListSessionsResponse{Sessions: list})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if !s.requireManager(w) {
		return
	}
	var req proto.CreateSessionRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.manager.Create(r.Context(), sm.CreateRequest{
		Name:          req.Name,
		MCPs:          req.MCPs,
		ExcludeMCPs:   req.ExcludeMCPs,
		Repos:         req.Repos,
		Model:         req.Model,
		MemLimitBytes: req.MemLimitBytes,
		CPULimitCores: req.CPULimitCores,
	})
	if err != nil {
		status, code := mapManagerErr(err)
		writeError(w, status, code, err.Error())
		return
	}
	resp := proto.CreateSessionResponse{
		SessionID: res.SessionID,
		Status:    res.Status,
		WebURL:    fmt.Sprintf("http://%s/sessions/%s", s.Addr(), res.SessionID),
		Attach:    proto.AttachPointer{StreamOp: proto.OpAttachStream},
		Session:   res.Summary,
	}
	writeJSON(w, http.StatusCreated, resp)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	d, err := s.manager.Get(r.Context(), id)
	if err != nil {
		status, code := mapManagerErr(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, 0, proto.GetSessionResponse{Session: d})
}

func (s *Server) handleTerminateSession(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	if err := s.manager.Terminate(r.Context(), id); err != nil {
		status, code := mapManagerErr(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, 0, proto.TerminateSessionResponse{SessionID: id, Status: "terminated"})
}

func (s *Server) handleSendMessage(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	var req proto.SendMessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	res, err := s.manager.Send(r.Context(), sm.SendRequest{
		SessionID:      id,
		Content:        req.Content,
		ClientID:       req.ClientID,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		status, code := mapManagerErr(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, 0, proto.SendMessageResponse{
		MessageID:  res.MessageID,
		Queued:     res.Queued,
		QueueDepth: res.QueueDepth,
		Idempotent: res.Idempotent,
	})
}

func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireManager(w) {
		return
	}
	var req proto.InterruptRequest
	_ = decodeJSON(r, &req)
	res, err := s.manager.Interrupt(r.Context(), id, req.ClearQueue)
	if err != nil {
		if errors.Is(err, sm.ErrNoInFlight) {
			writeError(w, http.StatusPreconditionFailed, proto.ErrPreconditionFailed, err.Error())
			return
		}
		status, code := mapManagerErr(err)
		writeError(w, status, code, err.Error())
		return
	}
	writeJSON(w, 0, proto.InterruptResponse{
		Interrupted:       res.Interrupted,
		ClearedQueueDepth: res.ClearedQueueDepth,
	})
}

func decodeJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return nil
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 16<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}
