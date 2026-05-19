package sm

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	SessionToken    string
	Store           *store.Store
	Repos           []proto.RepoState
	ResolvedMCPs    []mcp.Entry
	GitHubPAT       string
	SkillCollisions []string
	Usage           UsageRecorder
	SystemPrompt    string
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
	runtimeReady           bool
	terminated             bool
	pendingSnap            map[string]chan ControlFrame
	pendingDiffs           map[string]*pendingDiff
	pendingPush            map[string]chan ControlFrame
	containerID            string
	networkID              string
	networkName            string
	lastError              string
	nextMsgSeq             int64
	// sdkSessionID is the claude-agent-sdk session uuid the shim resumed (or
	// created on first run) for this actor. Persisted to sessions.sdk_session_id
	// so a fresh shim after a daemon restart can resume the same SDK session
	// and keep extending the same JSONL file instead of orphaning it.
	sdkSessionID string
}

func newActor(opts actorOptions) *actor {
	a := &actor{
		opts:            opts,
		mailbox:         make(chan mboxItem, mailboxSize),
		stopCh:          make(chan struct{}),
		summary:         opts.Summary,
		mcpStatus:       map[string]string{},
		skillCollisions: append([]string(nil), opts.SkillCollisions...),
		repos:           opts.Repos,
		pendingSnap:     map[string]chan ControlFrame{},
		pendingDiffs:    map[string]*pendingDiff{},
		pendingPush:     map[string]chan ControlFrame{},
	}
	// Seed the monotonic message-record seq from the DB so reattaches and
	// daemon restarts continue past the last persisted record. Empty/new
	// sessions naturally start at 0.
	if opts.Store != nil {
		var maxSeq sql.NullInt64
		_ = opts.Store.DB().QueryRow(
			`SELECT MAX(seq) FROM messages WHERE session_id = ?`, opts.ID,
		).Scan(&maxSeq)
		if maxSeq.Valid {
			a.nextMsgSeq = maxSeq.Int64 + 1
		}
		// Pick up sdk_session_id so the next shim greet can include it and
		// the SDK resumes the same conversation instead of orphaning the
		// existing JSONL.
		var sid sql.NullString
		_ = opts.Store.DB().QueryRow(
			`SELECT sdk_session_id FROM sessions WHERE id = ?`, opts.ID,
		).Scan(&sid)
		if sid.Valid {
			a.sdkSessionID = sid.String
		}
	}
	return a
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
		var conn ControlConn
		if item.controlConn != nil {
			conn = item.controlConn.conn
		}
		a.handleControlClosed(conn)
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
	// runtimeReady is normally cleared by handleControlClosed when readControl
	// wakes up from the closed conn — but that runs on the next mailbox tick.
	// Clear it inline so a Send arriving right after Stop (e.g. the auto-restart
	// path) sees the consistent "no runtime" state instead of dispatching a
	// frame into a nil control conn.
	a.runtimeReady = false
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
	// Starting a turn requires the shim to have sent RuntimeReady — otherwise
	// the AgentdMessage frame either has no control conn to land on (a.control
	// nil) or hits the shim before it enters its inbound loop, and the message
	// is silently lost. In either case we'd set inFlight without anything
	// running, so the session hangs. Defer to the queue and let the
	// RuntimeReady handler start the first turn.
	if a.inFlight == "" && a.runtimeReady {
		turnID := ulidgen.WithPrefix("turn")
		a.inFlight = turnID
		a.currentTurn = turnID
		a.broadcastLocked(proto.EventUserMessage, mustJSON(proto.UserMessageData{
			MessageID: s.messageID, Content: s.content, ClientID: s.clientID,
		}))
		a.broadcastLocked(proto.EventTurnStart, mustJSON(proto.TurnStartData{
			TurnID: turnID, MessageID: s.messageID, Model: a.summary.Model,
		}))
		// Pass the daemon's turn_id so the shim stamps the same id on every
		// runtime.event it emits for this turn. Without it the shim falls
		// back to message_id (image/shim/__main__.py), which breaks the
		// synth-correlation in tm.SessionRuntime (synthTID is the ULID turn
		// id but assistant.message arrives with TurnID = message_id, so
		// Synthesize hangs and Handoff never advances the stage).
		a.sendControlLocked(AgentdMessage, mustJSON(map[string]any{
			"message_id": s.messageID, "content": s.content, "turn_id": turnID,
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
		containerID, networkID, _ := a.snapshotIDs()
		ctx, cancel := context.WithTimeout(context.Background(), a.opts.ShutdownGrace+5*time.Second)
		if containerID != "" {
			_ = a.opts.Containers.Stop(ctx, containerID, a.opts.ShutdownGrace)
			_ = a.opts.Containers.Remove(ctx, containerID, true)
		}
		if networkID != "" {
			_ = a.opts.Containers.NetworkRemove(ctx, networkID)
		}
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

func (a *actor) handleControlClosed(conn ControlConn) {
	a.mu.Lock()
	// Stop and Restart close the old conn synchronously, then a fresh conn
	// can be wired in via handleControlConn before the old readControl
	// goroutine gets scheduled to enqueue its mboxControlClosed. Without an
	// identity check here, the stale close would wipe out the freshly
	// installed conn and clear runtimeReady — the session would look ready
	// for a moment and then silently drop back to "not ready".
	if conn != nil && a.control != conn {
		a.mu.Unlock()
		return
	}
	a.control = nil
	a.runtimeReady = false
	a.mu.Unlock()
	a.opts.Logger.Info("session.control_disconnected")
}

func (a *actor) readControl(conn ControlConn) {
	for {
		fr, err := conn.Recv()
		if err != nil {
			a.mailbox <- mboxItem{
				kind:        mboxControlClosed,
				controlConn: &controlConnItem{conn: conn},
			}
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
		a.runtimeReady = true
		// Pop the first queued message (if any) so messages typed during
		// container start get a turn now that the shim is reading frames.
		var pending *queuedMessage
		if a.inFlight == "" && len(a.queue) > 0 {
			head := a.queue[0]
			a.queue = a.queue[1:]
			a.summary.QueueDepth = len(a.queue)
			pending = &head
		}
		a.mu.Unlock()
		a.broadcast(proto.EventSessionRunning, mustJSON(map[string]any{}))
		if a.opts.Store != nil {
			_, _ = a.opts.Store.DB().Exec(`UPDATE sessions SET status='running' WHERE id=?`, a.opts.ID)
		}
		if pending != nil {
			a.broadcast(proto.EventQueueDepth, mustJSON(proto.QueueDepthData{Depth: a.queueDepth()}))
			a.startTurnFor(*pending)
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
	case RuntimeSessionID:
		a.handleRuntimeSessionID(fr)
	case RuntimeMessageRecord:
		a.handleMessageRecord(fr)
	case RuntimeDiffChunk:
		a.routeDiffChunk(fr)
	case RuntimeDiffEnd:
		a.routeDiffEnd(fr)
	case RuntimeExportPushResult:
		a.routePushResult(fr)
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
	case proto.EventUsage:
		a.broadcast(inner.Kind, a.persistUsage(inner.Data))
	case proto.EventTurnCancelled:
		a.broadcast(inner.Kind, inner.Data)
		a.completeTurn("cancelled")
	default:
		a.broadcast(inner.Kind, inner.Data)
	}
}

// persistUsage writes the usage row (R10) and returns the same data with the
// recorder-computed `cost_usd` filled in so the broadcast carries the
// canonical price-table number even if the runtime didn't include one. If the
// recorder is absent (tests / no-store mode) the original payload is forwarded
// unchanged.
func (a *actor) persistUsage(data json.RawMessage) json.RawMessage {
	var u proto.UsageData
	if err := json.Unmarshal(data, &u); err != nil {
		a.opts.Logger.Warn("usage.malformed_payload", slog.String("error", err.Error()))
		return data
	}
	if a.opts.Usage == nil {
		return data
	}
	rec := UsageRecord{
		SessionID:        a.opts.ID,
		TurnID:           u.TurnID,
		At:               a.opts.Now(),
		Model:            u.Model,
		InputTokens:      u.InputTokens,
		OutputTokens:     u.OutputTokens,
		CacheReadTokens:  u.CacheReadTokens,
		CacheWriteTokens: u.CacheWriteTokens,
	}
	if rec.Model == "" {
		a.mu.RLock()
		rec.Model = a.summary.Model
		a.mu.RUnlock()
		u.Model = rec.Model
	}
	if err := a.opts.Usage.OnUsage(context.Background(), rec); err != nil {
		a.opts.Logger.Warn("usage.persist_failed",
			slog.String("turn_id", u.TurnID),
			slog.String("error", err.Error()))
	}
	if cost, ok := a.opts.Usage.CostFor(rec); ok {
		u.CostUSD = cost
	}
	out, err := json.Marshal(u)
	if err != nil {
		return data
	}
	return out
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
	// Pass the daemon's turn_id so the shim stamps the same id on every
	// runtime.event it emits for this turn (assistant.delta, assistant.message,
	// tool.call, tool.result, turn.end, ...). Without it the shim falls back
	// to message_id as its turn_id and clients see turn.start.turn_id !=
	// turn.end.turn_id, which breaks turn-lifecycle tracking on the web UI.
	a.sendControlLocked(AgentdMessage, mustJSON(map[string]any{
		"message_id": q.messageID, "content": q.content, "turn_id": turnID,
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

// loadConversationFromStore returns the stored JSONL records for this
// session as a raw JSON array. Returns (nil, nil) when the session has no
// mirrored history yet — the caller should fall back to asking the shim.
func (a *actor) loadConversationFromStore() (json.RawMessage, error) {
	if a.opts.Store == nil {
		return nil, nil
	}
	rows, err := a.opts.Store.DB().Query(
		`SELECT record_json FROM messages WHERE session_id = ? ORDER BY seq`,
		a.opts.ID,
	)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	buf := []byte{'['}
	first := true
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err != nil {
			return nil, err
		}
		if !first {
			buf = append(buf, ',')
		}
		buf = append(buf, rec...)
		first = false
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, nil
	}
	buf = append(buf, ']')
	return buf, nil
}

// loadConversationFromDisk reads the SDK's JSONL history directly from the
// bind-mounted session volume. It exists for two cases the DB mirror does
// not cover:
//
//   - Sessions that pre-date the messages-table mirror (no rows; on-disk
//     JSONL is the only surviving record).
//   - Rehydrated sessions whose shim hasn't been started yet (no live shim
//     to ask via runtime.snapshot, but the JSONL is still on disk).
//
// Returns (nil, nil) when nothing is readable so the caller can keep its
// default empty conversation.
func (a *actor) loadConversationFromDisk() (json.RawMessage, error) {
	if a.opts.SessionDir == "" {
		return nil, nil
	}
	// /work is bind-mounted at <SessionDir>/volume; the SDK encodes cwd
	// "/work" as "-work" under .claude/projects/.
	projectsDir := filepath.Join(a.opts.SessionDir, "volume", ".claude", "projects", "-work")
	a.mu.RLock()
	preferred := a.sdkSessionID
	a.mu.RUnlock()
	path := ""
	if preferred != "" {
		candidate := filepath.Join(projectsDir, preferred+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}
	if path == "" {
		// No (or stale) sdk_session_id — fall back to scanning the
		// projects dir. With SDK resume working this directory holds at
		// most one file, but pre-resume sessions can leave several.
		// Picking the most recently modified one is the best heuristic
		// for "the live conversation."
		entries, err := os.ReadDir(projectsDir)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, nil
			}
			return nil, err
		}
		type cand struct {
			path string
			mod  time.Time
		}
		var found []cand
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			info, ierr := e.Info()
			if ierr != nil {
				continue
			}
			found = append(found, cand{path: filepath.Join(projectsDir, e.Name()), mod: info.ModTime()})
		}
		if len(found) == 0 {
			return nil, nil
		}
		sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
		path = found[0].path
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	buf := []byte{'['}
	first := true
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		// Drop malformed lines so a corrupt record can't poison the array.
		if !json.Valid(line) {
			continue
		}
		if !first {
			buf = append(buf, ',')
		}
		buf = append(buf, line...)
		first = false
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if first {
		return nil, nil
	}
	buf = append(buf, ']')
	return buf, nil
}

// handleRuntimeSessionID persists the SDK-assigned conversation id the shim
// captures the first time it sees a message. Keeping this in the DB is what
// lets a fresh shim (after a daemon restart or container swap) resume the
// same SDK session instead of starting a new one and orphaning the
// existing JSONL.
func (a *actor) handleRuntimeSessionID(fr ControlFrame) {
	var payload struct {
		SDKSessionID string `json:"sdk_session_id"`
	}
	if err := json.Unmarshal(fr.Data, &payload); err != nil || payload.SDKSessionID == "" {
		a.opts.Logger.Warn("control.malformed_session_id", slog.String("session_id", a.opts.ID))
		return
	}
	a.mu.Lock()
	changed := a.sdkSessionID != payload.SDKSessionID
	a.sdkSessionID = payload.SDKSessionID
	a.mu.Unlock()
	if !changed || a.opts.Store == nil {
		return
	}
	if _, err := a.opts.Store.DB().Exec(
		`UPDATE sessions SET sdk_session_id = ? WHERE id = ?`,
		payload.SDKSessionID, a.opts.ID,
	); err != nil {
		a.opts.Logger.Warn("control.session_id_persist_failed",
			slog.String("session_id", a.opts.ID),
			slog.String("error", err.Error()))
	}
}

// handleMessageRecord persists a single SDK JSONL record the shim shipped
// after a turn finished. The dedup index on (session_id, record_uuid) makes
// re-sends after a shim reconnect a no-op, so we don't have to track which
// records the daemon has already seen on the shim side.
func (a *actor) handleMessageRecord(fr ControlFrame) {
	if a.opts.Store == nil {
		return
	}
	var payload struct {
		Record json.RawMessage `json:"record"`
	}
	if err := json.Unmarshal(fr.Data, &payload); err != nil || len(payload.Record) == 0 {
		a.opts.Logger.Warn("control.malformed_message_record", slog.String("session_id", a.opts.ID))
		return
	}
	var uuidProbe struct {
		UUID string `json:"uuid"`
	}
	_ = json.Unmarshal(payload.Record, &uuidProbe)
	var uuidArg any
	if uuidProbe.UUID != "" {
		uuidArg = uuidProbe.UUID
	}
	seq := a.nextMsgSeq
	res, err := a.opts.Store.DB().Exec(
		`INSERT OR IGNORE INTO messages (session_id, seq, received_at, record_uuid, record_json)
		 VALUES (?, ?, ?, ?, ?)`,
		a.opts.ID, seq, a.opts.Now().Format(time.RFC3339Nano), uuidArg, string(payload.Record),
	)
	if err != nil {
		a.opts.Logger.Warn("control.message_record_insert_failed",
			slog.String("session_id", a.opts.ID), slog.String("error", err.Error()))
		return
	}
	if n, _ := res.RowsAffected(); n > 0 {
		a.nextMsgSeq = seq + 1
	}
}

func (a *actor) snapshotEvent(ctx context.Context) (*proto.Event, error) {
	conv := json.RawMessage(`[]`)
	// SQLite is the source of truth: the shim mirrors each JSONL line via
	// runtime.message_record, so history survives the container being
	// stopped, swept on idle, or moved to another node. We only ask the
	// shim if the DB has nothing yet — that path is the back-compat seam
	// for sessions that pre-date this mirror (no rows; shim still alive).
	if records, err := a.loadConversationFromStore(); err == nil && records != nil {
		conv = records
	} else {
		a.mu.Lock()
		conn := a.control
		a.mu.Unlock()
		if conn == nil {
			// No live shim to ask. Fall back to the SDK JSONL the
			// previous container left on the bind-mounted volume so
			// rehydrated and pre-mirror sessions still show their
			// history on attach.
			if records, derr := a.loadConversationFromDisk(); derr == nil && records != nil {
				conv = records
			} else if derr != nil {
				a.opts.Logger.Warn("snapshot.disk_fallback_failed",
					slog.String("session_id", a.opts.ID),
					slog.String("error", derr.Error()))
			}
		}
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
				var payload struct {
					Messages json.RawMessage `json:"messages"`
				}
				if err := json.Unmarshal(fr.Data, &payload); err == nil && len(payload.Messages) > 0 {
					conv = payload.Messages
				}
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
		NetworkID:      a.networkID,
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

func (a *actor) markRestarting(imageID string) {
	a.mu.Lock()
	a.summary.Status = "starting"
	a.summary.ImageID = imageID
	a.containerID = ""
	a.lastError = ""
	if a.control != nil {
		_ = a.control.Close()
		a.control = nil
	}
	a.mu.Unlock()
	a.broadcast(proto.EventSessionStarting, mustJSON(map[string]any{"phase": "restart"}))
}

func (a *actor) setContainerID(id string) {
	a.mu.Lock()
	a.containerID = id
	a.mu.Unlock()
}

func (a *actor) setNetwork(id, name string) {
	a.mu.Lock()
	a.networkID = id
	a.networkName = name
	a.mu.Unlock()
}

func (a *actor) snapshotIDs() (containerID, networkID, networkName string) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.containerID, a.networkID, a.networkName
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

// setModel swaps the runtime's model id mid-session (ADR 0020 §2).
//
// The daemon side of UpdateModel is responsible for validation against the
// session's provider catalog; this method assumes the model has already
// cleared that check and is correct for the session's provider. It updates
// the in-actor SessionSummary so subsequent turn.start frames carry the
// new model id, and forwards agentd.set_model to the shim. The control
// frame is fire-and-forget — there is no dedicated runtime.set_model_ack;
// per the api.md §4.3 entry, the next runtime.event (usage / assistant.*)
// carrying the new model id is the implicit ack.
//
// The new SessionSummary is returned so callers can echo the canonical
// value back to the user without a second snapshotSummary round-trip.
func (a *actor) setModel(model string) proto.SessionSummary {
	a.mu.Lock()
	prev := a.summary.Model
	a.summary.Model = model
	a.summary.LastActivityAt = a.opts.Now()
	if a.control != nil {
		a.sendControlLocked(AgentdSetModel, mustJSON(map[string]any{
			"model": model,
		}))
	}
	out := a.summary
	a.mu.Unlock()
	a.opts.Logger.Info("session.model.swapped",
		slog.String("from", prev),
		slog.String("to", model))
	return out
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
	a.mu.Lock()
	defer a.mu.Unlock()
	payload := map[string]any{
		"session_id": a.opts.ID,
		"model":      a.summary.Model,
		"mcps":       render.Configs,
	}
	// provider selects which driver the shim instantiates (claude vs codex —
	// ADR 0020 §7). Always send when known so a daemon that has been
	// upgraded can drive the new Codex driver immediately; the shim treats
	// a missing field as "anthropic" for one release for backward
	// compatibility with sessions started before the daemon upgrade
	// (CODEX_PROVIDER_PLAN §1.3).
	if a.summary.Provider != "" {
		payload["provider"] = a.summary.Provider
	}
	// Pass the previously captured SDK session id so the shim resumes the
	// same conversation (extending its existing JSONL) instead of forking
	// a fresh one after a daemon restart or container swap.
	if a.sdkSessionID != "" {
		payload["sdk_session_id"] = a.sdkSessionID
	}
	// system_prompt is set by tm.SessionRuntime so each task-chat stage runs
	// under its agent's prompt; omitted for normal sessions so the SDK keeps
	// the default Claude Code prompt.
	if a.opts.SystemPrompt != "" {
		payload["system_prompt"] = a.opts.SystemPrompt
	}
	a.sendControlLocked(AgentdGreet, mustJSON(payload))
}

type greetSecretsView struct {
	GitHubPAT string
}

func (a *actor) greetSecrets() greetSecretsView {
	return greetSecretsView{GitHubPAT: a.opts.GitHubPAT}
}
