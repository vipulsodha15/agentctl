package sm

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
)

func newTestManager(t *testing.T) (Manager, *fakeControl) {
	t.Helper()
	dir := t.TempDir()
	fc := newFakeControl()
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100 * time.Millisecond,
	})
	return mgr, fc
}

func TestCreateSendInOrder(t *testing.T) {
	mgr, fc := newTestManager(t)
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "t"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := mgr.Attach(ctx, r.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)

	conn := fc.attach(t, r.SessionID, mgr)

	sendRes, err := mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "hello", ClientID: "cli-1"})
	if err != nil {
		t.Fatal(err)
	}
	if sendRes.Queued {
		t.Fatalf("expected first message not queued")
	}
	mustEvent(t, stream, proto.EventUserMessage)
	mustEvent(t, stream, proto.EventTurnStart)
	if got := conn.expect(t, AgentdMessage); got == "" {
		t.Fatal("control.send AgentdMessage missing")
	}
}

func TestQueueWhenInFlight(t *testing.T) {
	mgr, fc := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "q"})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	conn := fc.attach(t, r.SessionID, mgr)

	if _, err := mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "first"}); err != nil {
		t.Fatal(err)
	}
	mustEvent(t, stream, proto.EventUserMessage)
	mustEvent(t, stream, proto.EventTurnStart)
	conn.expect(t, AgentdMessage)

	res, err := mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "second"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Queued || res.QueueDepth != 1 {
		t.Fatalf("expected queued depth=1, got %+v", res)
	}
	ev := mustEvent(t, stream, proto.EventQueueDepth)
	var d proto.QueueDepthData
	_ = json.Unmarshal(ev.Data, &d)
	if d.Depth != 1 {
		t.Fatalf("queue.depth=%d", d.Depth)
	}

	// Now end the in-flight turn — the queued message should fire automatically.
	conn.feedRuntimeEvent(t, r.SessionID, proto.EventTurnEnd, json.RawMessage(`{"turn_id":"x","status":"ok"}`))
	mustEvent(t, stream, proto.EventTurnEnd)
	mustEvent(t, stream, proto.EventQueueDepth)
	mustEvent(t, stream, proto.EventUserMessage)
	mustEvent(t, stream, proto.EventTurnStart)
}

func TestInterruptMidTurn(t *testing.T) {
	mgr, fc := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "i"})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	conn := fc.attach(t, r.SessionID, mgr)

	_, _ = mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "long task"})
	mustEvent(t, stream, proto.EventUserMessage)
	mustEvent(t, stream, proto.EventTurnStart)
	conn.expect(t, AgentdMessage)

	// Queue one and interrupt with clear_queue.
	_, _ = mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "queued"})
	mustEvent(t, stream, proto.EventQueueDepth)

	res, err := mgr.Interrupt(ctx, r.SessionID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Interrupted || res.ClearedQueueDepth != 1 {
		t.Fatalf("interrupt result=%+v", res)
	}
	conn.expect(t, AgentdInterrupt)
	mustEvent(t, stream, proto.EventQueueDepth)
	conn.feedRuntimeEvent(t, r.SessionID, proto.EventTurnCancelled, json.RawMessage(`{"turn_id":"x","reason":"user"}`))
	mustEvent(t, stream, proto.EventTurnCancelled)
}

func TestInterruptWithoutInFlight(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)

	if _, err := mgr.Interrupt(ctx, r.SessionID, false); err == nil {
		t.Fatalf("expected error from interrupt with no in-flight turn")
	}
}

func TestAttachMidTurnSnapshotsFirst(t *testing.T) {
	mgr, fc := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "snap"})
	conn := fc.attach(t, r.SessionID, mgr)

	first, _ := mgr.Attach(ctx, r.SessionID)
	defer first.Close()
	mustEvent(t, first, proto.EventSessionSnapshot)

	_, _ = mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "go"})
	mustEvent(t, first, proto.EventUserMessage)
	mustEvent(t, first, proto.EventTurnStart)
	conn.expect(t, AgentdMessage)

	// Now a second client attaches mid-turn — first event must be snapshot.
	second, _ := mgr.Attach(ctx, r.SessionID)
	defer second.Close()
	ev := mustEvent(t, second, proto.EventSessionSnapshot)
	var d proto.SessionSnapshotData
	_ = json.Unmarshal(ev.Data, &d)
	if d.InFlight == "" {
		t.Fatalf("expected snapshot to carry in_flight turn id")
	}
}

func TestQueueGrowsBeyondOne(t *testing.T) {
	mgr, fc := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "many"})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	conn := fc.attach(t, r.SessionID, mgr)

	_, _ = mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "a"})
	mustEvent(t, stream, proto.EventUserMessage)
	mustEvent(t, stream, proto.EventTurnStart)
	conn.expect(t, AgentdMessage)
	for i := 0; i < 5; i++ {
		res, err := mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "queued"})
		if err != nil {
			t.Fatal(err)
		}
		if !res.Queued || res.QueueDepth != i+1 {
			t.Fatalf("expected queued depth=%d got %+v", i+1, res)
		}
		mustEvent(t, stream, proto.EventQueueDepth)
	}
}

func TestIdempotencyDoesNotRequireStore(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{Name: "idem"})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	res1, err := mgr.Send(ctx, SendRequest{SessionID: r.SessionID, Content: "x", IdempotencyKey: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Idempotent {
		t.Fatalf("first call should not be idempotent")
	}
}

func TestProvisionOrderCreateListenStart(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeControl()
	cm := newFakeContainerManager()
	fc.bound = cm
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Containers:      cm,
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		ImageID:         "sha256:abc",
		SnapshotTimeout: 100 * time.Millisecond,
	})
	ctx := context.Background()
	res, err := mgr.Create(ctx, CreateRequest{Name: "p"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if res.SessionID == "" {
		t.Fatal("expected session id")
	}
	cm.mu.Lock()
	calls := append([]string(nil), cm.calls...)
	cm.mu.Unlock()
	fc.mu.Lock()
	listened := fc.listened
	fc.mu.Unlock()
	if len(calls) != 2 || calls[0] != "create" || calls[1] != "start" {
		t.Fatalf("unexpected cm call order: %v", calls)
	}
	if !listened {
		t.Fatal("control listen not invoked")
	}
	// Listen must run between create and start.
	if cm.startSeenListen != true {
		t.Fatal("expected listen to be called before start")
	}
}

func TestProvisionTearsDownOnStartFailure(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeControl()
	cm := newFakeContainerManager()
	fc.bound = cm
	cm.startErr = errFakeClosed
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Containers:      cm,
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		ImageID:         "sha256:abc",
		SnapshotTimeout: 100 * time.Millisecond,
	})
	ctx := context.Background()
	if _, err := mgr.Create(ctx, CreateRequest{Name: "p"}); err == nil {
		t.Fatal("expected start error to surface")
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()
	gotStop, gotRemove := false, false
	for _, c := range cm.calls {
		if c == "stop" {
			gotStop = true
		}
		if c == "remove" {
			gotRemove = true
		}
	}
	if !gotStop || !gotRemove {
		t.Fatalf("expected stop+remove on failure, got calls=%v", cm.calls)
	}
}

type fakeContainerManager struct {
	mu              sync.Mutex
	calls           []string
	startErr        error
	createErr       error
	listenSeen      bool
	startSeenListen bool
}

func newFakeContainerManager() *fakeContainerManager {
	return &fakeContainerManager{}
}

func (f *fakeContainerManager) Create(_ context.Context, spec ContainerSpec) (ContainerHandle, error) {
	f.mu.Lock()
	f.calls = append(f.calls, "create")
	f.mu.Unlock()
	if f.createErr != nil {
		return ContainerHandle{}, f.createErr
	}
	return ContainerHandle{ID: "c-" + spec.SessionID, Image: spec.ImageID}, nil
}

func (f *fakeContainerManager) Start(_ context.Context, _ string) error {
	f.mu.Lock()
	f.startSeenListen = f.listenSeen
	f.calls = append(f.calls, "start")
	err := f.startErr
	f.mu.Unlock()
	return err
}

func (f *fakeContainerManager) Stop(_ context.Context, _ string, _ time.Duration) error {
	f.mu.Lock()
	f.calls = append(f.calls, "stop")
	f.mu.Unlock()
	return nil
}

func (f *fakeContainerManager) Remove(_ context.Context, _ string, _ bool) error {
	f.mu.Lock()
	f.calls = append(f.calls, "remove")
	f.mu.Unlock()
	return nil
}

func TestTerminate(t *testing.T) {
	mgr, _ := newTestManager(t)
	ctx := context.Background()
	r, _ := mgr.Create(ctx, CreateRequest{})
	stream, _ := mgr.Attach(ctx, r.SessionID)
	defer stream.Close()
	mustEvent(t, stream, proto.EventSessionSnapshot)
	if err := mgr.Terminate(ctx, r.SessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.Get(ctx, r.SessionID); err == nil {
		t.Fatal("expected session to be gone after terminate")
	}
}

func mustEvent(t *testing.T, stream Stream, kind string) proto.Event {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		done := make(chan struct{})
		var ev proto.Event
		var ok bool
		go func() {
			ev, ok, _ = stream.Recv()
			close(done)
		}()
		select {
		case <-done:
			if !ok {
				t.Fatalf("stream ended waiting for %s", kind)
			}
			if ev.Kind == kind {
				return ev
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for %s", kind)
		}
	}
	t.Fatalf("never saw %s", kind)
	return proto.Event{}
}

type fakeControl struct {
	mu       sync.Mutex
	handlers map[string]ControlHandler
	conns    map[string]*fakeConn
	listened bool
	bound    *fakeContainerManager
}

func newFakeControl() *fakeControl {
	return &fakeControl{handlers: map[string]ControlHandler{}, conns: map[string]*fakeConn{}}
}

func (f *fakeControl) Listen(sessionID, _, _ string, h ControlHandler) error {
	f.mu.Lock()
	f.handlers[sessionID] = h
	f.listened = true
	bound := f.bound
	f.mu.Unlock()
	if bound != nil {
		bound.mu.Lock()
		bound.listenSeen = true
		bound.mu.Unlock()
	}
	return nil
}

func (f *fakeControl) Stop(sessionID string) error {
	f.mu.Lock()
	delete(f.handlers, sessionID)
	c := f.conns[sessionID]
	delete(f.conns, sessionID)
	f.mu.Unlock()
	if c != nil {
		_ = c.Close()
	}
	return nil
}

func (f *fakeControl) attach(t *testing.T, sessionID string, mgr Manager) *fakeConn {
	t.Helper()
	conn := newFakeConn()
	f.mu.Lock()
	f.conns[sessionID] = conn
	f.mu.Unlock()
	mm := mgr.(*manager)
	a := mm.actorFor(sessionID)
	if a == nil {
		t.Fatalf("no actor for %s", sessionID)
	}
	a.InjectControlConn(conn)
	return conn
}

type fakeConn struct {
	mu       sync.Mutex
	raw      chan ControlFrame
	filtered chan ControlFrame
	in       chan ControlFrame
	closed   bool
}

func newFakeConn() *fakeConn {
	c := &fakeConn{
		raw:      make(chan ControlFrame, 64),
		filtered: make(chan ControlFrame, 64),
		in:       make(chan ControlFrame, 32),
	}
	go c.demux()
	return c
}

// demux pulls frames written by the actor; it auto-replies to snapshot
// requests (since the shim doesn't exist in unit tests) and forwards every
// other frame to the consumer-visible channel.
func (c *fakeConn) demux() {
	for fr := range c.raw {
		if fr.Kind == AgentdSnapshotRequest {
			var meta struct {
				RequestID string `json:"request_id"`
			}
			_ = json.Unmarshal(fr.Data, &meta)
			reply := ControlFrame{
				V: 1, Kind: RuntimeSnapshot, TS: time.Now().UTC(),
				Data: json.RawMessage(`{"request_id":"` + meta.RequestID + `","messages":[]}`),
			}
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				c.in <- reply
			}
			continue
		}
		c.filtered <- fr
	}
}

func (c *fakeConn) Send(fr ControlFrame) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()
	c.raw <- fr
	return nil
}

func (c *fakeConn) Recv() (ControlFrame, error) {
	fr, ok := <-c.in
	if !ok {
		return ControlFrame{}, errFakeClosed
	}
	return fr, nil
}

func (c *fakeConn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.in)
		close(c.raw)
	}
	c.mu.Unlock()
	return nil
}

func (c *fakeConn) expect(t *testing.T, kind string) string {
	t.Helper()
	for {
		select {
		case fr := <-c.filtered:
			if fr.Kind == kind {
				return fr.Kind
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for control frame %s", kind)
			return ""
		}
	}
}

func (c *fakeConn) feedRuntimeEvent(t *testing.T, sessionID, kind string, data json.RawMessage) {
	t.Helper()
	inner, _ := json.Marshal(RuntimeEventData{Kind: kind, Data: data})
	c.in <- ControlFrame{V: 1, Kind: RuntimeEvent, TS: time.Now().UTC(), Data: inner}
}

var errFakeClosed = &errString{"fake conn closed"}

type errString struct{ s string }

func (e *errString) Error() string { return e.s }
