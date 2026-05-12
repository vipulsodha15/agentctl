package websrv

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/tm"
	"github.com/agentctl/agentctl/internal/ttl"
)

// TaskService is the websrv handle on tm.Manager.
type TaskService interface {
	ListTasks(ctx context.Context) ([]tm.Task, error)
	GetTask(ctx context.Context, id string) (*tm.Task, error)
	TaskMessages(ctx context.Context, id string) ([]tm.Message, error)
	CreateTask(ctx context.Context, req tm.CreateTaskRequest) (*tm.Task, error)
	AttachWorkflow(ctx context.Context, id, workflow string) (*tm.Task, error)
	Send(ctx context.Context, req tm.SendMessageRequest) error
	Handoff(ctx context.Context, id string) error
	Complete(ctx context.Context, id string) error
	Abandon(ctx context.Context, id string) error
}

// LibraryService exposes agent+workflow CRUD to websrv.
type LibraryService interface {
	ListAgents() []ttl.Agent
	GetAgent(name string) (ttl.Agent, error)
	PutAgent(spec ttl.Agent, body []byte) (ttl.Agent, error)
	RemoveAgent(name string) error
	ListWorkflows() []ttl.Workflow
	GetWorkflow(name string) (ttl.Workflow, error)
	PutWorkflow(spec ttl.Workflow, body []byte) (ttl.Workflow, error)
	RemoveWorkflow(name string) error
}

// ----- handlers -----

func (s *Server) handleListAgents(w http.ResponseWriter, r *http.Request) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.library.ListAgents()})
}

func (s *Server) handleGetAgent(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	a, err := s.library.GetAgent(name)
	if err != nil {
		writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleAddAgent(w http.ResponseWriter, r *http.Request) {
	s.handlePutAgent(w, r, "")
}

func (s *Server) handlePutAgent(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var spec ttl.Agent
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.Unmarshal(body, &spec); err != nil {
			writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "invalid JSON: "+err.Error())
			return
		}
		body = nil // tell library to use the parsed spec
	}
	if name != "" {
		spec.Name = name
	}
	saved, err := s.library.PutAgent(spec, body)
	if err != nil {
		if errors.Is(err, ttl.ErrValidation) {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleRemoveAgent(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	if err := s.library.RemoveAgent(name); err != nil {
		switch {
		case errors.Is(err, ttl.ErrNotFound):
			writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
		case errors.Is(err, ttl.ErrBuiltinReadOnly):
			writeError(w, http.StatusBadRequest, "builtin_readonly", err.Error())
		case errors.Is(err, ttl.ErrInUse):
			writeError(w, http.StatusBadRequest, "agent_referenced", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleListWorkflows(w http.ResponseWriter, r *http.Request) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workflows": s.library.ListWorkflows()})
}

func (s *Server) handleGetWorkflow(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	wf, err := s.library.GetWorkflow(name)
	if err != nil {
		writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) handleAddWorkflow(w http.ResponseWriter, r *http.Request) {
	s.handlePutWorkflow(w, r, "")
}

func (s *Server) handlePutWorkflow(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var spec ttl.Workflow
	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "application/json") {
		if err := json.Unmarshal(body, &spec); err != nil {
			writeError(w, http.StatusBadRequest, proto.ErrBadRequest, "invalid JSON: "+err.Error())
			return
		}
		body = nil
	}
	if name != "" {
		spec.Name = name
	}
	saved, err := s.library.PutWorkflow(spec, body)
	if err != nil {
		if errors.Is(err, ttl.ErrValidation) {
			writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) handleRemoveWorkflow(w http.ResponseWriter, r *http.Request, name string) {
	if s.library == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task library not wired")
		return
	}
	if err := s.library.RemoveWorkflow(name); err != nil {
		switch {
		case errors.Is(err, ttl.ErrNotFound):
			writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
		case errors.Is(err, ttl.ErrBuiltinReadOnly):
			writeError(w, http.StatusBadRequest, "builtin_readonly", err.Error())
		default:
			writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ----- tasks -----

func (s *Server) requireTasks(w http.ResponseWriter) bool {
	if s.tasks == nil {
		writeError(w, http.StatusServiceUnavailable, proto.ErrUnavailable, "task service not wired")
		return false
	}
	return true
}

func (s *Server) handleListTasks(w http.ResponseWriter, r *http.Request) {
	if !s.requireTasks(w) {
		return
	}
	tasks, err := s.tasks.ListTasks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks})
}

func (s *Server) handleGetTask(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	task, err := s.tasks.GetTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, tm.ErrTaskNotFound) {
			writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
		return
	}
	msgs, _ := s.tasks.TaskMessages(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"task": task, "messages": msgs})
}

func (s *Server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if !s.requireTasks(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var req tm.CreateTaskRequest
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	task, err := s.tasks.CreateTask(r.Context(), req)
	if err != nil {
		mapTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, task)
}

func (s *Server) handleAttachWorkflow(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var req struct {
		Workflow string `json:"workflow"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	task, err := s.tasks.AttachWorkflow(r.Context(), id, req.Workflow)
	if err != nil {
		mapTaskError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleTaskSend(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	var req struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeError(w, http.StatusBadRequest, proto.ErrBadRequest, err.Error())
		return
	}
	if err := s.tasks.Send(r.Context(), tm.SendMessageRequest{TaskID: id, Content: req.Content}); err != nil {
		mapTaskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTaskHandoff(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	if err := s.tasks.Handoff(r.Context(), id); err != nil {
		mapTaskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTaskComplete(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	if err := s.tasks.Complete(r.Context(), id); err != nil {
		mapTaskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTaskAbandon(w http.ResponseWriter, r *http.Request, id string) {
	if !s.requireTasks(w) {
		return
	}
	if err := s.tasks.Abandon(r.Context(), id); err != nil {
		mapTaskError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func mapTaskError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tm.ErrTaskNotFound):
		writeError(w, http.StatusNotFound, proto.ErrNotFound, err.Error())
	case errors.Is(err, tm.ErrWorkflowNotFound):
		writeError(w, http.StatusBadRequest, "workflow_not_found", err.Error())
	case errors.Is(err, tm.ErrAgentNotFound):
		writeError(w, http.StatusBadRequest, "agent_not_found", err.Error())
	case errors.Is(err, tm.ErrValidation):
		writeError(w, http.StatusBadRequest, "validation_failed", err.Error())
	case errors.Is(err, tm.ErrPreconditionFailed):
		writeError(w, http.StatusPreconditionFailed, proto.ErrPreconditionFailed, err.Error())
	case errors.Is(err, tm.ErrTerminal):
		writeError(w, http.StatusPreconditionFailed, "terminal", err.Error())
	case errors.Is(err, tm.ErrStageBusy):
		writeError(w, http.StatusPreconditionFailed, "stage_busy", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, proto.ErrInternal, err.Error())
	}
}
