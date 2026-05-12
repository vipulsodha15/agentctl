package tm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/ttl"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

// HandoffAutoPrompt is the verbatim text injected as a user message when the
// user clicks "Hand off". See workflows-task-management-architecture.md §7.1.
const HandoffAutoPrompt = `Produce your synthesis for the next stage now using this structure:

## Summary
What you did and what you found, in 2–4 sentences.

## Key evidence
Concrete pointers — file:line refs, log excerpts, repro steps, links. Be specific.

## Recommendation for the next stage
What the next agent should do with what you've handed over.

## Open questions
Anything you could not resolve.`

// StageRuntime is the abstraction the task manager talks to for running a
// stage. In production this drives a real session via sm.Manager; in offline
// or test environments a simulator implementation produces canned replies so
// the UI/CLI can be exercised end-to-end without Docker. See SimRuntime below.
type StageRuntime interface {
	// StartStage prepares the agent's session and returns once the agent is
	// ready for messages. The returned sessionID is recorded on the stage row.
	StartStage(ctx context.Context, in StartStageInput) (StartStageResult, error)
	// SendUserMessage delivers a user message to the active stage's agent.
	SendUserMessage(ctx context.Context, in SendMessageInput) error
	// Synthesize delivers the handoff auto-prompt and *synchronously* returns
	// the agent's response, locking the assistant reply that corresponds to
	// the auto-prompt rather than any earlier in-flight turn.
	Synthesize(ctx context.Context, in SendMessageInput) (string, error)
	// IsBusy reports whether a turn is currently in flight for this stage.
	IsBusy(stageID string) bool
	// StopStage tears down the stage's session.
	StopStage(ctx context.Context, sessionID string) error
}

type StartStageInput struct {
	TaskID         string
	StageID        string
	Position       int
	Agent          ttl.Agent
	PrevAgent      string
	NextAgent      string
	IssueMD        string
	HandoffInMD    string
	RepoURL        string
	BaseSHA        string
	// OnAssistantMessage is invoked with each assistant message the agent
	// emits while the stage is active. The manager uses these to populate
	// the task chat thread and to lock the synthesis on handoff.
	OnAssistantMessage func(content string)
	// OnToolUse is invoked with each tool call (optional; SimRuntime ignores).
	OnToolUse func(tool string, input json.RawMessage)
	// OnError is invoked when the runtime surfaces an inline error.
	OnError func(message string)
}

type StartStageResult struct {
	SessionID  string
	VolumeName string
}

type SendMessageInput struct {
	TaskID    string
	StageID   string
	SessionID string
	Content   string
}

// Hub matches a small subset of fan.Hub for broadcasting task events.
type Hub interface {
	Broadcast(channel string, ev proto.Event)
	Subscribe(channel string) (fan.Stream, func(), error)
	Close(channel string, reason string)
}

// Options for the Manager.
type Options struct {
	Store   *store.Store
	Library *ttl.Library
	Runtime StageRuntime
	Hub     Hub
	Logger  *slog.Logger
	Now     func() time.Time
}

type Manager struct {
	opts   Options
	logger *slog.Logger
	now    func() time.Time
	mu     sync.Mutex
}

func New(opts Options) *Manager {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Hub == nil {
		opts.Hub = fan.NewHub()
	}
	if opts.Runtime == nil {
		opts.Runtime = NewSimRuntime(opts.Logger)
	}
	return &Manager{opts: opts, logger: opts.Logger, now: opts.Now}
}

// ChannelForTask returns the fan-out channel name used for a task's stream.
// It is distinct from session IDs because the task stream is a multiplex of
// every stage's session — see arch §8.2.
func ChannelForTask(taskID string) string { return "task:" + taskID }

// CreateTask inserts a new task row. If req.WorkflowName is set, stages are
// inserted in pending state and stage 1 transitions to active.
func (m *Manager) CreateTask(ctx context.Context, req CreateTaskRequest) (*Task, error) {
	if strings.TrimSpace(req.IssueMD) == "" {
		return nil, fmt.Errorf("%w: issue body is required", ErrValidation)
	}
	if req.SourceKind == "" {
		req.SourceKind = SourceFreeform
	}
	if req.SourceKind != SourceGithubIssue && req.SourceKind != SourceFreeform {
		return nil, fmt.Errorf("%w: source_kind must be github_issue or freeform", ErrValidation)
	}
	if req.Name == "" {
		req.Name = trimTitle(req.IssueMD)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	taskID := ulidgen.WithPrefix("task")
	now := m.now()
	status := TaskStatusNotStarted
	var startedAt *string
	if req.WorkflowName != "" {
		status = TaskStatusWorking
		ts := now.Format(time.RFC3339Nano)
		startedAt = &ts
	}

	tx, err := m.opts.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks
        (task_id, name, workflow_name, repo_url, base_sha, source_kind, source_url, issue_md, current_stage_id, status, created_at, started_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`,
		taskID, req.Name, nullable(req.WorkflowName), nullable(req.RepoURL), nullable(""),
		req.SourceKind, nullable(req.SourceURL), req.IssueMD,
		status, now.Format(time.RFC3339Nano), nullableTime(startedAt)); err != nil {
		return nil, fmt.Errorf("insert task: %w", err)
	}

	var stages []Stage
	if req.WorkflowName != "" {
		wf, err := m.opts.Library.GetWorkflow(req.WorkflowName)
		if err != nil {
			return nil, fmt.Errorf("%w: workflow %q", ErrWorkflowNotFound, req.WorkflowName)
		}
		// Validate every stage's agent exists.
		for _, st := range wf.Stages {
			if _, err := m.opts.Library.GetAgent(st.Agent); err != nil {
				return nil, fmt.Errorf("%w: workflow %q references missing agent %q", ErrValidation, wf.Name, st.Agent)
			}
		}
		var stageIDs []string
		for i, st := range wf.Stages {
			id := ulidgen.WithPrefix("stg")
			stageIDs = append(stageIDs, id)
			if _, err := tx.ExecContext(ctx, `INSERT INTO stages
                (stage_id, task_id, position, agent_name, status, started_at)
                VALUES (?, ?, ?, ?, ?, ?)`,
				id, taskID, i+1, st.Agent, StageStatusPending, nullableString("")); err != nil {
				return nil, fmt.Errorf("insert stage: %w", err)
			}
		}
		firstStageID := stageIDs[0]
		if _, err := tx.ExecContext(ctx,
			`UPDATE stages SET status='active', started_at=? WHERE stage_id=?`,
			now.Format(time.RFC3339Nano), firstStageID); err != nil {
			return nil, fmt.Errorf("activate first stage: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET current_stage_id=? WHERE task_id=?`,
			firstStageID, taskID); err != nil {
			return nil, fmt.Errorf("set current_stage_id: %w", err)
		}
		stages = make([]Stage, 0, len(wf.Stages))
		for i, st := range wf.Stages {
			s := Stage{
				ID: stageIDs[i], TaskID: taskID, Position: i + 1, AgentName: st.Agent,
				Status: StageStatusPending,
			}
			if i == 0 {
				s.Status = StageStatusActive
				s.StartedAt = now
			}
			if a, err := m.opts.Library.GetAgent(st.Agent); err == nil {
				s.Colour = a.Colour
			}
			stages = append(stages, s)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	task := &Task{
		ID: taskID, Name: req.Name, WorkflowName: req.WorkflowName,
		RepoURL: req.RepoURL, SourceKind: req.SourceKind, SourceURL: req.SourceURL,
		IssueMD: req.IssueMD, Status: status, CreatedAt: now, Stages: stages,
	}
	if req.WorkflowName != "" {
		task.StartedAt = now
		task.CurrentStageID = stages[0].ID
		// Seed first stage's chat thread.
		seedMsg := fmt.Sprintf("Task opened.\n\n# %s\n\n%s", req.Name, req.IssueMD)
		m.recordMessage(ctx, taskID, stages[0].ID, "", RoleSystem, seedMsg)
		if err := m.spawnStage(ctx, task, &stages[0], "", ""); err != nil {
			m.logger.Warn("stage.spawn_failed", slog.String("task", taskID), slog.String("error", err.Error()))
			m.recordMessage(ctx, taskID, stages[0].ID, "", RoleError, "Failed to spawn stage: "+err.Error())
		}
	}
	m.broadcastStatus(taskID, "", status)
	return task, nil
}

// AttachWorkflow flips a not-started task to working with stages.
func (m *Manager) AttachWorkflow(ctx context.Context, taskID, workflow string) (*Task, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	task, err := m.loadTaskTx(ctx, nil, taskID)
	if err != nil {
		return nil, err
	}
	if task.Status != TaskStatusNotStarted {
		return nil, fmt.Errorf("%w: task is %s", ErrPreconditionFailed, task.Status)
	}
	wf, err := m.opts.Library.GetWorkflow(workflow)
	if err != nil {
		return nil, fmt.Errorf("%w: workflow %q", ErrWorkflowNotFound, workflow)
	}
	for _, st := range wf.Stages {
		if _, err := m.opts.Library.GetAgent(st.Agent); err != nil {
			return nil, fmt.Errorf("%w: missing agent %q", ErrValidation, st.Agent)
		}
	}
	now := m.now()
	tx, err := m.opts.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()

	stageIDs := make([]string, len(wf.Stages))
	for i, st := range wf.Stages {
		stageIDs[i] = ulidgen.WithPrefix("stg")
		if _, err := tx.ExecContext(ctx, `INSERT INTO stages
            (stage_id, task_id, position, agent_name, status)
            VALUES (?, ?, ?, ?, 'pending')`,
			stageIDs[i], taskID, i+1, st.Agent); err != nil {
			return nil, fmt.Errorf("insert stage: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE stages SET status='active', started_at=? WHERE stage_id=?`,
		now.Format(time.RFC3339Nano), stageIDs[0]); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET workflow_name=?, status='working', started_at=?, current_stage_id=? WHERE task_id=?`,
		workflow, now.Format(time.RFC3339Nano), stageIDs[0], taskID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	reloaded, _ := m.loadTaskTx(ctx, nil, taskID)
	seedMsg := fmt.Sprintf("Task opened.\n\n# %s\n\n%s", reloaded.Name, reloaded.IssueMD)
	m.recordMessage(ctx, taskID, stageIDs[0], "", RoleSystem, seedMsg)
	first := &reloaded.Stages[0]
	if err := m.spawnStage(ctx, reloaded, first, "", ""); err != nil {
		m.logger.Warn("stage.spawn_failed", slog.String("task", taskID), slog.String("error", err.Error()))
		m.recordMessage(ctx, taskID, first.ID, "", RoleError, "Failed to spawn stage: "+err.Error())
	}
	m.broadcastStatus(taskID, TaskStatusNotStarted, TaskStatusWorking)
	return reloaded, nil
}

// Send routes a user message to the current stage's agent.
func (m *Manager) Send(ctx context.Context, req SendMessageRequest) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, err := m.loadTaskTx(ctx, nil, req.TaskID)
	if err != nil {
		return err
	}
	if task.Status == TaskStatusDone || task.Status == TaskStatusAbandoned {
		return ErrTerminal
	}
	stage, ok := activeStage(task)
	if !ok {
		return fmt.Errorf("%w: no active stage", ErrPreconditionFailed)
	}
	m.recordMessage(ctx, task.ID, stage.ID, stage.AgentName, RoleUser, req.Content)
	if err := m.opts.Runtime.SendUserMessage(ctx, SendMessageInput{
		TaskID: task.ID, StageID: stage.ID, SessionID: stage.SessionID, Content: req.Content,
	}); err != nil {
		m.recordMessage(ctx, task.ID, stage.ID, stage.AgentName, RoleError, "Runtime send failed: "+err.Error())
		return err
	}
	return nil
}

// Handoff triggers the synthesis auto-prompt on the active stage. The
// runtime's Synthesize call returns the agent's reply synchronously, which
// the manager locks as the stage's synthesis before advancing the workflow.
//
// Returns ErrStageBusy if a turn is currently in flight; the SPA disables
// the Hand off button in that case.
func (m *Manager) Handoff(ctx context.Context, taskID string) error {
	m.mu.Lock()
	task, err := m.loadTaskTx(ctx, nil, taskID)
	if err != nil {
		m.mu.Unlock()
		return err
	}
	if task.Status != TaskStatusWorking {
		m.mu.Unlock()
		return ErrTerminal
	}
	stage, ok := activeStage(task)
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("%w: no active stage", ErrPreconditionFailed)
	}
	if m.opts.Runtime.IsBusy(stage.ID) {
		m.mu.Unlock()
		return ErrStageBusy
	}
	stageCopy := *stage
	taskCopy := *task
	m.mu.Unlock()

	isFinal := stageCopy.Position == len(taskCopy.Stages)

	m.recordMessage(ctx, taskCopy.ID, stageCopy.ID, stageCopy.AgentName, RoleSystem,
		"⤳ Hand off requested. The agent is producing its synthesis…")
	synthesis, err := m.opts.Runtime.Synthesize(ctx, SendMessageInput{
		TaskID: taskCopy.ID, StageID: stageCopy.ID, SessionID: stageCopy.SessionID, Content: HandoffAutoPrompt,
	})
	if err != nil {
		m.recordMessage(ctx, taskCopy.ID, stageCopy.ID, stageCopy.AgentName, RoleError, "Synthesis failed: "+err.Error())
		return err
	}
	m.recordMessage(ctx, taskCopy.ID, stageCopy.ID, stageCopy.AgentName, RoleSynthesis, synthesis)
	m.lockSynthesisAndAdvance(ctx, taskCopy.ID, stageCopy.ID, synthesis, isFinal)
	return nil
}

// Complete marks a working task done. Requires the final stage to be active.
// In normal flow Complete is called after a final-stage Handoff has produced
// a synthesis; the manager allows Complete on the final active stage even
// without a prior synthesis (records empty synthesis).
func (m *Manager) Complete(ctx context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, err := m.loadTaskTx(ctx, nil, taskID)
	if err != nil {
		return err
	}
	if task.Status != TaskStatusWorking {
		return ErrTerminal
	}
	stage, ok := activeStage(task)
	if !ok || stage.Position != len(task.Stages) {
		return fmt.Errorf("%w: only the final stage can be completed", ErrPreconditionFailed)
	}
	now := m.now()
	if _, err := m.opts.Store.DB().ExecContext(ctx,
		`UPDATE stages SET status='done', ended_at=? WHERE stage_id=?`,
		now.Format(time.RFC3339Nano), stage.ID); err != nil {
		return err
	}
	if _, err := m.opts.Store.DB().ExecContext(ctx,
		`UPDATE tasks SET status='done', ended_at=?, current_stage_id=NULL WHERE task_id=?`,
		now.Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	if stage.SessionID != "" {
		_ = m.opts.Runtime.StopStage(ctx, stage.SessionID)
	}
	m.broadcastStatus(taskID, TaskStatusWorking, TaskStatusDone)
	return nil
}

// Abandon terminates the active stage and marks the task abandoned.
func (m *Manager) Abandon(ctx context.Context, taskID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, err := m.loadTaskTx(ctx, nil, taskID)
	if err != nil {
		return err
	}
	if task.Status == TaskStatusDone || task.Status == TaskStatusAbandoned {
		return ErrTerminal
	}
	from := task.Status
	now := m.now()
	if _, err := m.opts.Store.DB().ExecContext(ctx,
		`UPDATE tasks SET status='abandoned', ended_at=?, current_stage_id=NULL WHERE task_id=?`,
		now.Format(time.RFC3339Nano), taskID); err != nil {
		return err
	}
	if stage, ok := activeStage(task); ok {
		if stage.SessionID != "" {
			_ = m.opts.Runtime.StopStage(ctx, stage.SessionID)
		}
		_, _ = m.opts.Store.DB().ExecContext(ctx,
			`UPDATE stages SET status='done', ended_at=?, session_id=NULL, volume_name=NULL WHERE stage_id=?`,
			now.Format(time.RFC3339Nano), stage.ID)
	}
	m.broadcastStatus(taskID, from, TaskStatusAbandoned)
	return nil
}

// ListTasks returns all tasks ordered by created_at desc.
func (m *Manager) ListTasks(ctx context.Context) ([]Task, error) {
	rows, err := m.opts.Store.DB().QueryContext(ctx,
		`SELECT task_id, name, workflow_name, repo_url, base_sha, source_kind, source_url, issue_md, current_stage_id, status, created_at, started_at, ended_at
         FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	var out []Task
	for rows.Next() {
		t, err := scanTaskRow(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, t)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Load stages outside the outer cursor (modernc.org/sqlite serializes
	// queries on the single connection and would otherwise deadlock).
	for i := range out {
		stages, err := m.loadStages(ctx, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Stages = stages
	}
	return out, nil
}

// GetTask returns a full task view.
func (m *Manager) GetTask(ctx context.Context, taskID string) (*Task, error) {
	return m.loadTaskTx(ctx, nil, taskID)
}

// TaskMessages returns the recorded chat history for a task.
func (m *Manager) TaskMessages(ctx context.Context, taskID string) ([]Message, error) {
	rows, err := m.opts.Store.DB().QueryContext(ctx,
		`SELECT task_id, seq, stage_id, agent_name, at, role, content
         FROM task_messages WHERE task_id=? ORDER BY seq ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		var m Message
		var sid sql.NullString
		var aname sql.NullString
		var at string
		if err := rows.Scan(&m.TaskID, &m.Seq, &sid, &aname, &at, &m.Role, &m.Content); err != nil {
			return nil, err
		}
		m.StageID = sid.String
		m.AgentName = aname.String
		if t, err := time.Parse(time.RFC3339Nano, at); err == nil {
			m.At = t
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ----- internals -----

func (m *Manager) loadTaskTx(ctx context.Context, tx *sql.Tx, taskID string) (*Task, error) {
	row := m.opts.Store.DB().QueryRowContext(ctx,
		`SELECT task_id, name, workflow_name, repo_url, base_sha, source_kind, source_url, issue_md, current_stage_id, status, created_at, started_at, ended_at
         FROM tasks WHERE task_id=?`, taskID)
	t, err := scanTaskRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrTaskNotFound
		}
		return nil, err
	}
	stages, err := m.loadStages(ctx, taskID)
	if err != nil {
		return nil, err
	}
	t.Stages = stages
	return &t, nil
}

func (m *Manager) loadStages(ctx context.Context, taskID string) ([]Stage, error) {
	rows, err := m.opts.Store.DB().QueryContext(ctx,
		`SELECT stage_id, task_id, position, agent_name, session_id, volume_name, synthesis, status, started_at, ended_at
         FROM stages WHERE task_id=? ORDER BY position ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var stages []Stage
	for rows.Next() {
		var s Stage
		var sid, vol, syn, startedAt, endedAt sql.NullString
		if err := rows.Scan(&s.ID, &s.TaskID, &s.Position, &s.AgentName, &sid, &vol, &syn, &s.Status, &startedAt, &endedAt); err != nil {
			return nil, err
		}
		s.SessionID = sid.String
		s.VolumeName = vol.String
		s.Synthesis = syn.String
		if t, err := time.Parse(time.RFC3339Nano, startedAt.String); err == nil {
			s.StartedAt = t
		}
		if t, err := time.Parse(time.RFC3339Nano, endedAt.String); err == nil {
			s.EndedAt = t
		}
		if a, err := m.opts.Library.GetAgent(s.AgentName); err == nil {
			s.Colour = a.Colour
		}
		stages = append(stages, s)
	}
	return stages, rows.Err()
}

func scanTaskRow(s scanner) (Task, error) {
	var t Task
	var wf, repo, base, surl, csid, startedAt, endedAt sql.NullString
	var createdAt string
	if err := s.Scan(&t.ID, &t.Name, &wf, &repo, &base, &t.SourceKind, &surl, &t.IssueMD, &csid, &t.Status, &createdAt, &startedAt, &endedAt); err != nil {
		return t, err
	}
	t.WorkflowName = wf.String
	t.RepoURL = repo.String
	t.BaseSHA = base.String
	t.SourceURL = surl.String
	t.CurrentStageID = csid.String
	if tt, err := time.Parse(time.RFC3339Nano, createdAt); err == nil {
		t.CreatedAt = tt
	}
	if tt, err := time.Parse(time.RFC3339Nano, startedAt.String); err == nil {
		t.StartedAt = tt
	}
	if tt, err := time.Parse(time.RFC3339Nano, endedAt.String); err == nil {
		t.EndedAt = tt
	}
	return t, nil
}

type scanner interface {
	Scan(...any) error
}

func activeStage(t *Task) (*Stage, bool) {
	for i := range t.Stages {
		if t.Stages[i].Status == StageStatusActive {
			return &t.Stages[i], true
		}
	}
	return nil, false
}

func (m *Manager) recordMessage(ctx context.Context, taskID, stageID, agent, role, content string) {
	var seq int64
	_ = m.opts.Store.DB().QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0)+1 FROM task_messages WHERE task_id=?`, taskID).Scan(&seq)
	at := m.now().Format(time.RFC3339Nano)
	if _, err := m.opts.Store.DB().ExecContext(ctx,
		`INSERT INTO task_messages (task_id, seq, stage_id, agent_name, at, role, content) VALUES (?,?,?,?,?,?,?)`,
		taskID, seq, nullable(stageID), nullable(agent), at, role, content); err != nil {
		m.logger.Warn("task_message.insert_failed", slog.String("error", err.Error()))
		return
	}
	m.broadcastMessage(taskID, Message{
		TaskID: taskID, Seq: seq, StageID: stageID, AgentName: agent,
		At: m.now(), Role: role, Content: content,
	})
}

func (m *Manager) broadcastMessage(taskID string, msg Message) {
	data, _ := json.Marshal(msg)
	m.opts.Hub.Broadcast(ChannelForTask(taskID), proto.Event{
		EventID:   ulidgen.WithPrefix("evt"),
		Kind:      "task.message",
		SessionID: taskID,
		TS:        m.now(),
		Data:      data,
	})
}

func (m *Manager) broadcastStatus(taskID, from, to string) {
	data, _ := json.Marshal(map[string]string{"from": from, "to": to})
	m.opts.Hub.Broadcast(ChannelForTask(taskID), proto.Event{
		EventID:   ulidgen.WithPrefix("evt"),
		Kind:      "task.status_changed",
		SessionID: taskID,
		TS:        m.now(),
		Data:      data,
	})
}

func (m *Manager) broadcastStageAdvanced(taskID, fromStage, toStage string) {
	data, _ := json.Marshal(map[string]string{"from_stage_id": fromStage, "to_stage_id": toStage})
	m.opts.Hub.Broadcast(ChannelForTask(taskID), proto.Event{
		EventID:   ulidgen.WithPrefix("evt"),
		Kind:      "task.stage_advanced",
		SessionID: taskID,
		TS:        m.now(),
		Data:      data,
	})
}

func (m *Manager) handleAssistantMessage(stageID string, content string) {
	var taskID, agent string
	row := m.opts.Store.DB().QueryRow(`SELECT task_id, agent_name FROM stages WHERE stage_id=?`, stageID)
	if err := row.Scan(&taskID, &agent); err != nil {
		return
	}
	m.recordMessage(context.Background(), taskID, stageID, agent, RoleAssistant, content)
}

func (m *Manager) handleError(stageID, message string) {
	var taskID, agent string
	row := m.opts.Store.DB().QueryRow(`SELECT task_id, agent_name FROM stages WHERE stage_id=?`, stageID)
	if err := row.Scan(&taskID, &agent); err != nil {
		return
	}
	m.recordMessage(context.Background(), taskID, stageID, agent, RoleError, message)
}

func (m *Manager) lockSynthesisAndAdvance(ctx context.Context, taskID, stageID, synthesis string, isFinal bool) {
	now := m.now()
	tx, err := m.opts.Store.DB().BeginTx(ctx, nil)
	if err != nil {
		m.logger.Warn("lock_synthesis.begin_failed", slog.String("error", err.Error()))
		return
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx,
		`UPDATE stages SET status='done', ended_at=?, synthesis=?, session_id=NULL, volume_name=NULL WHERE stage_id=?`,
		now.Format(time.RFC3339Nano), synthesis, stageID); err != nil {
		m.logger.Warn("lock_synthesis.update_stage_failed", slog.String("error", err.Error()))
		return
	}

	if isFinal {
		if _, err := tx.ExecContext(ctx,
			`UPDATE tasks SET status='done', ended_at=?, current_stage_id=NULL WHERE task_id=?`,
			now.Format(time.RFC3339Nano), taskID); err != nil {
			m.logger.Warn("lock_synthesis.complete_task_failed", slog.String("error", err.Error()))
			return
		}
		if err := tx.Commit(); err != nil {
			return
		}
		m.broadcastStatus(taskID, TaskStatusWorking, TaskStatusDone)
		return
	}

	// Find the next pending stage.
	var nextID string
	if err := tx.QueryRowContext(ctx,
		`SELECT stage_id FROM stages WHERE task_id=? AND status='pending' ORDER BY position ASC LIMIT 1`,
		taskID).Scan(&nextID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Shouldn't happen — handoff on non-final stage should have a next.
			if _, err := tx.ExecContext(ctx,
				`UPDATE tasks SET status='done', ended_at=?, current_stage_id=NULL WHERE task_id=?`,
				now.Format(time.RFC3339Nano), taskID); err == nil {
				_ = tx.Commit()
				m.broadcastStatus(taskID, TaskStatusWorking, TaskStatusDone)
			}
			return
		}
		m.logger.Warn("lock_synthesis.next_stage_failed", slog.String("error", err.Error()))
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE stages SET status='active', started_at=? WHERE stage_id=?`,
		now.Format(time.RFC3339Nano), nextID); err != nil {
		m.logger.Warn("lock_synthesis.activate_next_failed", slog.String("error", err.Error()))
		return
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tasks SET current_stage_id=? WHERE task_id=?`, nextID, taskID); err != nil {
		m.logger.Warn("lock_synthesis.current_stage_failed", slog.String("error", err.Error()))
		return
	}
	if err := tx.Commit(); err != nil {
		return
	}
	m.broadcastStageAdvanced(taskID, stageID, nextID)

	// Insert seam message + spawn next stage.
	task, err := m.loadTaskTx(ctx, nil, taskID)
	if err != nil {
		return
	}
	var prevAgent, nextStageAgent string
	for _, s := range task.Stages {
		if s.ID == stageID {
			prevAgent = s.AgentName
		}
		if s.ID == nextID {
			nextStageAgent = s.AgentName
		}
	}
	m.recordMessage(ctx, taskID, nextID, "", RoleSeam, fmt.Sprintf("handed off to %s", nextStageAgent))
	for i := range task.Stages {
		if task.Stages[i].ID == nextID {
			if err := m.spawnStage(ctx, task, &task.Stages[i], prevAgent, synthesis); err != nil {
				m.logger.Warn("stage.spawn_failed", slog.String("task", taskID), slog.String("error", err.Error()))
				m.recordMessage(ctx, taskID, nextID, "", RoleError, "Failed to spawn next stage: "+err.Error())
			}
			break
		}
	}
}

func (m *Manager) spawnStage(ctx context.Context, task *Task, stage *Stage, prevAgent, handoffIn string) error {
	agent, err := m.opts.Library.GetAgent(stage.AgentName)
	if err != nil {
		return err
	}
	stageRef := *stage
	in := StartStageInput{
		TaskID:      task.ID,
		StageID:     stage.ID,
		Position:    stage.Position,
		Agent:       agent,
		PrevAgent:   prevAgent,
		IssueMD:     task.IssueMD,
		HandoffInMD: handoffIn,
		RepoURL:     task.RepoURL,
		BaseSHA:     task.BaseSHA,
		OnAssistantMessage: func(content string) {
			m.handleAssistantMessage(stageRef.ID, content)
		},
		OnError: func(message string) {
			m.handleError(stageRef.ID, message)
		},
	}
	if stage.Position < len(task.Stages) {
		in.NextAgent = task.Stages[stage.Position].AgentName // 1-indexed Position → next index
	}
	res, err := m.opts.Runtime.StartStage(ctx, in)
	if err != nil {
		return err
	}
	if _, err := m.opts.Store.DB().ExecContext(ctx,
		`UPDATE stages SET session_id=?, volume_name=? WHERE stage_id=?`,
		nullable(res.SessionID), nullable(res.VolumeName), stage.ID); err != nil {
		return err
	}
	return nil
}

// ----- helpers -----

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableString(s string) any { return nullable(s) }

func nullableTime(p *string) any {
	if p == nil || *p == "" {
		return nil
	}
	return *p
}

func trimTitle(body string) string {
	body = strings.TrimSpace(body)
	if idx := strings.Index(body, "\n"); idx > 0 {
		body = body[:idx]
	}
	body = strings.TrimPrefix(body, "# ")
	if len(body) > 60 {
		body = body[:60]
	}
	if body == "" {
		body = "Untitled task"
	}
	return body
}
