package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/mcpimport"
	"github.com/agentctl/agentctl/internal/proto"
)

func runMCP(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		mcpUsage(env)
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list", "ls":
		return runMCPList(ctx, env, rest)
	case "add":
		return runMCPAdd(ctx, env, rest)
	case "remove", "rm":
		return runMCPRemove(ctx, env, rest)
	case "set-default":
		return runMCPSetDefault(ctx, env, rest)
	case "update":
		return runMCPUpdate(ctx, env, rest)
	case "import":
		return runMCPImport(ctx, env, rest)
	case "-h", "--help", "help":
		mcpUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl mcp: unknown subcommand %q\n\n", sub)
		mcpUsage(env)
		return ExitUsage
	}
}

func mcpUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl mcp <subcommand> [flags]")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Manage the MCP registry. Entries are stored in agentd.db; sessions read")
	fmt.Fprintln(env.Stderr, "default-enabled rows at start, or the explicit --mcps list passed to start.")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands:")
	fmt.Fprintln(env.Stderr, "  list                       List MCP registry entries (--json for machine-readable).")
	fmt.Fprintln(env.Stderr, "  add <name>                 Add a new MCP entry.")
	fmt.Fprintln(env.Stderr, "  update <name>              Update fields of an MCP entry.")
	fmt.Fprintln(env.Stderr, "  remove <name>              Remove an MCP entry.")
	fmt.Fprintln(env.Stderr, "  set-default <name> on|off  Toggle default-enabled.")
	fmt.Fprintln(env.Stderr, "  import [claude|codex]      Import MCP servers from Claude Code or Codex CLI.")
}

func dialAgentd(env *Env) (*cliclient.Client, int) {
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "%v\n", err)
		return nil, ExitEnvironment
	}
	return c, ExitOK
}

func runMCPList(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp list", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp list [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.ListMCPsResponse
	if err := c.Call(proto.OpListMCPs, proto.ListMCPsRequest{}, &resp, 5*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "mcp list: %v\n", err)
		return ExitGeneric
	}
	if *asJSON {
		out, _ := json.MarshalIndent(resp.MCPs, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tURL\tTRANSPORT\tKIND\tDEFAULT\tDESCRIPTION")
	for _, m := range resp.MCPs {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%t\t%s\n",
			m.Name, m.URL, m.Transport, m.Kind, m.DefaultEnabled, truncate(m.Description, 60))
	}
	_ = tw.Flush()
	return ExitOK
}

func runMCPAdd(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	url := fs.String("url", "", "MCP server URL (required)")
	transport := fs.String("transport", "http", "wire transport: http|sse")
	kind := fs.String("kind", "none", "auth kind: none|github_pat")
	authConfig := fs.String("auth-config", "", "kind-specific JSON config")
	defaultEnabled := fs.Bool("default-enabled", false, "include in new sessions by default")
	description := fs.String("description", "", "free-text description")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp add <name> --url <url> [--transport http|sse] [--kind none|github_pat] [--auth-config <json>] [--default-enabled] [--description <s>]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Adds an MCP server entry to the registry. Sessions enable it via")
		fmt.Fprintln(env.Stderr, "`agentctl start --mcps <name>` or by setting --default-enabled.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Examples:")
		fmt.Fprintln(env.Stderr, "  agentctl mcp add github --url https://api.githubcopilot.com/mcp/ \\")
		fmt.Fprintln(env.Stderr, "    --transport http --kind github_pat --default-enabled")
		fmt.Fprintln(env.Stderr, "  agentctl mcp add internal-jira --url http://10.0.0.5/mcp/ --transport sse")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	if *url == "" {
		fmt.Fprintln(env.Stderr, "mcp add: --url is required")
		return ExitUsage
	}
	name := fs.Arg(0)
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.AddMCPResponse
	err := c.Call(proto.OpAddMCP, proto.AddMCPRequest{
		Name: name, URL: *url, Transport: *transport, Kind: *kind,
		AuthConfigJSON: *authConfig, DefaultEnabled: *defaultEnabled, Description: *description,
	}, &resp, 5*time.Second)
	if err != nil {
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrConflict {
			fmt.Fprintf(env.Stderr, "mcp add: %q already exists\n", name)
			return ExitSessionState
		}
		fmt.Fprintf(env.Stderr, "mcp add: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "added %s\n", resp.MCP.Name)
	return ExitOK
}

func runMCPRemove(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp remove", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "remove even if referenced by an active session")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp remove <name> [--force] [--yes]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	name := fs.Arg(0)
	if !*yes {
		fmt.Fprintf(env.Stderr, "Remove MCP entry %q? [y/N]: ", name)
		buf := make([]byte, 16)
		n, _ := env.Stdin.Read(buf)
		ans := strings.TrimSpace(string(buf[:n]))
		if ans != "y" && ans != "Y" && ans != "yes" {
			fmt.Fprintln(env.Stderr, "aborted")
			return ExitOK
		}
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.RemoveMCPResponse
	if err := c.Call(proto.OpRemoveMCP, proto.RemoveMCPRequest{Name: name, Force: *force}, &resp, 5*time.Second); err != nil {
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrNotFound {
			fmt.Fprintf(env.Stderr, "mcp remove: %q not found\n", name)
			return ExitSessionState
		}
		fmt.Fprintf(env.Stderr, "mcp remove: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "removed %s\n", name)
	return ExitOK
}

func runMCPSetDefault(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp set-default", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp set-default <name> on|off")
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return ExitUsage
	}
	name := fs.Arg(0)
	val := strings.ToLower(fs.Arg(1))
	var enabled bool
	switch val {
	case "on", "true", "1", "yes":
		enabled = true
	case "off", "false", "0", "no":
		enabled = false
	default:
		fmt.Fprintf(env.Stderr, "mcp set-default: expected on|off, got %q\n", val)
		return ExitUsage
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.SetDefaultMCPResponse
	if err := c.Call(proto.OpSetDefaultMCP, proto.SetDefaultMCPRequest{Name: name, DefaultEnabled: enabled}, &resp, 5*time.Second); err != nil {
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrNotFound {
			fmt.Fprintf(env.Stderr, "mcp set-default: %q not found\n", name)
			return ExitSessionState
		}
		fmt.Fprintf(env.Stderr, "mcp set-default: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "%s default_enabled=%t\n", resp.MCP.Name, resp.MCP.DefaultEnabled)
	return ExitOK
}

func runMCPUpdate(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp update", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	url := fs.String("url", "", "new URL")
	transport := fs.String("transport", "", "new transport")
	kind := fs.String("kind", "", "new kind")
	authConfig := fs.String("auth-config", "", "new auth-config JSON")
	description := fs.String("description", "", "new description")
	defaultEnabledStr := fs.String("default-enabled", "", "true|false to update default-enabled")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp update <name> [--url ...] [--transport ...] [--kind ...] [--auth-config ...] [--description ...] [--default-enabled true|false]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	name := fs.Arg(0)
	req := proto.UpdateMCPRequest{Name: name}
	hasFlag := false
	fs.Visit(func(fl *flag.Flag) {
		hasFlag = true
		switch fl.Name {
		case "url":
			v := *url
			req.URL = &v
		case "transport":
			v := *transport
			req.Transport = &v
		case "kind":
			v := *kind
			req.Kind = &v
		case "auth-config":
			v := *authConfig
			req.AuthConfigJSON = &v
		case "description":
			v := *description
			req.Description = &v
		case "default-enabled":
			s := strings.ToLower(*defaultEnabledStr)
			b := s == "true" || s == "on" || s == "1" || s == "yes"
			req.DefaultEnabled = &b
		}
	})
	if !hasFlag {
		fmt.Fprintln(env.Stderr, "mcp update: pass at least one --flag")
		return ExitUsage
	}
	c, code := dialAgentd(env)
	if c == nil {
		return code
	}
	defer func() { _ = c.Close() }()
	var resp proto.UpdateMCPResponse
	if err := c.Call(proto.OpUpdateMCP, req, &resp, 5*time.Second); err != nil {
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrNotFound {
			fmt.Fprintf(env.Stderr, "mcp update: %q not found\n", name)
			return ExitSessionState
		}
		fmt.Fprintf(env.Stderr, "mcp update: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "updated %s\n", resp.MCP.Name)
	return ExitOK
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func isAPIError(err error, target **cliclient.APIError) bool {
	for cur := err; cur != nil; cur = unwrap(cur) {
		if e, ok := cur.(*cliclient.APIError); ok {
			*target = e
			return true
		}
	}
	return false
}

func unwrap(err error) error {
	if u, ok := err.(interface{ Unwrap() error }); ok {
		return u.Unwrap()
	}
	return nil
}

func runMCPImport(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("mcp import", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	path := fs.String("path", "", "explicit source file (overrides the default for the chosen source)")
	force := fs.Bool("force", false, "overwrite existing registry entries with the same name")
	dryRun := fs.Bool("dry-run", false, "report what would be imported without writing")
	defaultEnabled := fs.Bool("default-enabled", false, "mark imported entries as default-enabled")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl mcp import [claude|codex] [--path <file>] [--force] [--dry-run] [--default-enabled]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Reads MCP servers from Claude Code (~/.claude.json) or Codex CLI")
		fmt.Fprintln(env.Stderr, "(~/.codex/config.toml) and adds matching entries to the registry.")
		fmt.Fprintln(env.Stderr, "stdio servers are reported and skipped (the registry only supports")
		fmt.Fprintln(env.Stderr, "http/sse today).")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	source := strings.ToLower(fs.Arg(0))
	if source == "" {
		source = "claude"
	}
	src, entries, code := loadMCPImportSource(env, source, *path)
	if code != ExitOK {
		return code
	}
	if len(entries) == 0 {
		fmt.Fprintf(env.Stdout, "mcp import: no MCP servers found in %s\n", src)
		return ExitOK
	}
	return applyMCPImport(env, src, entries, *force, *dryRun, *defaultEnabled)
}

// loadMCPImportSource resolves the source path and parses it. It returns
// the resolved path so callers can include it in user-facing messages.
func loadMCPImportSource(env *Env, source, override string) (string, []mcpimport.ParsedEntry, int) {
	var (
		path    string
		entries []mcpimport.ParsedEntry
		err     error
	)
	switch source {
	case "claude":
		path = override
		if path == "" {
			path = mcpimport.DefaultClaudePath(env.Layout.Home)
		}
		entries, err = mcpimport.ParseClaude(path)
	case "codex":
		path = override
		if path == "" {
			path = mcpimport.DefaultCodexPath(env.Layout.Home)
		}
		entries, err = mcpimport.ParseCodex(path)
	default:
		fmt.Fprintf(env.Stderr, "mcp import: unknown source %q (want claude|codex)\n", source)
		return "", nil, ExitUsage
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(env.Stdout, "mcp import: %s not found, nothing to import.\n", path)
			return path, nil, ExitOK
		}
		fmt.Fprintf(env.Stderr, "mcp import: %v\n", err)
		return path, nil, ExitGeneric
	}
	return path, entries, ExitOK
}

// applyMCPImport pushes each parsed entry into the registry via the
// existing AddMCP / UpdateMCP RPCs. With --force, conflicting names are
// updated in place; without it, they are reported as skipped.
func applyMCPImport(env *Env, src string, entries []mcpimport.ParsedEntry, force, dryRun, defaultEnabled bool) int {
	var c *cliclient.Client
	if !dryRun {
		var code int
		c, code = dialAgentd(env)
		if c == nil {
			return code
		}
		defer func() { _ = c.Close() }()
	}
	var imported, skipped int
	for _, e := range entries {
		if e.Skip != "" {
			fmt.Fprintf(env.Stderr, "skipped %s: %s\n", e.Name, e.Skip)
			skipped++
			continue
		}
		if dryRun {
			fmt.Fprintf(env.Stdout, "would import %s (%s, %s)\n", e.Name, e.URL, e.Transport)
			imported++
			continue
		}
		var resp proto.AddMCPResponse
		err := c.Call(proto.OpAddMCP, proto.AddMCPRequest{
			Name:           e.Name,
			URL:            e.URL,
			Transport:      e.Transport,
			Kind:           "none",
			DefaultEnabled: defaultEnabled,
			Description:    e.Description,
		}, &resp, 5*time.Second)
		if err == nil {
			fmt.Fprintf(env.Stdout, "imported %s\n", e.Name)
			imported++
			continue
		}
		var apiErr *cliclient.APIError
		if isAPIError(err, &apiErr) && apiErr.Code == proto.ErrConflict {
			if !force {
				fmt.Fprintf(env.Stderr, "skipped %s: already in registry (use --force to overwrite)\n", e.Name)
				skipped++
				continue
			}
			url, transport, kind, desc := e.URL, e.Transport, "none", e.Description
			var upResp proto.UpdateMCPResponse
			upErr := c.Call(proto.OpUpdateMCP, proto.UpdateMCPRequest{
				Name:        e.Name,
				URL:         &url,
				Transport:   &transport,
				Kind:        &kind,
				Description: &desc,
			}, &upResp, 5*time.Second)
			if upErr != nil {
				fmt.Fprintf(env.Stderr, "mcp import: update %s: %v\n", e.Name, upErr)
				skipped++
				continue
			}
			fmt.Fprintf(env.Stdout, "updated %s\n", e.Name)
			imported++
			continue
		}
		fmt.Fprintf(env.Stderr, "mcp import: add %s: %v\n", e.Name, err)
		skipped++
	}
	fmt.Fprintf(env.Stdout, "mcp import: %d imported, %d skipped from %s\n", imported, skipped, src)
	return ExitOK
}
