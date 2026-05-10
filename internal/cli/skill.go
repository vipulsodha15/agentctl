package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"text/tabwriter"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/skills"
)

func runSkill(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		skillUsage(env)
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return runSkillList(ctx, env, rest)
	case "new":
		return runSkillNew(ctx, env, rest)
	case "add":
		return runSkillAdd(ctx, env, rest)
	case "edit":
		return runSkillEdit(ctx, env, rest)
	case "remove", "rm":
		return runSkillRemove(ctx, env, rest)
	case "validate":
		return runSkillValidate(ctx, env, rest)
	case "show":
		return runSkillShow(ctx, env, rest)
	case "export":
		return runSkillExport(ctx, env, rest)
	case "import":
		return runSkillImport(ctx, env, rest)
	case "-h", "--help", "help":
		skillUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl skill: unknown subcommand %q\n\n", sub)
		skillUsage(env)
		return ExitUsage
	}
}

func skillUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl skill <subcommand> [flags]")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands: list new add edit remove validate show export import")
}

func runSkillList(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("skill list", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	wantBuiltin := fs.Bool("builtin", false, "include only built-in skills")
	wantCustom := fs.Bool("custom", false, "include only custom skills")
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill list [--builtin] [--custom] [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.ListInstalledSkillsResponse
	if err := c.Call(proto.OpListInstalledSkills, proto.ListInstalledSkillsRequest{}, &resp, 5*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "skill list: %v\n", err)
		return ExitGeneric
	}
	filtered := make([]proto.SkillEntry, 0, len(resp.Skills))
	for _, s := range resp.Skills {
		if *wantBuiltin && s.Source != skills.SourceBuiltin {
			continue
		}
		if *wantCustom && s.Source != skills.SourceCustom {
			continue
		}
		filtered = append(filtered, s)
	}
	if *asJSON {
		out, _ := json.MarshalIndent(filtered, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tOVERRIDES\tDESCRIPTION")
	for _, s := range filtered {
		ov := ""
		if s.Overrides {
			ov = "yes"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Name, s.Source, ov, truncate(s.Description, 80))
	}
	_ = tw.Flush()
	return ExitOK
}

func runSkillNew(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill new <name>")
		return ExitUsage
	}
	name := args[0]
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: env.Layout.BuiltinSkills,
		CustomDir:  env.Layout.CustomSkills,
	})
	path, err := mgr.Scaffold(name)
	if err != nil {
		fmt.Fprintf(env.Stderr, "skill new: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "scaffolded %s at %s\n", name, path)
	return ExitOK
}

func runSkillAdd(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("skill add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "overwrite existing skill of the same name")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill add <path-or-tarball> [--force]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	src, _ := filepath.Abs(fs.Arg(0))
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.AddSkillResponse
	if err := c.Call(proto.OpAddSkill, proto.AddSkillRequest{Path: src, Force: *force}, &resp, 30*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "skill add: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "added skill %s at %s\n", resp.Name, resp.Path)
	return ExitOK
}

func runSkillEdit(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill edit <name>")
		return ExitUsage
	}
	name := args[0]
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: env.Layout.BuiltinSkills,
		CustomDir:  env.Layout.CustomSkills,
	})
	s, err := mgr.Show(name)
	if err != nil {
		fmt.Fprintf(env.Stderr, "skill edit: %v\n", err)
		return ExitGeneric
	}
	if s.Source == skills.SourceBuiltin {
		fmt.Fprintf(env.Stderr, "skill edit: %q is a built-in; copy via `skill add` first or shadow with --force\n", name)
		return ExitSessionState
	}
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command(editor, s.Path)
	cmd.Stdin = env.Stdin
	cmd.Stdout = env.Stdout
	cmd.Stderr = env.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(env.Stderr, "skill edit: %v\n", err)
		return ExitGeneric
	}
	res, _ := mgr.Validate(skills.ValidateSource{Name: name, Path: s.Path})
	if !res.OK {
		fmt.Fprintf(env.Stderr, "skill edit: validation failed: %v\n", res.Issues)
		return ExitGeneric
	}
	return ExitOK
}

func runSkillRemove(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill remove <name>")
		return ExitUsage
	}
	name := args[0]
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.RemoveSkillResponse
	if err := c.Call(proto.OpRemoveSkill, proto.RemoveSkillRequest{Name: name}, &resp, 5*time.Second); err != nil {
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) {
			if apiErr.Code == proto.ErrPreconditionFailed {
				fmt.Fprintf(env.Stderr, "skill remove: %q is a built-in; uninstall by re-running install.sh\n", name)
				return ExitSessionState
			}
			if apiErr.Code == proto.ErrNotFound {
				fmt.Fprintf(env.Stderr, "skill remove: %q not found\n", name)
				return ExitSessionState
			}
		}
		fmt.Fprintf(env.Stderr, "skill remove: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "removed %s\n", name)
	return ExitOK
}

func runSkillValidate(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill validate <name-or-path>")
		return ExitUsage
	}
	target := args[0]
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: env.Layout.BuiltinSkills,
		CustomDir:  env.Layout.CustomSkills,
	})
	src := skills.ValidateSource{Name: target}
	if info, err := os.Stat(target); err == nil && info.IsDir() {
		abs, _ := filepath.Abs(target)
		src = skills.ValidateSource{Path: abs}
	}
	res, err := mgr.Validate(src)
	if err != nil {
		fmt.Fprintf(env.Stderr, "skill validate: %v\n", err)
		return ExitGeneric
	}
	if res.OK {
		fmt.Fprintf(env.Stdout, "ok %s — %s\n", res.Name, res.Description)
		return ExitOK
	}
	fmt.Fprintf(env.Stderr, "validate failed: %s\n", res.Name)
	for _, iss := range res.Issues {
		fmt.Fprintf(env.Stderr, "  - %s\n", iss)
	}
	return ExitGeneric
}

func runSkillShow(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill show <name>")
		return ExitUsage
	}
	name := args[0]
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: env.Layout.BuiltinSkills,
		CustomDir:  env.Layout.CustomSkills,
	})
	s, err := mgr.Show(name)
	if err != nil {
		if errors.Is(err, skills.ErrNotFound) {
			fmt.Fprintf(env.Stderr, "skill show: %q not found\n", name)
			return ExitSessionState
		}
		fmt.Fprintf(env.Stderr, "skill show: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "name:        %s\n", s.Name)
	fmt.Fprintf(env.Stdout, "source:      %s\n", s.Source)
	fmt.Fprintf(env.Stdout, "path:        %s\n", s.Path)
	fmt.Fprintf(env.Stdout, "overrides:   %t\n", s.Overrides)
	fmt.Fprintf(env.Stdout, "description: %s\n", s.Description)
	if entries, err := os.ReadDir(s.Path); err == nil {
		fmt.Fprintln(env.Stdout, "files:")
		for _, e := range entries {
			fmt.Fprintf(env.Stdout, "  %s\n", e.Name())
		}
	}
	return ExitOK
}

func runSkillExport(_ context.Context, env *Env, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill export <name> [path]")
		return ExitUsage
	}
	name := args[0]
	dest := name + ".tar.gz"
	if len(args) >= 2 {
		dest = args[1]
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.ExportSkillResponse
	if err := c.Call(proto.OpExportSkill, proto.ExportSkillRequest{Name: name}, &resp, 10*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "skill export: %v\n", err)
		return ExitGeneric
	}
	if err := os.WriteFile(dest, resp.Tarball, 0o644); err != nil {
		fmt.Fprintf(env.Stderr, "skill export: write %s: %v\n", dest, err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "exported %s -> %s\n", name, dest)
	return ExitOK
}

func runSkillImport(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("skill import", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "force re-import / shadow built-in")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl skill import [<source>] [--force] [--dry-run]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	src := fs.Arg(0)
	if src == "" {
		src = filepath.Join(env.Layout.Home, ".claude", "skills")
	}
	mgr := skills.NewManager(skills.Options{
		BuiltinDir: env.Layout.BuiltinSkills,
		CustomDir:  env.Layout.CustomSkills,
	})
	info, err := os.Stat(src)
	if err != nil {
		fmt.Fprintf(env.Stderr, "skill import: %v\n", err)
		return ExitGeneric
	}
	if info.IsDir() {
		// If the dir itself looks like a single skill, import that.
		if _, err := os.Stat(filepath.Join(src, "manifest.json")); err == nil {
			res, err := mgr.Import(src, filepath.Base(src), skills.ImportOptions{Force: *force, DryRun: *dryRun})
			if err != nil {
				fmt.Fprintf(env.Stderr, "skill import: %v\n", err)
				return ExitGeneric
			}
			printImportResult(env, res.Imported, res.Skipped)
			return ExitOK
		}
		imp, skipped, err := mgr.ImportDirectory(src, skills.ImportOptions{Force: *force, DryRun: *dryRun})
		if err != nil {
			fmt.Fprintf(env.Stderr, "skill import: %v\n", err)
			return ExitGeneric
		}
		printImportResult(env, imp, skipped)
		return ExitOK
	}
	fmt.Fprintln(env.Stderr, "skill import: source must be a directory in v1 (tarball import via `skill add`)")
	return ExitUsage
}

func printImportResult(env *Env, imported []string, skipped []skills.SkippedImport) {
	for _, name := range imported {
		fmt.Fprintf(env.Stdout, "imported %s\n", name)
	}
	for _, sk := range skipped {
		fmt.Fprintf(env.Stderr, "skipped %s: %s\n", sk.Name, sk.Reason)
	}
	if len(imported) == 0 && len(skipped) == 0 {
		fmt.Fprintln(env.Stdout, "nothing to import")
	}
}
