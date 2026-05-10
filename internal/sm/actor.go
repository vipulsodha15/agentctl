package sm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	agentlog "github.com/agentctl/agentctl/internal/log"
	"github.com/agentctl/agentctl/internal/mcp"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/ulidgen"
)

const mailboxSize = 64

type mboxKind int

const (
	mboxSend mboxKind = iota + 1
	mboxInterrupt
	mboxTerminate
	mboxControlFrame
	mboxControlConn
	mboxControlClosed
	mboxShutdown
	mboxStop
)

type sendItem struct {
	messageID string
	content   string
	clientID  string
	reply     chan SendResult
}

type interruptItem struct {
	clearQueue bool
	reply      chan InterruptResult
	errReply   chan error
}

type terminateItem struct {
	reply chan error
}

type stopItem struct {
	reason string
	reply  chan error
}

type controlFrameItem struct {
	frame ControlFrame
}

type controlConnItem struct {
	conn ControlConn
}

type mboxItem struct {
	kind         mboxKind
	send         *sendItem
	interrupt    *interruptItem
	terminate    *terminateItem
	stop         *stopItem
	controlFrame *controlFrameItem
	controlConn  *controlConnItem
}

type queuedMessage struct {
	messageID string
	content   string
	clientID  string
}

type actorOptions struct {
	ID              string
	Summary         proto.SessionSummary
	SessionDir      string
	Hub             fan.Hub
	Logger          *slog.Logger
	DaemonLogger    *slog.Logger
	SessionLogger   *agentlog.SessionLogger
	Now             func() time.Time
	SnapshotTimeout time.Duration
	ShutdownGrace   time.Duration
	Containers      ContainerManager
	Control         ControlServer
	ControlSockPath string
	SessionToken    string
	Store           *store.Store
	Repos           []proto.RepoState
	ResolvedMCPs    []mcp.Entry
	GitHubPAT       string
	SkillCollisions []string
}

type actor struct {
	opts    actorOptions
	mailbox chan mboxItem
	wg      sync.WaitGroup
	stopCh  chan struct{}

	mu                     sync.RWMutex
	summary                proto.SessionSummary
	queue                  []queuedMessage
	inFlight               string
	currentTurn            string
	mcpStatus              map[string]string
	mcpFailures            []proto.MCPUnreachableData
	mcpFailuresEmitted     bool
	skillCollisions        []string
	skillCollisionsEmitted bool
	repos                  []proto.RepoState
	control                ControlConn
	terminated             bool
	pendingSnap            map[string]chan ControlFrame
	containerID            string
	lastError              string
}

func newActor(opts actorOptions) *actor {
	return &actor{
		opts:            opts,
		mailbox:         make(chan mboxItem, mailboxSize),
		stopCh:          make(chan struct{}),
		summary:         opts.Summary,
		mcpStatus:       map[string]string{},
		skillCollisions: append([]string(nil), opts.SkillCollisions...),
		repos:           opts.Repos,
		pendingSnap:     map[string]chan ControlFrame{},
	}
}

func (a *actor) start() {
	a.wg.Add(1)
	go a.run()
}

func (a *actor) run() {
	defer a.wg.Done()
	for {
		select {
		case <-a.stopCh:
			return
		case item := <-a.mailbox:
			a.handle(item)
			if item.kind == mboxTerminate || item.kind == mboxShutdown {
				return
			}
		}
	}
}

func (a *actor) shutdown() {
	select {
	case <-a.stopCh:
		return
	default:
	}
	close(a.stopCh)
	a.wg.Wait()
	a.opts.Hub.Close(a.opts.ID, fan.StreamEndSessionTerminated)
	if a.opts.SessionLogger != nil {
		_ = a.opts.SessionLogger.Close()
	}
}

func (a *actor) handle(item mboxItem) {
	switch item.kind {
	case mboxSend:
		a.handleSend(item.send)
	case mboxInterrupt:
		a.handleInterrupt(item.interrupt)
	case mboxTerminate:
		a.handleTerminate(item.terminate)
	case mboxStop:
		a.handleStop(item.stop)
	case mboxControlFrame:
		a.handleControlFrame(item.controlFrame.frame)
	case mboxControlConn:
		a.handleControlConn(item.controlConn.conn)
	case mboxControlClosed:
		a.handleControlClosed()
	}
}

func (a *actor) handleStop(s *stopItem) {
	a.mu.Lock()
	if a.summary.Status == "stopped" || a.summary.Status == "terminated" {
		a.mu.Unlock()
		s.reply <- nil
		return
	}
	prevStatus := a.summary.Status
	a.summary.Status = "stopped"
	a.summary.InFlight = false
	a.queue = a.queue[:0]
	a.summary.QueueDepth = 0
	a.inFlight = ""
	a.currentTurn = ""
	containerID := a.containerID
	a.mu.Unlock()

	a.broadcast(proto.EventSessionStopping, mustJSON(map[string]string{"reason": s.reason}))

	if a.opts.Containers != nil && containerID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), a.opts.ShutdownGrace+5*time.Second)
		_ = a.opts.Containers.Stop(ctx, containerID, a.opts.ShutdownGrace)
		cancel()
	}

	a.mu.Lock()
	if a.control != nil {
		_ = a.control.Close()
		a.control = nil
	}
	a.mu.Unlock()
	if a.opts.Control != nil {
		_ = a.opts.Control.Stop(a.opts.ID)
	}

	if a.opts.Store != nil {
		now := a.opts.Now().Format(time.RFC3339Nano)
		if _, err := a.opts.Store.DB().Exec(`UPDATE sessions SET status='stopped', container_id=NULL WHERE id=?`, a.opts.ID); err != nil {
			a.opts.DaemonLogger.Warn("session.stop.db_update_failed", slog.String("session_id", a.opts.ID), slog.String("error", err.Error()))
		}
		_, _ = a.opts.Store.DB().Exec(`INSERT INTO session_lifecycle (session_id, at, event, detail_json) VALUES (?, ?, 'stopped', ?)`,
			a.opts.ID, now, mustJSON(map[string]string{"reason": s.reason}))
	}

	a.broadcast(proto.EventSessionStopped, mustJSON(map[string]string{"reason": s.reason, "previous": prevStatus}))
	s.reply <- nil
}

func (a *actor) handleSend(s *sendItem) {
	a.mu.Lock()
	defer func() { a.mu.Unlock() }()
	a.summary.LastActivityAt = a.opts.Now()
	if a.inFlight == "" {
		turnID := ulidgen.WithPrefix("turn")
		a.inFlight = turnID
		a.currentTurn = turnID
		a.broadcastLocked(proto.EventUserMessage, mustJSON(proto.UserMessageData{
			MessageID: s.messageID, Content: s.content, ClientID: s.clientID,
		}))
		a.broadcastLocked(proto.EventTurnStart, mustJSON(proto.TurnStartData{
			TurnID: turnID, MessageID: s.messageID, Model: a.summary.Model,
		}))
		a.sendControlLocked(AgentdMessage, mustJSON(map[string]any{
			"message_id": s.messageID, "content": s.content,
		}))
		s.reply <- SendResult{MessageID: s.messageID, Queued: false, QueueDepth: len(a.queue)}
		a.summary.InFlight = true
		return
	}
	a.queue = append(a.queue, queuedMessage{messageID: s.messageID, content: s.content, clientID: s.clientID})
	a.summary.QueueDepth = len(a.queue)
	a.broadcastLocked(proto.EventQueueDepth, mustJSON(proto.QueueDepthData{Depth: len(a.queue)}))
	s.reply <- SendResult{MessageID: s.messageID, Queued: true, QueueDepth: len(a.queue)}
}

func (a *actor) handleInterrupt(it *interruptItem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight == "" {
		it.errReply <- ErrNoInFlight
		return
	}
	a.sendControlLocked(AgentdInterrupt, mustJSON(map[string]string{"reason": "user"}))
	cleared := 0
	if it.clearQueue {
		cleared = len(a.queue)
		a.queue = a.queue[:0]
		a.summary.QueueDepth = 0
		a.broadcastLocked(proto.EventQueueDepth, mustJSON(proto.QueueDepthData{Depth: 0}))
	}
	a.opts.Logger.Info("session.interrupt_requested", slog.String("turn_id", a.inFlight), slog.Bool("clear_queue", it.clearQueue))
	it.reply <- InterruptResult{Interrupted: true, ClearedQueueDepth: cleared}
}

func (a *actor) handleTerminate(t *terminateItem) {
	a.mu.Lock()
	if a.terminated {
		a.mu.Unlock()
		t.reply <- nil
		return
	}
	a.terminated = true
	if a.inFlight != "" {
		a.sendControlLocked(AgentdInterrupt, mustJSON(map[string]string{"reason": "shutdown"}))
	}
	a.queue = a.queue[:0]
	a.summary.QueueDepth = 0
	a.summary.Status = "terminated"
	a.summary.InFlight = false
	a.sendControlLocked(AgentdShutdown, mustJSON(map[string]int{"grace_seconds": int(a.opts.ShutdownGrace.Seconds())}))
	if a.control != nil {
		_ = a.control.Close()
		a.control = nil
	}
	a.mu.Unlock()

	if a.opts.Containers != nil && a.summary.ImageID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), a.opts.ShutdownGrace+5*time.Second)
		_ = a.opts.Containers.Stop(ctx, a.opts.ID, a.opts.ShutdownGrace)
		_ = a.opts.Containers.Remove(ctx, a.opts.ID, true)
		cancel()
	}
	if a.opts.Control != nil {
		_ = a.opts.Control.Stop(a.opts.ID)
	}

	if a.opts.Store != nil {
		now := a.opts.Now().Format(time.RFC3339Nano)
		_, err := a.opts.Store.DB().Exec(`UPDATE sessions SET status='terminated', terminated_at=?, container_id=NULL, network_id=NULL WHERE id=?`,
			now, a.opts.ID)
		if err != nil {
			a.opts.DaemonLogger.Warn("session.terminate.db_update_failed", slog.String("session_id", a.opts.ID), slog.String("error", err.Error()))
		}
		_, _ = a.opts.Store.DB().Exec(`INSERT INTO session_lifecycle (session_id, at, event, detail_json) VALUES (?, ?, 'terminated', ?)`,
			a.opts.ID, now, `{}`)
	}

	a.broadcast(proto.EventSessionTerminated, mustJSON(map[string]any{}))
	t.reply <- nil
}

func (a *actor) handleControlConn(conn ControlConn) {
	a.mu.Lock()
	if a.control != nil {
		_ = a.control.Close()
	}
	a.control = conn
	a.mu.Unlock()
	go a.readControl(conn)
}

func (a *actor) handleControlClosed() {
	a.mu.Lock()
	a.control = nil
	a.mu.Unlock()
	a.opts.Logger.Info("session.control_disconnected")
}

func (a *actor) readControl(conn ControlConn) {
	for {
		fr, err := conn.Recv()
		if err != nil {
			a.mailbox <- mboxItem{kind: mboxControlClosed}
			return
		}
		a.mailbox <- mboxItem{kind: mboxControlFrame, controlFrame: &controlFrameItem{frame: fr}}
	}
}

func (a *actor) handleControlFrame(fr ControlFrame) {
	switch fr.Kind {
	case RuntimeHello:
		a.sendGreet()
	case RuntimeReady:
		a.mu.Lock()
		a.summary.Status = "running"
		a.mu.Unlock()
		a.broadcast(proto.EventSessionRunning, mustJSON(map[string]any{}))
		if a.opts.Store != nil {
			_, _ = a.opts.Store.DB().Exec(`UPDATE sessions SET status='running' WHERE id=?`, a.opts.ID)
		}
	case RuntimeEvent:
		a.handleRuntimeEvent(fr)
	case RuntimeError:
		a.mu.Lock()
		a.summary.Status = "error"
		a.mu.Unlock()
		a.broadcast(proto.EventSessionError, fr.Data)
	case RuntimeSnapshot:
		a.routeSnapshotReply(fr)
	case RuntimeHeartbeat:
		a.mu.Lock()
		a.summary.LastActivityAt = a.opts.Now()
		a.mu.Unlock()
	}
}

func (a *actor) handleRuntimeEvent(fr ControlFrame) {
	var inner RuntimeEventData
	if err := json.Unmarshal(fr.Data, &inner); err != nil {
		a.opts.Logger.Warn("control.malformed_runtime_event", slog.String("error", err.Error()))
		return
	}
	a.mu.Lock()
	a.summary.LastActivityAt = a.opts.Now()
	a.mu.Unlock()
	switch inner.Kind {
	case proto.EventTurnEnd:
		a.broadcast(inner.Kind, inner.Data)
		a.completeTurn("ok")
	case proto.EventTurnCancelled:
		a.broadcast(inner.Kind, inner.Data)
		a.completeTurn("cancelled")
	case proto.EventUsage:
		// TODO(M5): persist usage rows in `usage` table; M2 only fans out the event.
		a.broadcast(inner.Kind, inner.Data)
	default:
		a.broadcast(inner.Kind, inner.Data)
	}
}

func (a *actor) completeTurn(_ string) {
	a.mu.Lock()
	a.inFlight = ""
	a.summary.InFlight = false
	a.currentTurn = ""
	var next *queuedMessage
	if len(a.queue) > 0 {
		head := a.queue[0]
		a.queue = a.queue[1:]
		a.summary.QueueDepth = len(a.queue)
		next = &head
	}
	a.mu.Unlock()
	if next != nil {
		a.broadcast(proto.EventQueueDepth, mustJSON(proto.QueueDepthData{Depth: a.queueDepth()}))
		a.startTurnFor(*next)
	}
}

func (a *actor) startTurnFor(q queuedMessage) {
	a.mu.Lock()
	turnID := ulidgen.WithPrefix("turn")
	a.inFlight = turnID
	a.currentTurn = turnID
	a.summary.InFlight = true
	a.broadcastLocked(proto.EventUserMessage, mustJSON(proto.UserMessageData{
		MessageID: q.messageID, Content: q.content, ClientID: q.clientID,
	}))
	a.broadcastLocked(proto.EventTurnStart, mustJSON(proto.TurnStartData{
		TurnID: turnID, MessageID: q.messageID, Model: a.summary.Model,
	}))
	a.sendControlLocked(AgentdMessage, mustJSON(map[string]any{
		"message_id": q.messageID, "content": q.content,
	}))
	a.mu.Unlock()
}

func (a *actor) routeSnapshotReply(fr ControlFrame) {
	var meta struct {
		RequestID string `json:"request_id"`
	}
	_ = json.Unmarshal(fr.Data, &meta)
	a.mu.Lock()
	ch, ok := a.pendingSnap[meta.RequestID]
	if ok {
		delete(a.pendingSnap, meta.RequestID)
	}
	a.mu.Unlock()
	if ok {
		ch <- fr
	}
}

func (a *actor) snapshotEvent(ctx context.Context) (*proto.Event, error) {
	conv := json.RawMessage(`[]`)
	a.mu.Lock()
	conn := a.control
	a.mu.Unlock()
	if conn != nil {
		reqID := ulidgen.New()
		ch := make(chan ControlFrame, 1)
		a.mu.Lock()
		a.pendingSnap[reqID] = ch
		a.mu.Unlock()
		if err := conn.Send(ControlFrame{
			V:    1,
			Kind: AgentdSnapshotRequest,
			TS:   a.opts.Now(),
			Data: mustJSON(map[string]string{"request_id": reqID}),
		}); err != nil {
			a.mu.Lock()
			delete(a.pendingSnap, reqID)
			a.mu.Unlock()
			return nil, fmt.Errorf("snapshot send: %w", err)
		}
		timeout := a.opts.SnapshotTimeout
		if timeout == 0 {
			timeout = 10 * time.Second
		}
		select {
		case fr := <-ch:
			conv = fr.Data
		case <-time.After(timeout):
			a.mu.Lock()
			delete(a.pendingSnap, reqID)
			a.mu.Unlock()
			return nil, fmt.Errorf("snapshot timeout after %s", timeout)
		case <-ctx.Done():
			a.mu.Lock()
			delete(a.pendingSnap, reqID)
			a.mu.Unlock()
			return nil, ctx.Err()
		}
	}
	a.mu.RLock()
	data := proto.SessionSnapshotData{
		Session:      a.summary,
		Conversation: conv,
		QueueDepth:   len(a.queue),
		InFlight:     a.inFlight,
		MCPsStatus:   copyStrMap(a.mcpStatus),
		Repos:        append([]proto.RepoState(nil), a.repos...),
	}
	a.mu.RUnlock()
	body, _ := json.Marshal(data)
	ev := proto.Event{
		EventID:   ulidgen.New(),
		Kind:      proto.EventSessionSnapshot,
		SessionID: a.opts.ID,
		TS:        a.opts.Now(),
		Data:      body,
	}
	go a.emitDeferredAttachEvents()
	return &ev, nil
}

func (a *actor) snapshotSummary() proto.SessionSummary {
	a.mu.RLock()
	defer a.mu.RUnlock()
	s := a.summary
	s.QueueDepth = len(a.queue)
	s.InFlight = a.inFlight != ""
	return s
}

func (a *actor) snapshotDetail() proto.SessionDetail {
	a.mu.RLock()
	defer a.mu.RUnlock()
	d := proto.SessionDetail{
		SessionSummary: a.summary,
		MCPStatus:      copyStrMap(a.mcpStatus),
		ContainerID:    a.containerID,
		LastError:      a.lastError,
	}
	d.QueueDepth = len(a.queue)
	d.InFlight = a.inFlight != ""
	return d
}

func (a *actor) queueDepth() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.queue)
}

func (a *actor) sendControlLocked(kind string, data json.RawMessage) {
	if a.control == nil {
		return
	}
	if err := a.control.Send(ControlFrame{V: 1, Kind: kind, TS: a.opts.Now(), Data: data}); err != nil {
		a.opts.Logger.Warn("control.send_failed", slog.String("kind", kind), slog.String("error", err.Error()))
	}
}

func (a *actor) broadcast(kind string, data json.RawMessage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.broadcastLocked(kind, data)
}

func (a *actor) broadcastLocked(kind string, data json.RawMessage) {
	ev := proto.Event{
		EventID:   ulidgen.New(),
		Kind:      kind,
		SessionID: a.opts.ID,
		TS:        a.opts.Now(),
		Data:      data,
	}
	a.opts.Hub.Broadcast(a.opts.ID, ev)
	a.opts.Logger.Info("event.broadcast", slog.String("kind", kind))
}

func copyStrMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// InjectControlConn is exposed for adapters wiring the cc package's accept loop
// to the actor; production wiring uses ControlServer.Listen with a callback.
func (a *actor) InjectControlConn(conn ControlConn) {
	a.mailbox <- mboxItem{kind: mboxControlConn, controlConn: &controlConnItem{conn: conn}}
}

func (a *actor) markError(reason string) {
	a.mu.Lock()
	a.summary.Status = "error"
	a.lastError = reason
	a.mu.Unlock()
	body, _ := json.Marshal(map[string]string{"reason": reason})
	a.broadcast(proto.EventSessionError, body)
}

func (a *actor) setContainerID(id string) {
	a.mu.Lock()
	a.containerID = id
	a.mu.Unlock()
}

func (a *actor) applyMCPStatus(statuses map[string]string, failures []proto.MCPUnreachableData) {
	a.mu.Lock()
	a.mcpStatus = copyStrMap(statuses)
	a.mcpFailures = append(a.mcpFailures[:0], failures...)
	a.mu.Unlock()
}

// emitDeferredAttachEvents fires the one-shot mcp.unreachable + mcp.skipped
// events that accumulated before any client was attached. Called by
// snapshotEvent right after the snapshot is sent.
func (a *actor) emitDeferredAttachEvents() {
	a.mu.Lock()
	mcpFirst := !a.mcpFailuresEmitted
	a.mcpFailuresEmitted = true
	failures := append([]proto.MCPUnreachableData(nil), a.mcpFailures...)
	skillFirst := !a.skillCollisionsEmitted
	a.skillCollisionsEmitted = true
	collisions := append([]string(nil), a.skillCollisions...)
	a.mu.Unlock()
	if mcpFirst {
		for _, f := range failures {
			a.broadcast(proto.EventMCPUnreachable, mustJSON(f))
		}
	}
	if skillFirst {
		for _, name := range collisions {
			a.broadcast(proto.EventSkillCollision, mustJSON(proto.SkillCollisionData{
				Name:      name,
				Overrides: "builtin",
			}))
		}
	}
}

// notifyMCPSkipped is called by the manager during MCP rendering for the
// `agentd.greet` payload — entries whose transport/kind are unknown to this
// build are dropped and reported here.
func (a *actor) notifyMCPSkipped(skipped []proto.MCPSkippedData) {
	for _, sk := range skipped {
		a.broadcast(proto.EventMCPSkipped, mustJSON(sk))
	}
}

func (a *actor) renderedMCPs() []mcp.Entry {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return append([]mcp.Entry(nil), a.opts.ResolvedMCPs...)
}

func (a *actor) sendGreet() {
	entries := a.renderedMCPs()
	render := mcp.Render(mcp.RenderInputs{
		Entries: entries,
		Secrets: mcp.Secrets{GitHubPAT: a.greetSecrets().GitHubPAT},
	})
	skipped := make([]proto.MCPSkippedData, 0, len(render.Skipped))
	for _, s := range render.Skipped {
		skipped = append(skipped, proto.MCPSkippedData{Name: s.Name, Transport: s.Transport, Kind: s.Kind, Reason: s.Reason})
	}
	a.notifyMCPSkipped(skipped)
	payload := map[string]any{
		"session_id": a.opts.ID,
		"model":      a.summary.Model,
		"mcps":       render.Configs,
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sendControlLocked(AgentdGreet, mustJSON(payload))
}

type greetSecretsView struct {
	GitHubPAT string
}

func (a *actor) greetSecrets() greetSecretsView {
	return greetSecretsView{GitHubPAT: a.opts.GitHubPAT}
}
