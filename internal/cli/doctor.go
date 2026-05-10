package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/doctor"
	"github.com/agentctl/agentctl/internal/store"
)

func runDoctor(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON for scripting")
	verbose := fs.Bool("verbose", false, "include extended details (e.g. top sessions by volume size)")
	fix := fs.Bool("fix", false, "apply known fixes for failed checks (chmod paths, rebuild image)")
	repairDB := fs.Bool("repair-db", false, "run sqlite VACUUM and verify integrity")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl doctor [--fix] [--repair-db] [--json] [--verbose]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Runs install + connectivity checks against the local agentctl install:")
		fmt.Fprintln(env.Stderr, "  bin.versions, fs.perms, db.integrity, service.active, agentd.health,")
		fmt.Fprintln(env.Stderr, "  docker.reachable, docker.api, image.built, image.build_context, image.present,")
		fmt.Fprintln(env.Stderr, "  skills.builtin, skills.custom, mcp.registry, secrets.fresh,")
		fmt.Fprintln(env.Stderr, "  network.peer_isolation, volumes.disk.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "  --fix         apply known fixes (alias for `agentctl init --repair`).")
		fmt.Fprintln(env.Stderr, "  --repair-db   sqlite VACUUM; aborts if integrity_check still fails.")
		fmt.Fprintln(env.Stderr, "  --json        machine-readable output.")
		fmt.Fprintln(env.Stderr, "  --verbose     show top sessions by volume size and extra detail lines.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Environment:")
		fmt.Fprintln(env.Stderr, "  AGENTCTL_SKIP_ANTHROPIC_VALIDATE=1  skip the Anthropic API probe.")
		fmt.Fprintln(env.Stderr, "  AGENTCTL_SKIP_GITHUB_PAT_CHECK=1    skip the GitHub API probe.")
		fmt.Fprintln(env.Stderr, "")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}

	if *repairDB {
		return runRepairDB(env)
	}

	cfg, _ := config.Load(env.Layout.ConfigFile)
	opts := doctorOptions(env, cfg.Agentd.WebAddr)

	res := doctor.Run(opts)
	if *asJSON {
		_ = json.NewEncoder(env.Stdout).Encode(res)
	} else {
		printDoctor(env, res, *verbose)
	}

	if *fix {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Applying fixes ...")
		if code := applyFixes(ctx, env, res); code != ExitOK {
			return code
		}
		fmt.Fprintln(env.Stdout, "Re-running checks ...")
		fmt.Fprintln(env.Stdout, "")
		res = doctor.Run(opts)
		if *asJSON {
			_ = json.NewEncoder(env.Stdout).Encode(res)
		} else {
			printDoctor(env, res, *verbose)
		}
	}

	if res.HasFailures() {
		_ = os.Setenv("AGENTCTL_DOCTOR_FAILED", firstFailedName(res))
		return ExitRuntime
	}
	return ExitOK
}

func doctorOptions(env *Env, webAddr string) doctor.RunOptions {
	opts := doctor.DefaultPaths(env.Layout.Home)
	opts.ConfigPath = env.Layout.ConfigFile
	opts.SecretsPath = env.Layout.SecretsFile
	opts.WebTokenPath = env.Layout.WebTokenFile
	opts.DBPath = env.Layout.DBFile
	opts.SocketPath = env.Layout.SocketFile
	opts.BuiltinDir = env.Layout.BuiltinSkills
	opts.CustomDir = env.Layout.CustomSkills
	opts.SessionsDir = env.Layout.SessionsDir
	if webAddr != "" {
		opts.WebAddr = webAddr
	}
	return opts
}

func printDoctor(env *Env, res doctor.Result, verbose bool) {
	fmt.Fprintln(env.Stdout, "agentctl doctor")
	for _, c := range res.Checks {
		marker := "ok  "
		switch c.Status {
		case doctor.StatusFail:
			marker = "FAIL"
		case doctor.StatusWarn:
			marker = "warn"
		case doctor.StatusSkip:
			marker = "skip"
		}
		fmt.Fprintf(env.Stdout, "  %-22s %s  %s\n", c.Name, marker, c.Message)
		if c.Detail != "" && (c.Status == doctor.StatusFail || c.Status == doctor.StatusWarn || verbose) {
			fmt.Fprintf(env.Stdout, "                         %s\n", c.Detail)
		}
	}
}

func firstFailedName(res doctor.Result) string {
	for _, c := range res.Checks {
		if c.Status == doctor.StatusFail {
			return c.Name
		}
	}
	return ""
}

func applyFixes(ctx context.Context, env *Env, res doctor.Result) int {
	fixed := 0
	for _, c := range res.Checks {
		if c.Status != doctor.StatusFail {
			continue
		}
		switch c.Name {
		case "fs.perms":
			if err := fixFSPerms(env); err != nil {
				fmt.Fprintf(env.Stderr, "fs.perms: %v\n", err)
				continue
			}
			fmt.Fprintln(env.Stdout, "  fs.perms     restored 0700/0600 on config + data dirs")
			fixed++
		case "image.present", "image.built":
			fmt.Fprintln(env.Stdout, "  image        re-running `agentctl init --repair --skip-docker-check=false`")
			if err := runInitRepair(ctx, env); err != nil {
				fmt.Fprintf(env.Stderr, "image fix: %v\n", err)
				return ExitGeneric
			}
			fixed++
		case "mcp.registry":
			fmt.Fprintln(env.Stdout, "  mcp.registry no automatic fix; edit rows manually")
		case "db.integrity":
			fmt.Fprintln(env.Stdout, "  db.integrity run `agentctl doctor --repair-db`")
		case "secrets.fresh":
			fmt.Fprintln(env.Stdout, "  secrets.fresh run `agentctl init --reset-token anthropic` or `--reset-token github`")
		}
	}
	if fixed == 0 {
		fmt.Fprintln(env.Stdout, "  (nothing to do)")
	}
	return ExitOK
}

func fixFSPerms(env *Env) error {
	dirs := []string{
		env.Layout.ConfigDir,
		env.Layout.DataDir,
		env.Layout.SessionsDir,
	}
	for _, d := range dirs {
		if _, err := os.Stat(d); err == nil {
			if err := os.Chmod(d, 0o700); err != nil {
				return err
			}
		}
	}
	files := []string{
		env.Layout.ConfigFile,
		env.Layout.SecretsFile,
		env.Layout.WebTokenFile,
	}
	for _, f := range files {
		if _, err := os.Stat(f); err == nil {
			if err := os.Chmod(f, 0o600); err != nil {
				return err
			}
		}
	}
	return nil
}

func runInitRepair(ctx context.Context, env *Env) error {
	code := runInit(ctx, env, []string{"--repair"})
	if code != ExitOK {
		return fmt.Errorf("init --repair exited with code %d", code)
	}
	return nil
}

func runRepairDB(env *Env) int {
	dbPath := env.Layout.DBFile
	if _, err := os.Stat(dbPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(env.Stderr, "doctor --repair-db: agentd.db missing at %s\n", dbPath)
			return ExitEnvironment
		}
		fmt.Fprintf(env.Stderr, "doctor --repair-db: %v\n", err)
		return ExitGeneric
	}
	st, err := store.Open(store.Options{Path: dbPath})
	if err != nil {
		fmt.Fprintf(env.Stderr, "doctor --repair-db: open: %v\n", err)
		return ExitGeneric
	}
	defer func() { _ = st.Close() }()
	fmt.Fprintf(env.Stdout, "running VACUUM on %s ...\n", filepath.Base(dbPath))
	if _, err := st.DB().Exec("VACUUM"); err != nil {
		fmt.Fprintf(env.Stderr, "doctor --repair-db: VACUUM failed: %v\n", err)
		return ExitGeneric
	}
	res, err := st.IntegrityCheck()
	if err != nil {
		fmt.Fprintf(env.Stderr, "doctor --repair-db: integrity_check failed: %v\n", err)
		return ExitGeneric
	}
	if res != "ok" {
		fmt.Fprintf(env.Stderr, "doctor --repair-db: integrity_check=%s\n", res)
		fmt.Fprintln(env.Stderr, "DB corruption beyond repair; restore from backup.")
		return ExitRuntime
	}
	fmt.Fprintln(env.Stdout, "VACUUM ok; integrity_check=ok")
	return ExitOK
}
