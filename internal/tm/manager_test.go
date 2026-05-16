package tm

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/ttl"
)

// TestLoadStages_SurfacesProviderAndModelFromSessions verifies the LEFT
// JOIN that brings provider/model from the sessions row into the Stage
// payload — the data path the run-view chip relies on (ADR 0020 §3).
// Without this join the SPA would have to hit /v1/sessions/{id} per
// stage, which is exactly the "buried metadata" failure mode ADR 0020
// §3 calls out.
func TestLoadStages_SurfacesProviderAndModelFromSessions(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "tm.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}

	// Seed two sessions, one per provider — the mixed-provider case the
	// run-view chip is for. Only the columns loadStages touches matter
	// (id, provider, model); the rest of the schema gets default-friendly
	// values so the INSERT respects NOT NULL constraints.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at,
         image_id, volume_path, control_sock_path,
         skills_snapshot_path, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores,
         mcp_set_json, repos_json, session_token, provider)
         VALUES
         ('sess-a','a','running',?, ?, '','','','','', 'claude-opus-4-7', 0, 0, '[]','[]','tok-a','anthropic'),
         ('sess-b','b','running',?, ?, '','','','','', 'gpt-5.5',          0, 0, '[]','[]','tok-b','openai')`,
		now, now, now, now,
	); err != nil {
		t.Fatalf("insert sessions: %v", err)
	}

	// Seed a task with two stages, one bound to each session.
	if _, err := st.DB().Exec(`INSERT INTO tasks
        (task_id, name, assembly_line_name, repo_url, base_sha, source_kind, source_url, issue_md, current_stage_id, status, created_at)
        VALUES ('task-1','t','bug-multi-provider',NULL,NULL,'freeform',NULL,'fix it',NULL,'working',?)`, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO stages
        (stage_id, task_id, position, agent_name, session_id, status, started_at)
        VALUES
        ('stg-1','task-1',1,'bug-investigator','sess-a','done',?),
        ('stg-2','task-1',2,'bug-executor','sess-b','active',?)`, now, now); err != nil {
		t.Fatalf("insert stages: %v", err)
	}

	// A throwaway library is enough — loadStages only calls GetAgent for
	// the colour, and a missing agent is tolerated.
	lib := newEmptyLibrary(t)
	mgr := New(Options{Store: st, Library: lib, Runtime: noopRuntime{}})

	stages, err := mgr.loadStages(context.Background(), "task-1")
	if err != nil {
		t.Fatalf("loadStages: %v", err)
	}
	if len(stages) != 2 {
		t.Fatalf("expected 2 stages, got %d", len(stages))
	}
	if stages[0].Provider != "anthropic" || stages[0].Model != "claude-opus-4-7" {
		t.Errorf("stage 1 join lost: provider=%q model=%q", stages[0].Provider, stages[0].Model)
	}
	if stages[1].Provider != "openai" || stages[1].Model != "gpt-5.5" {
		t.Errorf("stage 2 join lost: provider=%q model=%q", stages[1].Provider, stages[1].Model)
	}
}

func newEmptyLibrary(t *testing.T) *ttl.Library {
	t.Helper()
	dir := t.TempDir()
	libStore, err := store.Open(store.Options{Path: filepath.Join(dir, "lib.db")})
	if err != nil {
		t.Fatalf("library store: %v", err)
	}
	t.Cleanup(func() { _ = libStore.Close() })
	if err := libStore.Migrate(); err != nil {
		t.Fatalf("library migrate: %v", err)
	}
	lib := ttl.New(ttl.Options{DB: libStore.DB()})
	if _, err := ttl.Materialize(context.Background(), libStore.DB()); err != nil {
		t.Fatalf("library materialize: %v", err)
	}
	if _, err := lib.Load(context.Background()); err != nil {
		t.Fatalf("library load: %v", err)
	}
	return lib
}

// TestSpawnStage_PullsProviderModelFromAssemblyLineYAML verifies the
// end-to-end plumbing for mixed-provider built-in lines: spawnStage
// looks up the task's AssemblyLineName in the library, finds the
// matching stage by position, and forwards Provider/Model as
// StageProvider/StageModel on StartStageInput. The bug-multi-provider
// built-in is the headline test fixture for this — its investigator
// pins anthropic, its executor pins openai.
func TestSpawnStage_PullsProviderModelFromAssemblyLineYAML(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "tm.db")})
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.Migrate(); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := ttl.Materialize(context.Background(), st.DB()); err != nil {
		t.Fatalf("ttl.Materialize: %v", err)
	}
	lib := ttl.New(ttl.Options{DB: st.DB()})
	if _, err := lib.Load(context.Background()); err != nil {
		t.Fatalf("lib.Load: %v", err)
	}

	// Capture every StartStageInput spawnStage hands to the runtime so we
	// can assert on the stage-level pins.
	rec := &recordingRuntime{}
	mgr := New(Options{Store: st, Library: lib, Runtime: rec})

	// Build a minimal Task pointing at the built-in mixed-provider line.
	// We're only exercising spawnStage, so the task/stage rows just need
	// to exist; full CreateTask isn't required.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := st.DB().Exec(`INSERT INTO tasks
        (task_id, name, assembly_line_name, repo_url, base_sha, source_kind, source_url, issue_md, current_stage_id, status, created_at)
        VALUES ('task-1','t','bug-multi-provider',NULL,NULL,'freeform',NULL,'fix it',NULL,'working',?)`, now); err != nil {
		t.Fatalf("insert task: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO stages
        (stage_id, task_id, position, agent_name, status, started_at)
        VALUES
        ('stg-1','task-1',1,'bug-investigator','active',?),
        ('stg-2','task-1',2,'bug-planner','pending',?),
        ('stg-3','task-1',3,'bug-executor','pending',?)`, now, now, now); err != nil {
		t.Fatalf("insert stages: %v", err)
	}
	task := &Task{
		ID:               "task-1",
		AssemblyLineName: "bug-multi-provider",
		IssueMD:          "fix it",
		Stages: []Stage{
			{ID: "stg-1", TaskID: "task-1", Position: 1, AgentName: "bug-investigator"},
			{ID: "stg-2", TaskID: "task-1", Position: 2, AgentName: "bug-planner"},
			{ID: "stg-3", TaskID: "task-1", Position: 3, AgentName: "bug-executor"},
		},
	}

	for i := range task.Stages {
		if err := mgr.spawnStage(context.Background(), task, &task.Stages[i], "", ""); err != nil {
			t.Fatalf("spawnStage %d: %v", i+1, err)
		}
	}

	if len(rec.starts) != 3 {
		t.Fatalf("expected 3 StartStage calls, got %d", len(rec.starts))
	}
	if rec.starts[0].StageProvider != "anthropic" {
		t.Errorf("investigator stage: want StageProvider=anthropic got %q", rec.starts[0].StageProvider)
	}
	if rec.starts[1].StageProvider != "" {
		t.Errorf("planner stage carries no provider pin in the builtin; got %q", rec.starts[1].StageProvider)
	}
	if rec.starts[2].StageProvider != "openai" {
		t.Errorf("executor stage: want StageProvider=openai got %q", rec.starts[2].StageProvider)
	}
}

// recordingRuntime captures StartStageInput payloads for assertion. All
// other StageRuntime methods are no-ops — spawnStage tests don't need
// them.
type recordingRuntime struct {
	starts []StartStageInput
	next   int
}

func (r *recordingRuntime) StartStage(_ context.Context, in StartStageInput) (StartStageResult, error) {
	r.starts = append(r.starts, in)
	r.next++
	// Return empty SessionID so spawnStage's UPDATE stages SET session_id=…
	// writes NULL — the stages.session_id FK to sessions(id) is honoured
	// without us having to forge a row for every stub stage.
	return StartStageResult{}, nil
}
func (recordingRuntime) SendUserMessage(context.Context, SendMessageInput) error { return nil }
func (recordingRuntime) Synthesize(context.Context, SendMessageInput) (string, error) {
	return "", nil
}
func (recordingRuntime) IsBusy(string) bool                                { return false }
func (recordingRuntime) EnsureAttached(context.Context, AttachInput) error { return nil }
func (recordingRuntime) StopStage(context.Context, string) error           { return nil }

// noopRuntime satisfies StageRuntime for loadStages tests that never
// actually spawn anything.
type noopRuntime struct{}

func (noopRuntime) StartStage(context.Context, StartStageInput) (StartStageResult, error) {
	return StartStageResult{}, nil
}
func (noopRuntime) SendUserMessage(context.Context, SendMessageInput) error { return nil }
func (noopRuntime) Synthesize(context.Context, SendMessageInput) (string, error) {
	return "", nil
}
func (noopRuntime) IsBusy(string) bool                                { return false }
func (noopRuntime) EnsureAttached(context.Context, AttachInput) error { return nil }
func (noopRuntime) StopStage(context.Context, string) error           { return nil }
