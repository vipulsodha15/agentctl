package cli

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/store"
	"github.com/agentctl/agentctl/internal/update"
)

func runUpdate(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	noCache := fs.Bool("no-cache", false, "pass --no-cache to docker build")
	report := fs.Bool("report", false, "skip the build, just print the staleness report")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl update [--no-cache] [--report]")
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

	if !*report {
		contextPath := config.ExpandHome(cfg.Image.BuildContextPath)
		if _, err := os.Stat(contextPath); err != nil {
			fmt.Fprintf(env.Stderr, "build context missing at %s; re-run install.sh\n", contextPath)
			return ExitEnvironment
		}
		fmt.Fprintf(env.Stdout, "Building %s ...\n", cfg.Image.LocalTag)
		res, err := update.Build(ctx, update.BuildOptions{
			Tag:         cfg.Image.LocalTag,
			ContextPath: contextPath,
			NoCache:     *noCache,
			Output:      env.Stdout,
		})
		if err != nil {
			fmt.Fprintf(env.Stderr, "update: %v\n", err)
			return ExitEnvironment
		}
		previous := cfg.Image.PinnedID
		if previous != "" && previous != res.ImageID {
			cfg.Image.PreviousID = previous
		}
		cfg.Image.PinnedID = res.ImageID
		if err := config.Save(layout.ConfigFile, cfg); err != nil {
			fmt.Fprintf(env.Stderr, "update: save config: %v\n", err)
			return ExitGeneric
		}
		fmt.Fprintf(env.Stdout, "image built: %s\n", res.ImageID)
		if previous != "" && previous != res.ImageID {
			fmt.Fprintf(env.Stdout, "previous: %s (kept for `update --rollback`)\n", previous)
		}
	}
	fmt.Fprintln(env.Stdout, "")
	if err := printSessionStaleness(env, layout.DBFile, cfg.Image.PinnedID); err != nil {
		fmt.Fprintf(env.Stderr, "update: %v\n", err)
		return ExitGeneric
	}
	return ExitOK
}

func printSessionStaleness(env *Env, dbPath, pinned string) error {
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	if err := st.Migrate(); err != nil {
		return err
	}
	rows, err := st.DB().Query(`SELECT id, name, status, image_id FROM sessions ORDER BY created_at`)
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var id, name, status, image string
		if err := rows.Scan(&id, &name, &status, &image); err != nil {
			return err
		}
		marker := ""
		if status != "terminated" && image != pinned {
			marker = "  *stale image; run `agentctl restart` (M4)"
		}
		fmt.Fprintf(env.Stdout, "  %s  %-20s %-10s%s\n", id, name, status, marker)
		count++
	}
	if count == 0 {
		fmt.Fprintln(env.Stdout, "no sessions exist; new sessions will use the pinned image.")
	}
	return rows.Err()
}
