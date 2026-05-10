package sm

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/fan"
)

// diffFakeConn extends fakeConn to also auto-reply to diff/export requests
// with canned chunks. We can't reuse fakeConn directly because its demux
// drops anything other than snapshot requests; here we extend it.
type diffFakeConn struct {
	*fakeConn
	diffPayload  []byte
	diffEndExtra map[string]string
	pushSucceeds bool
	pushOutput   string
	pushErr      string
	dispatchStop chan struct{}
}

func newDiffFakeConn() *diffFakeConn {
	return &diffFakeConn{
		fakeConn:     newFakeConn(),
		diffPayload:  []byte("--- a/x\n+++ b/x\n@@ -1 +1 @@\n-a\n+b\n"),
		pushSucceeds: true,
		pushOutput:   "ok",
		dispatchStop: make(chan struct{}),
	}
}

// hijack swaps the inner demux so we can react to diff frames as well as
// snapshot. We re-wire by swapping the raw->in pipeline.
func (d *diffFakeConn) reroute(t *testing.T) {
	t.Helper()
	go func() {
		for {
			select {
			case <-d.dispatchStop:
				return
			case fr, ok := <-d.fakeConn.filtered:
				if !ok {
					return
				}
				d.handleControlOut(fr)
			}
		}
	}()
}

func (d *diffFakeConn) handleControlOut(fr ControlFrame) {
	switch fr.Kind {
	case AgentdDiffRequest, AgentdExportPatchReq:
		var meta struct {
			RequestID string `json:"request_id"`
			Repo      string `json:"repo"`
		}
		_ = json.Unmarshal(fr.Data, &meta)
		repos := []string{meta.Repo}
		if meta.Repo == "" {
			repos = []string{"alpha"}
		}
		for _, r := range repos {
			data, _ := json.Marshal(map[string]any{
				"request_id": meta.RequestID, "repo": r,
				"data": base64.StdEncoding.EncodeToString(d.diffPayload),
			})
			d.in <- ControlFrame{V: 1, Kind: RuntimeDiffChunk, TS: time.Now().UTC(), Data: data}
			endData, _ := json.Marshal(map[string]any{
				"request_id": meta.RequestID, "repo": r, "exit_code": 0,
			})
			d.in <- ControlFrame{V: 1, Kind: RuntimeDiffEnd, TS: time.Now().UTC(), Data: endData}
		}
		// terminator for "all repos" mode (empty repo).
		if meta.Repo == "" {
			endData, _ := json.Marshal(map[string]any{
				"request_id": meta.RequestID, "repo": "", "exit_code": 0,
			})
			d.in <- ControlFrame{V: 1, Kind: RuntimeDiffEnd, TS: time.Now().UTC(), Data: endData}
		}
	case AgentdExportPushReq:
		var meta struct {
			RequestID string `json:"request_id"`
			Repo      string `json:"repo"`
			Branch    string `json:"branch"`
		}
		_ = json.Unmarshal(fr.Data, &meta)
		body, _ := json.Marshal(map[string]any{
			"request_id": meta.RequestID,
			"repo":       meta.Repo,
			"branch":     meta.Branch,
			"success":    d.pushSucceeds,
			"output":     d.pushOutput,
			"error":      d.pushErr,
		})
		d.in <- ControlFrame{V: 1, Kind: RuntimeExportPushResult, TS: time.Now().UTC(), Data: body}
	}
}

func (d *diffFakeConn) Close() error {
	close(d.dispatchStop)
	return d.fakeConn.Close()
}

func newDiffManager(t *testing.T) (Manager, *diffFakeConn, string) {
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
	r, err := mgr.Create(context.Background(), CreateRequest{Name: "d"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	stream, _ := mgr.Attach(context.Background(), r.SessionID)
	mustEvent(t, stream, "session.snapshot")
	stream.Close()

	dc := newDiffFakeConn()
	fc.mu.Lock()
	fc.conns[r.SessionID] = dc.fakeConn
	fc.mu.Unlock()
	mm := mgr.(*manager)
	a := mm.actorFor(r.SessionID)
	a.InjectControlConn(dc)
	dc.reroute(t)
	waitForControl(t, a)
	return mgr, dc, r.SessionID
}

func waitForControl(t *testing.T, a *actor) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a.mu.Lock()
		ok := a.control != nil
		a.mu.Unlock()
		if ok {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("timeout waiting for control conn to be installed")
}

func TestDiffStreamsAllRepos(t *testing.T) {
	mgr, dc, id := newDiffManager(t)
	defer dc.Close()
	stream, err := mgr.Diff(context.Background(), id, DiffRequest{})
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	defer stream.Close()
	got := []DiffChunk{}
	for {
		ch, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("recv: %v", err)
		}
		got = append(got, ch)
	}
	if len(got) < 2 {
		t.Fatalf("expected at least chunk + end, got %d: %+v", len(got), got)
	}
	if string(got[0].Data) != string(dc.diffPayload) {
		t.Errorf("data mismatch: %q", got[0].Data)
	}
	sawEnd := false
	for _, c := range got {
		if c.End {
			sawEnd = true
		}
	}
	if !sawEnd {
		t.Errorf("never saw End=true chunk")
	}
}

func TestDiffSessionNotFound(t *testing.T) {
	mgr, _ := newTestManager(t)
	if _, err := mgr.Diff(context.Background(), "missing", DiffRequest{}); err == nil {
		t.Fatal("expected ErrSessionNotFound")
	}
}

func TestExportPatchUsesPatchRequestKind(t *testing.T) {
	mgr, dc, id := newDiffManager(t)
	defer dc.Close()
	stream, err := mgr.ExportPatch(context.Background(), id, DiffRequest{Repo: "alpha"})
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	defer stream.Close()
	got := []byte{}
	for {
		ch, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("recv: %v", err)
		}
		got = append(got, ch.Data...)
	}
	if string(got) != string(dc.diffPayload) {
		t.Errorf("data mismatch: %q", got)
	}
}

func TestExportPushSuccess(t *testing.T) {
	mgr, dc, id := newDiffManager(t)
	defer dc.Close()
	res, err := mgr.ExportPush(context.Background(), id, PushRequest{
		Repo: "alpha", Branch: "feat/x", Message: "ci",
	})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if !res.Success {
		t.Errorf("expected success, got %+v", res)
	}
	if res.Branch != "feat/x" {
		t.Errorf("branch: got %q", res.Branch)
	}
}

func TestExportPushFailureSurfacesError(t *testing.T) {
	mgr, dc, id := newDiffManager(t)
	defer dc.Close()
	dc.pushSucceeds = false
	dc.pushErr = "rejected"
	res, err := mgr.ExportPush(context.Background(), id, PushRequest{Repo: "alpha", Branch: "feat/y"})
	if err != nil {
		t.Fatalf("push: %v", err)
	}
	if res.Success || res.Error != "rejected" {
		t.Errorf("expected failure with rejected error, got %+v", res)
	}
}

func TestExportPushBranchRequired(t *testing.T) {
	mgr, dc, id := newDiffManager(t)
	defer dc.Close()
	if _, err := mgr.ExportPush(context.Background(), id, PushRequest{}); err == nil {
		t.Fatal("expected error for empty branch")
	}
}

func TestSessionReposReturnsRepos(t *testing.T) {
	mgr, _ := newTestManager(t)
	r, _ := mgr.Create(context.Background(), CreateRequest{Repos: []string{"https://x/y.git"}})
	repos, err := mgr.SessionRepos(context.Background(), r.SessionID)
	if err != nil {
		t.Fatalf("repos: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("expected 1 repo, got %d", len(repos))
	}
}
