package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/update"
)

func runUpdate(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	noCache := fs.Bool("no-cache", false, "pass --no-cache to docker build")
	report := fs.Bool("report", false, "skip the build, just print the staleness report")
	rollback := fs.Bool("rollback", false, "swap pinned/previous image IDs and re-tag")
	restartStopped := fs.Bool("restart-stopped", false, "restart every stopped session after the rebuild")
	yes := fs.Bool("yes", false, "do not prompt for confirmation")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl update [--no-cache] [--report] [--rollback] [--restart-stopped] [--yes]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Re-runs `docker build` against the local build context, repins config.toml")
		fmt.Fprintln(env.Stderr, "[image].pinned_id, and (optionally) restarts stopped sessions onto the new id.")
		fmt.Fprintln(env.Stderr, "Running sessions keep their existing image until they are restarted.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Examples:")
		fmt.Fprintln(env.Stderr, "  agentctl update --report             # show staleness without rebuilding")
		fmt.Fprintln(env.Stderr, "  agentctl update --no-cache --yes     # full rebuild, no prompts")
		fmt.Fprintln(env.Stderr, "  agentctl update --rollback           # swap pinned <-> previous image id")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	layout := env.Layout
	cfg, err := config.Load(layout.ConfigFile)
	if err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitEnvironment
	}

	switch {
	case *rollback:
		return runUpdateRollback(env, &cfg)
	case *report:
		return runUpdateReport(env, cfg)
	default:
		return runUpdateBuild(ctx, env, &cfg, *noCache, *restartStopped, *yes)
	}
}

func runUpdateBuild(ctx context.Context, env *Env, cfg *config.Config, noCache, restartStopped, yes bool) int {
	contextPath := config.ExpandHome(cfg.Image.BuildContextPath)
	if _, err := os.Stat(contextPath); err != nil {
		fmt.Fprintf(env.Stderr, "build context missing at %s; re-run install.sh\n", contextPath)
		return ExitEnvironment
	}
	fmt.Fprintf(env.Stdout, "Building %s ...\n", cfg.Image.LocalTag)
	res, err := update.Build(ctx, update.BuildOptions{
		Tag:         cfg.Image.LocalTag,
		ContextPath: contextPath,
		NoCache:     noCache,
		Output:      env.Stdout,
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitEnvironment
	}
	previous := cfg.Image.PinnedID
	if previous == res.ImageID {
		fmt.Fprintf(env.Stdout, "no change: %s already pinned\n", previous)
	} else {
		if previous != "" {
			cfg.Image.PreviousID = previous
		}
		cfg.Image.PinnedID = res.ImageID
		if err := config.Save(env.Layout.ConfigFile, *cfg); err != nil {
			fmt.Fprintf(env.Stderr, "update: save config: %v\n", err)
			return ExitGeneric
		}
		fmt.Fprintf(env.Stdout, "image built: %s\n", res.ImageID)
		if previous != "" {
			fmt.Fprintf(env.Stdout, "previous: %s (kept for `update --rollback`)\n", previous)
		}
	}
	fmt.Fprintln(env.Stdout, "")
	if err := printSessionStaleness(env, env.Layout.DBFile, cfg.Image.PinnedID); err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitGeneric
	}
	if restartStopped {
		return runRestartStoppedSessions(env, env.Layout.DBFile, yes)
	}
	return ExitOK
}

func runUpdateReport(env *Env, cfg config.Config) int {
	if cfg.Image.PinnedID == "" {
		fmt.Fprintln(env.Stdout, "no image pinned; run `agentctl init` or `agentctl update`.")
	}
	if err := printSessionStaleness(env, env.Layout.DBFile, cfg.Image.PinnedID); err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitGeneric
	}
	return ExitOK
}

func runUpdateRollback(env *Env, cfg *config.Config) int {
	if cfg.Image.PreviousID == "" {
		fmt.Fprintln(env.Stderr, "update --rollback: no previous image recorded")
		return ExitGeneric
	}
	previous := cfg.Image.PreviousID
	current := cfg.Image.PinnedID
	if _, err := exec.LookPath("docker"); err == nil {
		out, err := exec.Command("docker", "tag", previous, cfg.Image.LocalTag).CombinedOutput()
		if err != nil {
			fmt.Fprintf(env.Stderr, "update --rollback: docker tag failed: %v: %s\n", err, strings.TrimSpace(string(out)))
			return ExitEnvironment
		}
	} else {
		fmt.Fprintf(env.Stderr, "update --rollback: docker not on PATH; cannot re-tag %s\n", cfg.Image.LocalTag)
		return ExitEnvironment
	}
	cfg.Image.PinnedID = previous
	cfg.Image.PreviousID = current
	if err := config.Save(env.Layout.ConfigFile, *cfg); err != nil {
		fmt.Fprintf(env.Stderr, "update --rollback: save config: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "rolled back: pinned=%s previous=%s\n", previous, current)
	fmt.Fprintln(env.Stdout, "")
	if err := printSessionStaleness(env, env.Layout.DBFile, cfg.Image.PinnedID); err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitGeneric
	}
	return ExitOK
}

func runRestartStoppedSessions(env *Env, dbPath string, yes bool) int {
	rows, err := loadStaleSessions(dbPath)
	if err != nil {
		fmt.Fprintf(env.Stderr, "update --restart-stopped: %v\n", err)
		return ExitGeneric
	}
	stopped := make([]staleRow, 0, len(rows))
	for _, r := range rows {
		if r.Status == "stopped" {
			stopped = append(stopped, r)
		}
	}
	if len(stopped) == 0 {
		fmt.Fprintln(env.Stdout, "no stopped sessions to restart")
		return ExitOK
	}
	if !yes {
		fmt.Fprintf(env.Stderr, "restart %d stopped session(s)? [y/N] ", len(stopped))
		var ans string
		_, _ = fmt.Fscanln(env.Stdin, &ans)
		if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ans)), "y") {
			fmt.Fprintln(env.Stderr, "aborted")
			return ExitOK
		}
	}
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "update --restart-stopped: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	failures := 0
	for _, r := range stopped {
		var resp proto.RestartSessionResponse
		if err := c.Call(proto.OpRestartSession, proto.RestartSessionRequest{SessionID: r.ID}, &resp, 60*time.Second); err != nil {
			fmt.Fprintf(env.Stderr, "  %s: %v\n", r.ID, err)
			failures++
			continue
		}
		fmt.Fprintf(env.Stdout, "  %s: %s on %s\n", resp.SessionID, resp.Status, truncImage(resp.ImageID))
	}
	if failures > 0 {
		return ExitGeneric
	}
	return ExitOK
}

type staleRow struct {
	ID      string
	Name    string
	Status  string
	ImageID string
}

func loadStaleSessions(dbPath string) ([]staleRow, error) {
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		return nil, err
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		return nil, err
	}
	rows, err := st.DB().Query(`SELECT id, name, status, image_id FROM sessions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []staleRow{}
	for rows.Next() {
		var r staleRow
		if err := rows.Scan(&r.ID, &r.Name, &r.Status, &r.ImageID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func printSessionStaleness(env *Env, dbPath, pinned string) error {
	rows, err := loadStaleSessions(dbPath)
	if err != nil {
		return err
	}
	return writeStalenessReport(env.Stdout, rows, pinned)
}

func writeStalenessReport(w io.Writer, rows []staleRow, pinned string) error {
	if len(rows) == 0 {
		fmt.Fprintln(w, "no sessions exist; new sessions will use the pinned image.")
		return nil
	}
	fmt.Fprintf(w, "%d session%s exist:\n", len(rows), plural(len(rows)))
	for _, r := range rows {
		switch r.Status {
		case "terminated":
			fmt.Fprintf(w, "  %s  %-20q %-10s            (no action)\n", r.ID, r.Name, r.Status)
		case "running":
			suffix := " (already on new image)"
			if r.ImageID != pinned {
				suffix = " (will pick up new image after next restart)"
			}
			fmt.Fprintf(w, "  %s  %-20q %-10s on %s%s\n", r.ID, r.Name, r.Status, truncImage(r.ImageID), suffix)
		case "stopped":
			suffix := " (already on new image)"
			if r.ImageID != pinned {
				suffix = " (will pick up new image on next resume)"
			}
			fmt.Fprintf(w, "  %s  %-20q %-10s on %s%s\n", r.ID, r.Name, r.Status, truncImage(r.ImageID), suffix)
		default:
			fmt.Fprintf(w, "  %s  %-20q %-10s on %s\n", r.ID, r.Name, r.Status, truncImage(r.ImageID))
		}
	}
	return nil
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func truncImage(id string) string {
	if len(id) <= 19 {
		return id
	}
	return id[:19] + "…"
}
