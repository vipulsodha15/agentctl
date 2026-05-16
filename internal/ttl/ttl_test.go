package ttl

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// newTestLib spins up an in-memory sqlite with the agents + assembly_lines tables
// and returns a library backed by it.
func newTestLib(t *testing.T) (*Library, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	for _, q := range []string{
		`CREATE TABLE agents (
            name TEXT PRIMARY KEY,
            source TEXT NOT NULL CHECK (source IN ('builtin','custom')),
            yaml_body TEXT NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )`,
		`CREATE TABLE assembly_lines (
            name TEXT PRIMARY KEY,
            source TEXT NOT NULL CHECK (source IN ('builtin','custom')),
            yaml_body TEXT NOT NULL,
            created_at TEXT NOT NULL,
            updated_at TEXT NOT NULL
        )`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	return New(Options{DB: db}), db
}

// TestMaterializeIsIdempotent verifies that Materialize can run twice in a
// row without producing duplicate rows — a stateless container will call it
// on every boot, so this is on the hot path.
func TestMaterializeIsIdempotent(t *testing.T) {
	_, db := newTestLib(t)
	ctx := context.Background()
	first, err := Materialize(ctx, db)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first == 0 {
		t.Fatalf("expected first materialize to write rows, got 0")
	}
	second, err := Materialize(ctx, db)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	// The second run reissues UPSERTs (intentional, so binary upgrades can
	// ship fixed prompts), but every row's source is still 'builtin' and
	// rowcount stays equal to the number of embedded files.
	if second != first {
		t.Fatalf("second materialize affected a different number of rows: first=%d second=%d", first, second)
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM agents WHERE source='builtin'`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n == 0 {
		t.Fatalf("no builtin agents after materialize")
	}
}

// TestMaterializeDoesNotClobberCustom checks the core durability promise:
// a custom row created via PutAgent survives Materialize calls that follow
// it.
func TestMaterializeDoesNotClobberCustom(t *testing.T) {
	lib, db := newTestLib(t)
	ctx := context.Background()
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// Forge a custom row with the same name as a built-in. Because Put
	// refuses to overwrite a builtin, we have to author a fresh name.
	customYAML := []byte(`name: my-reviewer
description: My custom code reviewer.
colour: amber
prompt: |
  You are a careful reviewer.`)
	saved, err := lib.PutAgent(ctx, Agent{}, customYAML)
	if err != nil {
		t.Fatalf("PutAgent: %v", err)
	}
	if saved.Source != SourceCustom {
		t.Fatalf("expected source=custom, got %q", saved.Source)
	}
	// Now re-materialize. The custom row must not be touched.
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := lib.GetAgent("my-reviewer")
	if err != nil {
		t.Fatalf("GetAgent after materialize: %v", err)
	}
	if got.Source != SourceCustom {
		t.Fatalf("custom row was clobbered: source=%q", got.Source)
	}
	if got.Description != "My custom code reviewer." {
		t.Fatalf("custom row body was clobbered: %q", got.Description)
	}
}

// TestPutAgentRefusesBuiltinOverwrite — saving over a built-in name is
// rejected; the user is expected to fork it under a new name.
func TestPutAgentRefusesBuiltinOverwrite(t *testing.T) {
	lib, db := newTestLib(t)
	ctx := context.Background()
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	body := []byte(`name: bug-investigator
description: Hijacked!
colour: red
prompt: |
  I'm not the real investigator.`)
	_, err := lib.PutAgent(ctx, Agent{}, body)
	if !errors.Is(err, ErrBuiltinReadOnly) {
		t.Fatalf("expected ErrBuiltinReadOnly, got %v", err)
	}
	// And the original is still intact.
	got, _ := lib.GetAgent("bug-investigator")
	if got.Source != SourceBuiltin {
		t.Fatalf("source flipped: %q", got.Source)
	}
	if !strings.Contains(got.Prompt, "bug investigator") {
		t.Fatalf("prompt was overwritten: %q", got.Prompt[:50])
	}
}

// TestRemoveAgentReferencedByAssemblyLine — assembly lines hold the unique
// referential integrity we care about; ErrInUse must be returned.
func TestRemoveAgentReferencedByAssemblyLine(t *testing.T) {
	lib, db := newTestLib(t)
	ctx := context.Background()
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// Author a custom agent + an assembly line that uses it.
	if _, err := lib.PutAgent(ctx, Agent{}, []byte(`name: my-agent
description: x
colour: blue
prompt: hi`)); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.PutAssemblyLine(ctx, AssemblyLine{}, []byte(`name: my-flow
description: x
stages:
  - agent: my-agent`)); err != nil {
		t.Fatal(err)
	}
	if err := lib.RemoveAgent(ctx, "my-agent"); !errors.Is(err, ErrInUse) {
		t.Fatalf("expected ErrInUse, got %v", err)
	}
	// Remove the assembly line first, then the agent should remove cleanly.
	if err := lib.RemoveAssemblyLine(ctx, "my-flow"); err != nil {
		t.Fatal(err)
	}
	if err := lib.RemoveAgent(ctx, "my-agent"); err != nil {
		t.Fatalf("RemoveAgent after RemoveAssemblyLine: %v", err)
	}
}

// TestMaterializeShipsMixedProviderBuiltin — the bug-multi-provider line
// from ADR 0020 §3 is the headline example for orchestration across
// providers, so it lives in the image as a built-in. This test guards
// that it's actually wired up (the YAML embed glob fires for it) and
// that the per-stage provider pins survive the parse + materialise +
// reload round trip — without them, the line's whole point is lost.
func TestMaterializeShipsMixedProviderBuiltin(t *testing.T) {
	lib, db := newTestLib(t)
	ctx := context.Background()
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	wf, err := lib.GetAssemblyLine("bug-multi-provider")
	if err != nil {
		t.Fatalf("bug-multi-provider not materialized: %v", err)
	}
	if wf.Source != SourceBuiltin {
		t.Fatalf("expected source=builtin, got %q", wf.Source)
	}
	if len(wf.Stages) < 2 {
		t.Fatalf("expected >=2 stages, got %d", len(wf.Stages))
	}
	// Locate the two pinned stages by agent name so the test doesn't break
	// if the planner stage gains or loses a pin in the future.
	got := map[string]string{}
	for _, s := range wf.Stages {
		got[s.Agent] = s.Provider
	}
	if got["bug-investigator"] != "anthropic" {
		t.Errorf("bug-investigator stage should pin provider=anthropic, got %q", got["bug-investigator"])
	}
	if got["bug-executor"] != "openai" {
		t.Errorf("bug-executor stage should pin provider=openai, got %q", got["bug-executor"])
	}
}

// TestParseAssemblyLineYAMLStageProviderModel — stage-level provider and
// model overrides survive YAML round-trips. This is the wire format the
// run-view chip relies on; if YAML deserialisation drops the field,
// mixed-provider lines silently collapse to the resolver default.
func TestParseAssemblyLineYAMLStageProviderModel(t *testing.T) {
	body := []byte(`name: mixed
description: stage pins
stages:
  - agent: a-investigator
    provider: anthropic
    model: claude-opus-4-7
  - agent: a-executor
    provider: openai
`)
	wf, err := ParseAssemblyLineYAML(body)
	if err != nil {
		t.Fatalf("ParseAssemblyLineYAML: %v", err)
	}
	if len(wf.Stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(wf.Stages))
	}
	if wf.Stages[0].Provider != "anthropic" || wf.Stages[0].Model != "claude-opus-4-7" {
		t.Errorf("stage 0 pins lost: %+v", wf.Stages[0])
	}
	if wf.Stages[1].Provider != "openai" {
		t.Errorf("stage 1 provider lost: %+v", wf.Stages[1])
	}
	if wf.Stages[1].Model != "" {
		t.Errorf("stage 1 model should be empty when YAML omits it; got %q", wf.Stages[1].Model)
	}
}

// TestPutAssemblyLineValidatesAgents — assembly lines that reference an
// unknown agent are rejected at Put time, not silently loaded with a flag.
func TestPutAssemblyLineValidatesAgents(t *testing.T) {
	lib, db := newTestLib(t)
	ctx := context.Background()
	if _, err := Materialize(ctx, db); err != nil {
		t.Fatal(err)
	}
	if _, err := lib.Load(ctx); err != nil {
		t.Fatal(err)
	}
	_, err := lib.PutAssemblyLine(ctx, AssemblyLine{}, []byte(`name: ghost-flow
description: references nothing
stages:
  - agent: does-not-exist`))
	if !errors.Is(err, ErrValidation) {
		t.Fatalf("expected ErrValidation, got %v", err)
	}
}
