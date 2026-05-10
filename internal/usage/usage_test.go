package usage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/store"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(store.Options{Path: filepath.Join(dir, "agentd.db")})
	if err != nil {
		t.Fatalf("store open: %v", err)
	}
	if err := st.Migrate(); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at,
         image_id, volume_path, control_sock_path, skills_snapshot_path, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
        VALUES ('sess_a', 'A', 'running', '2026-05-09T00:00:00Z', '2026-05-09T00:00:00Z',
                'sha256:img', '/tmp/v', '/tmp/c.sock', '/tmp/s', 'h',
                'claude-sonnet-4-6', 0, 0, '[]', '[]', 'tok')`); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	if _, err := st.DB().Exec(`INSERT INTO sessions
        (id, name, status, created_at, last_activity_at,
         image_id, volume_path, control_sock_path, skills_snapshot_path, skills_snapshot_hash,
         model, mem_limit_bytes, cpu_limit_cores, mcp_set_json, repos_json, session_token)
        VALUES ('sess_b', 'B', 'terminated', '2026-05-08T00:00:00Z', '2026-05-08T01:00:00Z',
                'sha256:img', '/tmp/v', '/tmp/c.sock', '/tmp/s', 'h',
                'claude-opus-4-7', 0, 0, '[]', '[]', 'tok')`); err != nil {
		t.Fatalf("seed session b: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func defaultPricing() config.PricingTables {
	return config.Default().Pricing.Tables
}

func TestRecorderInsertsCost(t *testing.T) {
	st := newTestStore(t)
	rec := New(Options{Store: st, Pricing: defaultPricing()})

	ev := UsageEvent{
		SessionID:        "sess_a",
		TurnID:           "turn_1",
		At:               time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC),
		Model:            "claude-sonnet-4-6",
		InputTokens:      1_000_000,
		OutputTokens:     500_000,
		CacheReadTokens:  100_000,
		CacheWriteTokens: 50_000,
	}
	if err := rec.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("OnUsage: %v", err)
	}

	var (
		model   string
		costN   sql.NullFloat64
		ver     sql.NullInt64
		inT, oT int64
	)
	row := st.DB().QueryRow(`SELECT model, cost_usd, price_table_version, input_tokens, output_tokens
                              FROM usage WHERE session_id='sess_a' AND turn_id='turn_1'`)
	if err := row.Scan(&model, &costN, &ver, &inT, &oT); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if model != "claude-sonnet-4-6" {
		t.Errorf("model=%q", model)
	}
	if !costN.Valid {
		t.Fatalf("cost_usd null; expected computed")
	}
	// (1e6*3 + 5e5*15 + 1e5*0.30 + 5e4*3.75) / 1e6 = (3e6 + 7.5e6 + 3e4 + 1.875e5) / 1e6 = 10.7178
	wantCost := (1_000_000.0*3 + 500_000.0*15 + 100_000.0*0.30 + 50_000.0*3.75) / 1_000_000.0
	if abs(costN.Float64-wantCost) > 1e-9 {
		t.Errorf("cost_usd=%v want %v", costN.Float64, wantCost)
	}
	if !ver.Valid || ver.Int64 != 1 {
		t.Errorf("price_table_version=%v want 1", ver)
	}
	if inT != 1_000_000 || oT != 500_000 {
		t.Errorf("token counts wrong")
	}
}

func TestRecorderUnknownModelKeepsRow(t *testing.T) {
	st := newTestStore(t)
	rec := New(Options{Store: st, Pricing: defaultPricing()})

	ev := UsageEvent{
		SessionID:    "sess_a",
		TurnID:       "turn_unknown",
		Model:        "claude-experimental-9000",
		InputTokens:  10,
		OutputTokens: 20,
	}
	if err := rec.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("OnUsage: %v", err)
	}
	row := st.DB().QueryRow(`SELECT cost_usd, model FROM usage WHERE turn_id='turn_unknown'`)
	var (
		costN sql.NullFloat64
		model string
	)
	if err := row.Scan(&costN, &model); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if costN.Valid {
		t.Errorf("expected NULL cost for unknown model, got %v", costN.Float64)
	}
	if model != "claude-experimental-9000" {
		t.Errorf("model=%q", model)
	}
}

func TestRecorderIdempotentOnUniqueViolation(t *testing.T) {
	st := newTestStore(t)
	rec := New(Options{Store: st, Pricing: defaultPricing()})
	ev := UsageEvent{
		SessionID: "sess_a", TurnID: "turn_dup", Model: "claude-sonnet-4-6",
		InputTokens: 100, OutputTokens: 100,
	}
	if err := rec.OnUsage(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := rec.OnUsage(context.Background(), ev); err != nil {
		t.Fatalf("duplicate insert should be ignored: %v", err)
	}
	var n int
	_ = st.DB().QueryRow(`SELECT COUNT(*) FROM usage WHERE turn_id='turn_dup'`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
}

func TestPerSessionAndRange(t *testing.T) {
	st := newTestStore(t)
	rec := New(Options{Store: st, Pricing: defaultPricing()})
	ctx := context.Background()
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	if err := rec.OnUsage(ctx, UsageEvent{
		SessionID: "sess_a", TurnID: "t1", At: now, Model: "claude-sonnet-4-6",
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rec.OnUsage(ctx, UsageEvent{
		SessionID: "sess_a", TurnID: "t2", At: now.Add(time.Minute), Model: "claude-sonnet-4-6",
		InputTokens: 2_000_000, OutputTokens: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}
	if err := rec.OnUsage(ctx, UsageEvent{
		SessionID: "sess_b", TurnID: "t3", At: now.Add(-25 * time.Hour), Model: "claude-opus-4-7",
		InputTokens: 1_000_000, OutputTokens: 1_000_000,
	}); err != nil {
		t.Fatal(err)
	}

	per, err := rec.PerSession(ctx, "sess_a")
	if err != nil {
		t.Fatalf("PerSession: %v", err)
	}
	if per.Turns != 2 {
		t.Errorf("turns=%d want 2", per.Turns)
	}
	if len(per.Timeline) != 2 {
		t.Errorf("timeline len=%d", len(per.Timeline))
	}
	if len(per.ByModel) != 1 || per.ByModel[0].Model != "claude-sonnet-4-6" {
		t.Errorf("by_model %#v", per.ByModel)
	}

	// Range covering only "today" — sess_b's row at -25h is excluded.
	rng, err := rec.Range(ctx, now.Add(-1*time.Hour), now.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("Range: %v", err)
	}
	if rng.Turns != 2 {
		t.Errorf("range turns=%d want 2 (sess_a only)", rng.Turns)
	}
	if len(rng.BySession) != 1 || rng.BySession[0].SessionID != "sess_a" {
		t.Errorf("by_session %#v", rng.BySession)
	}

	// Wider range pulls both.
	wide, err := rec.Range(ctx, now.Add(-48*time.Hour), now.Add(time.Hour), "")
	if err != nil {
		t.Fatalf("Range wide: %v", err)
	}
	if wide.Turns != 3 || len(wide.BySession) != 2 {
		t.Errorf("wide range got turns=%d sessions=%d", wide.Turns, len(wide.BySession))
	}

	// Session filter.
	filt, err := rec.Range(ctx, now.Add(-48*time.Hour), now.Add(time.Hour), "sess_b")
	if err != nil {
		t.Fatalf("Range filter: %v", err)
	}
	if filt.Turns != 1 || len(filt.BySession) != 1 || filt.BySession[0].SessionID != "sess_b" {
		t.Errorf("filter got %#v", filt)
	}

	totals, err := rec.RunningTotals(ctx, []string{"sess_a", "sess_b", "missing"})
	if err != nil {
		t.Fatalf("RunningTotals: %v", err)
	}
	if _, ok := totals["sess_a"]; !ok {
		t.Errorf("running total for sess_a missing")
	}
	if _, ok := totals["missing"]; ok {
		t.Errorf("running total leaked unknown session")
	}
}

func TestPerSessionEmpty(t *testing.T) {
	st := newTestStore(t)
	rec := New(Options{Store: st, Pricing: defaultPricing()})
	per, err := rec.PerSession(context.Background(), "sess_a")
	if err != nil {
		t.Fatalf("PerSession: %v", err)
	}
	if per.Turns != 0 || len(per.Timeline) != 0 || per.CostUSD != 0 {
		t.Errorf("expected empty totals, got %#v", per)
	}
}

func TestParseRange(t *testing.T) {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		spec      string
		wantStart time.Time
		wantEnd   time.Time
		expectErr bool
		approxNow bool
	}{
		{spec: "today", wantStart: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC), wantEnd: now},
		{spec: "7d", wantStart: now.Add(-7 * 24 * time.Hour), wantEnd: now},
		{spec: "1h", wantStart: now.Add(-1 * time.Hour), wantEnd: now},
		{spec: "2026-05-01..2026-05-09",
			wantStart: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		},
		{spec: "2026-05-09",
			wantStart: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
			wantEnd:   time.Date(2026, 5, 10, 0, 0, 0, 0, time.UTC),
		},
		{spec: "garbage", expectErr: true},
		{spec: "..2026-05-09", expectErr: true},
	}
	for _, c := range cases {
		t.Run(c.spec, func(t *testing.T) {
			s, e, err := ParseRange(c.spec, now)
			if c.expectErr {
				if err == nil {
					t.Fatalf("expected error for %q", c.spec)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !s.Equal(c.wantStart) || !e.Equal(c.wantEnd) {
				t.Errorf("got [%s, %s) want [%s, %s)", s, e, c.wantStart, c.wantEnd)
			}
		})
	}
	if _, _, err := ParseRange("", now); err != nil {
		t.Errorf("empty spec should default to 7d: %v", err)
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
