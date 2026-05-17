package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
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
		fmt.Fprintln(env.Stderr, "  docker.reachable, docker.api, image.built, image.build_context,")
		fmt.Fprintln(env.Stderr, "  image.build_context_drift, image.present, skills.builtin, skills.custom,")
		fmt.Fprintln(env.Stderr, "  mcp.registry, secrets.fresh, network.peer_isolation, volumes.disk.")
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
		// image.build_context_drift surfaces as a warning, not a failure,
		// because a stale build context only matters on the next image
		// rebuild — the running install still works. Treat its warn the
		// same as a failure for --fix purposes so a single
		// `doctor --fix` clears it.
		if c.Status != doctor.StatusFail && !(c.Status == doctor.StatusWarn && c.Name == "image.build_context_drift") {
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
		case "image.build_context_drift":
			n, err := syncBuildContextShim(env)
			if err != nil {
				fmt.Fprintf(env.Stderr, "build_context_drift: %v\n", err)
				continue
			}
			fmt.Fprintf(env.Stdout, "  build_context_drift  synced %d shim file(s); run `agentctl update --yes` to rebuild\n", n)
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

// syncBuildContextShim copies <source_url>/image/shim/ over the top of
// the build-context shim at <home>/.local/share/agentctl/image/shim/.
// Returns the number of files written. Files present only in the
// destination (and not the source) are not removed — keeping
// __pycache__ etc. avoids surprising the user, and they don't end up
// in the docker image anyway.
func syncBuildContextShim(env *Env) (int, error) {
	metaPath := env.Layout.InstallMeta
	data, err := os.ReadFile(metaPath)
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", metaPath, err)
	}
	var meta struct {
		SourceURL string `json:"source_url"`
	}
	if err := json.Unmarshal(data, &meta); err != nil {
		return 0, fmt.Errorf("parse %s: %w", metaPath, err)
	}
	if meta.SourceURL == "" {
		return 0, fmt.Errorf("no source_url recorded in %s", metaPath)
	}
	src := filepath.Join(meta.SourceURL, "image", "shim")
	dst := filepath.Join(env.Layout.ImageDir, "shim")
	if _, err := os.Stat(src); err != nil {
		return 0, fmt.Errorf("source shim missing: %w", err)
	}
	if _, err := os.Stat(dst); err != nil {
		return 0, fmt.Errorf("build-context shim missing: %w", err)
	}
	count := 0
	err = filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			if d.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			return err
		}
		if err := copyFile(path, out); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
		count++
		return nil
	})
	if err != nil {
		return count, err
	}
	return count, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
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
