package sm

import (
	"context"
	"errors"
	"testing"

	"github.com/agentctl/agentctl/internal/fan"
	"github.com/agentctl/agentctl/internal/proto"
)

// newCatalogManager builds a manager wired with a fixed provider catalog
// so UpdateModel's validation step has something to reject against. Mirrors
// newTestManager but with the catalog closure set. The variadic shape lets
// callers pass per-provider model lists; the common single-provider case is
// the first map entry.
func newCatalogManager(t *testing.T, modelsByProvider map[string][]string) (Manager, *fakeControl) {
	t.Helper()
	dir := t.TempDir()
	fc := newFakeControl()
	// Deep-copy so the closure sees a stable snapshot even if callers mutate.
	snap := make(map[string][]string, len(modelsByProvider))
	for p, models := range modelsByProvider {
		snap[p] = append([]string(nil), models...)
	}
	cat := ProviderCatalog{ModelsByProvider: snap}
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100_000_000, // 100ms
		ProviderCatalog: func() ProviderCatalog { return cat },
	})
	return mgr, fc
}

// TestUpdateModelValidatesAgainstCatalog covers the happy path and the
// model-not-in-catalog rejection. The session is created without a
// catalog-known model on purpose: UpdateModel must rely on the catalog's
// allowlist (the create-side validation is separate per ADR 0003).
func TestUpdateModelValidatesAgainstCatalog(t *testing.T) {
	mgr, fc := newCatalogManager(t, map[string][]string{
		"anthropic": {"claude-sonnet-4-6", "claude-opus-4-7"},
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "m", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Need the actor to have a control conn so the agentd.set_model frame
	// has somewhere to land. We don't assert on dispatch here — that's the
	// next test — but the manager mustn't park forever waiting on it.
	conn := fc.attach(t, r.SessionID, mgr)
	_ = conn

	// Happy path: a model in the catalog swaps the summary.
	sum, err := mgr.UpdateModel(ctx, r.SessionID, "claude-opus-4-7")
	if err != nil {
		t.Fatalf("update happy: %v", err)
	}
	if sum.Model != "claude-opus-4-7" {
		t.Errorf("summary.Model = %q, want claude-opus-4-7", sum.Model)
	}

	// Rejection: an unknown model returns ErrModelInvalid (which the
	// websrv handler maps to bad_request and the socksrv handler also
	// maps to bad_request).
	if _, err := mgr.UpdateModel(ctx, r.SessionID, "some-other-provider-model"); !errors.Is(err, ErrModelInvalid) {
		t.Fatalf("expected ErrModelInvalid for cross-provider model, got %v", err)
	}

	// Unknown session id is still the canonical 404.
	if _, err := mgr.UpdateModel(ctx, "sess_does_not_exist", "claude-opus-4-7"); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("expected ErrSessionNotFound, got %v", err)
	}
}

// TestUpdateModelSendsControlFrame asserts that a successful UpdateModel
// dispatches agentd.set_model on the control channel carrying the new
// model id — the shim relies on that frame to swap its driver client.
func TestUpdateModelSendsControlFrame(t *testing.T) {
	mgr, fc := newCatalogManager(t, map[string][]string{
		"anthropic": {"claude-sonnet-4-6", "claude-opus-4-7"},
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "ctrl", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	conn := fc.attach(t, r.SessionID, mgr)

	if _, err := mgr.UpdateModel(ctx, r.SessionID, "claude-opus-4-7"); err != nil {
		t.Fatalf("update: %v", err)
	}
	got := conn.expect(t, AgentdSetModel)
	if got == "" {
		t.Fatal("expected agentd.set_model on the control conn")
	}
}

// TestUpdateModelNoOpWhenUnchanged confirms a no-op swap doesn't churn the
// control channel — repeatedly setting the same model is a UX-level idempotent
// (the /model command echoes "already on …" rather than burning a frame).
func TestUpdateModelNoOpWhenUnchanged(t *testing.T) {
	mgr, fc := newCatalogManager(t, map[string][]string{
		"anthropic": {"claude-sonnet-4-6", "claude-opus-4-7"},
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "noop", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	conn := fc.attach(t, r.SessionID, mgr)
	// Drain any frames the attach handshake produced so the assertion
	// below is unambiguous.
	drainFiltered(conn)

	sum, err := mgr.UpdateModel(ctx, r.SessionID, "claude-sonnet-4-6")
	if err != nil {
		t.Fatalf("noop update: %v", err)
	}
	if sum.Model != "claude-sonnet-4-6" {
		t.Errorf("summary.Model after noop = %q", sum.Model)
	}
	// Wait a small window — if a frame leaks through it'll arrive promptly.
	// We don't want to use expect() (which blocks 2s); poll once.
	select {
	case fr := <-conn.filtered:
		if fr.Kind == AgentdSetModel {
			t.Errorf("no-op UpdateModel sent an agentd.set_model frame")
		}
	default:
		// Good — no frame queued.
	}
}

// TestUpdateModelRejectsEmptyWithNoCatalog covers the minimal-setup path.
// When no catalog is wired we still reject empty model id so "send the body
// but leave model unset" produces a clear error instead of clobbering the
// session's current value.
func TestUpdateModelRejectsEmptyWithNoCatalog(t *testing.T) {
	dir := t.TempDir()
	fc := newFakeControl()
	mgr := New(Options{
		SessionsDir:     dir,
		Hub:             fan.NewHub(),
		Control:         fc,
		DefaultModel:    "claude-sonnet-4-6",
		SnapshotTimeout: 100_000_000,
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "empty", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fc.attach(t, r.SessionID, mgr)
	if _, err := mgr.UpdateModel(ctx, r.SessionID, ""); !errors.Is(err, ErrModelInvalid) {
		t.Errorf("expected ErrModelInvalid for empty model, got %v", err)
	}
}

// drainFiltered drops any pending frames from the test-conn channel without
// blocking. Used after attach() because the snapshot handshake's RuntimeReady
// can produce intermediate frames we don't care about.
func drainFiltered(c *fakeConn) {
	for {
		select {
		case <-c.filtered:
		default:
			return
		}
	}
}

// TestProviderCatalogHasModel doubles as a sanity check on the catalog's
// empty-string handling and per-provider scoping so the validation contract
// is explicit.
func TestProviderCatalogHasModel(t *testing.T) {
	cat := ProviderCatalog{ModelsByProvider: map[string][]string{
		"anthropic": {"claude-sonnet-4-6"},
		"openai":    {"gpt-5.5"},
	}}
	if !cat.HasModel("anthropic", "claude-sonnet-4-6") {
		t.Error("expected catalog to recognize claude-sonnet-4-6 under anthropic")
	}
	if cat.HasModel("anthropic", "gpt-5.5") {
		t.Error("cross-provider model (gpt-5.5 under anthropic) must not validate")
	}
	if cat.HasModel("openai", "claude-sonnet-4-6") {
		t.Error("cross-provider model (claude-sonnet-4-6 under openai) must not validate")
	}
	if cat.HasModel("", "claude-sonnet-4-6") {
		t.Error("empty provider must never validate")
	}
	if cat.HasModel("anthropic", "") {
		t.Error("empty model id must never validate")
	}
	if cat.HasModel("anthropic", "claude-opus-4-7") {
		t.Error("unknown model id must not validate")
	}
	if cat.HasModel("unknown-provider", "claude-sonnet-4-6") {
		t.Error("unknown provider must not validate any model")
	}
}

// TestUpdateModelRejectsCrossProviderSwitch enforces ADR 0020 §1: the
// session's immutable `provider` field constrains which models are valid
// for UpdateModel. A Claude session must NOT be switchable to a GPT model
// even if both providers are configured and the GPT model is in the
// catalog under its own provider.
func TestUpdateModelRejectsCrossProviderSwitch(t *testing.T) {
	mgr, fc := newCatalogManager(t, map[string][]string{
		"anthropic": {"claude-sonnet-4-6", "claude-opus-4-7"},
		"openai":    {"gpt-5.5"},
	})
	ctx := context.Background()
	r, err := mgr.Create(ctx, CreateRequest{Name: "cross", Provider: "anthropic"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = fc.attach(t, r.SessionID, mgr)

	// gpt-5.5 is a real, catalog-known model — but not for this session's
	// provider. The rejection must come from the provider scoping, not
	// from "model not in catalog at all."
	if _, err := mgr.UpdateModel(ctx, r.SessionID, "gpt-5.5"); !errors.Is(err, ErrModelInvalid) {
		t.Fatalf("expected ErrModelInvalid for cross-provider switch, got %v", err)
	}
}

// quiet compile-checks against types referenced in the closures so the
// test file fails fast if proto.SessionSummary's signature drifts.
var _ proto.SessionSummary = proto.SessionSummary{}
