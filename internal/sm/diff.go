package sm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

type DiffRequest struct {
	Repo   string
	Format string
}

type PushRequest struct {
	Repo    string
	Branch  string
	Message string
}

type PushResult struct {
	Success bool
	Repo    string
	Branch  string
	Output  string
	Error   string
}

type DiffStream interface {
	Recv() (DiffChunk, error)
	Close() error
}

type DiffChunk struct {
	Repo     string
	Data     []byte
	End      bool
	ExitCode int
	BaseSHA  string
	Branch   string
	Note     string
	ErrorMsg string
}

var ErrDiffUnavailable = errors.New("diff unavailable: shim not connected")

func (m *manager) SessionRepos(_ context.Context, sessionID string) ([]proto.RepoState, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return nil, ErrSessionNotFound
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]proto.RepoState, len(a.repos))
	copy(out, a.repos)
	return out, nil
}

type pendingDiff struct {
	ch     chan DiffChunk
	done   chan struct{}
	closed bool
}

func (m *manager) Diff(ctx context.Context, sessionID string, req DiffRequest) (DiffStream, error) {
	return m.startDiff(ctx, sessionID, req, AgentdDiffRequest, false)
}

func (m *manager) ExportPatch(ctx context.Context, sessionID string, req DiffRequest) (DiffStream, error) {
	return m.startDiff(ctx, sessionID, req, AgentdExportPatchReq, true)
}

func (m *manager) startDiff(ctx context.Context, sessionID string, req DiffRequest, kind string, _ bool) (DiffStream, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return nil, ErrSessionNotFound
	}
	a.mu.Lock()
	conn := a.control
	a.mu.Unlock()
	if conn == nil {
		return nil, ErrDiffUnavailable
	}
	reqID := ulidgen.New()
	pd := &pendingDiff{ch: make(chan DiffChunk, 32), done: make(chan struct{})}
	a.mu.Lock()
	if a.pendingDiffs == nil {
		a.pendingDiffs = map[string]*pendingDiff{}
	}
	a.pendingDiffs[reqID] = pd
	a.mu.Unlock()
	payload := map[string]any{"request_id": reqID}
	if req.Repo != "" {
		payload["repo"] = req.Repo
	}
	fmtVal := req.Format
	if fmtVal == "" {
		fmtVal = "unified"
	}
	payload["format"] = fmtVal
	body, _ := json.Marshal(payload)
	if err := conn.Send(ControlFrame{V: 1, Kind: kind, TS: a.opts.Now(), Data: body}); err != nil {
		a.removePendingDiff(reqID)
		return nil, fmt.Errorf("diff send: %w", err)
	}
	expected := 1
	if req.Repo == "" {
		expected = -1
	}
	return &diffStream{
		actor:     a,
		requestID: reqID,
		pd:        pd,
		ctx:       ctx,
		expected:  expected,
		seenEnds:  0,
	}, nil
}

func (m *manager) ExportPush(ctx context.Context, sessionID string, req PushRequest) (PushResult, error) {
	a := m.actorFor(sessionID)
	if a == nil {
		return PushResult{}, ErrSessionNotFound
	}
	if req.Branch == "" {
		return PushResult{}, fmt.Errorf("branch required")
	}
	a.mu.Lock()
	conn := a.control
	a.mu.Unlock()
	if conn == nil {
		return PushResult{}, ErrDiffUnavailable
	}
	reqID := ulidgen.New()
	ch := make(chan ControlFrame, 1)
	a.mu.Lock()
	if a.pendingPush == nil {
		a.pendingPush = map[string]chan ControlFrame{}
	}
	a.pendingPush[reqID] = ch
	a.mu.Unlock()
	body, _ := json.Marshal(map[string]any{
		"request_id": reqID,
		"repo":       req.Repo,
		"branch":     req.Branch,
		"message":    req.Message,
	})
	if err := conn.Send(ControlFrame{V: 1, Kind: AgentdExportPushReq, TS: a.opts.Now(), Data: body}); err != nil {
		a.mu.Lock()
		delete(a.pendingPush, reqID)
		a.mu.Unlock()
		return PushResult{}, fmt.Errorf("push send: %w", err)
	}
	timeout := 5 * time.Minute
	select {
	case fr := <-ch:
		var pr struct {
			Success bool   `json:"success"`
			Repo    string `json:"repo"`
			Branch  string `json:"branch"`
			Output  string `json:"output"`
			Error   string `json:"error"`
		}
		_ = json.Unmarshal(fr.Data, &pr)
		return PushResult{Success: pr.Success, Repo: pr.Repo, Branch: pr.Branch, Output: pr.Output, Error: pr.Error}, nil
	case <-time.After(timeout):
		a.mu.Lock()
		delete(a.pendingPush, reqID)
		a.mu.Unlock()
		return PushResult{}, fmt.Errorf("push timeout after %s", timeout)
	case <-ctx.Done():
		a.mu.Lock()
		delete(a.pendingPush, reqID)
		a.mu.Unlock()
		return PushResult{}, ctx.Err()
	}
}

func (a *actor) removePendingDiff(reqID string) {
	a.mu.Lock()
	pd, ok := a.pendingDiffs[reqID]
	if ok {
		delete(a.pendingDiffs, reqID)
	}
	a.mu.Unlock()
	if ok && pd != nil {
		closePending(pd)
	}
}

func closePending(pd *pendingDiff) {
	if pd.closed {
		return
	}
	pd.closed = true
	close(pd.ch)
	close(pd.done)
}

// routeDiffChunk delivers an incoming runtime.diff_chunk frame to its
// awaiting DiffStream. If the request id is unknown the frame is dropped.
func (a *actor) routeDiffChunk(fr ControlFrame) {
	var meta struct {
		RequestID string `json:"request_id"`
		Repo      string `json:"repo"`
		Data      string `json:"data"`
	}
	if err := json.Unmarshal(fr.Data, &meta); err != nil {
		return
	}
	a.mu.Lock()
	pd, ok := a.pendingDiffs[meta.RequestID]
	a.mu.Unlock()
	if !ok || pd.closed {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(meta.Data)
	if err != nil {
		return
	}
	pd.ch <- DiffChunk{Repo: meta.Repo, Data: raw}
}

func (a *actor) routeDiffEnd(fr ControlFrame) {
	var meta struct {
		RequestID string `json:"request_id"`
		Repo      string `json:"repo"`
		ExitCode  int    `json:"exit_code"`
		BaseSHA   string `json:"base_sha"`
		Branch    string `json:"branch"`
		Note      string `json:"note"`
		Error     string `json:"error"`
	}
	if err := json.Unmarshal(fr.Data, &meta); err != nil {
		return
	}
	a.mu.Lock()
	pd, ok := a.pendingDiffs[meta.RequestID]
	a.mu.Unlock()
	if !ok || pd.closed {
		return
	}
	pd.ch <- DiffChunk{
		Repo: meta.Repo, End: true,
		ExitCode: meta.ExitCode, BaseSHA: meta.BaseSHA, Branch: meta.Branch,
		Note: meta.Note, ErrorMsg: meta.Error,
	}
}

func (a *actor) routePushResult(fr ControlFrame) {
	var meta struct {
		RequestID string `json:"request_id"`
	}
	if err := json.Unmarshal(fr.Data, &meta); err != nil {
		return
	}
	a.mu.Lock()
	ch, ok := a.pendingPush[meta.RequestID]
	if ok {
		delete(a.pendingPush, meta.RequestID)
	}
	a.mu.Unlock()
	if ok {
		select {
		case ch <- fr:
		default:
		}
	}
}

type diffStream struct {
	actor     *actor
	requestID string
	pd        *pendingDiff
	ctx       context.Context
	expected  int
	seenEnds  int
	doneSent  bool
}

func (s *diffStream) Recv() (DiffChunk, error) {
	if s.doneSent {
		return DiffChunk{}, io.EOF
	}
	for {
		select {
		case ch, ok := <-s.pd.ch:
			if !ok {
				s.doneSent = true
				return DiffChunk{}, io.EOF
			}
			if ch.End {
				s.seenEnds++
				done := false
				if s.expected > 0 && s.seenEnds >= s.expected {
					done = true
				}
				if s.expected < 0 && ch.Repo == "" {
					done = true
				}
				if done {
					s.doneSent = true
					s.actor.removePendingDiff(s.requestID)
				}
				return ch, nil
			}
			return ch, nil
		case <-s.ctx.Done():
			s.actor.removePendingDiff(s.requestID)
			return DiffChunk{}, s.ctx.Err()
		}
	}
}

func (s *diffStream) Close() error {
	if !s.doneSent {
		s.actor.removePendingDiff(s.requestID)
		s.doneSent = true
	}
	return nil
}
