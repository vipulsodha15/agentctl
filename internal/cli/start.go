package cli

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/config"
	"github.com/agentctl/agentctl/internal/paths"
	"github.com/agentctl/agentctl/internal/proto"
	"github.com/agentctl/agentctl/internal/secrets"
)

func runStart(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var (
		name     string
		mcpsCSV  string
		noMCPCSV string
		repoArr  stringList
		model    string
		provider string
		memBytes int64
		cpuLimit float64
	)
	plain := fs.Bool("plain", false, "use line-based streaming output instead of the TUI")
	fs.StringVar(&name, "name", "", "human-readable name for the session")
	fs.StringVar(&mcpsCSV, "mcps", "", "comma-separated MCP names to enable (default: registry defaults)")
	fs.StringVar(&noMCPCSV, "no-mcp", "", "comma-separated MCP names to exclude")
	fs.Var(&repoArr, "repo", "repo URL to clone (repeatable)")
	fs.StringVar(&model, "model", "", "model id override (e.g. claude-sonnet-4-6)")
	// --provider is registered unconditionally on the flag set so callers can
	// always pass it scripted; help-text visibility is gated on
	// EnabledProviders() count per ADR 0020 §UX principles (provider
	// invisibility). The Usage closure below filters it from PrintDefaults
	// when only one provider is configured so the existing Anthropic-only
	// workflow is byte-for-byte unchanged after upgrade.
	fs.StringVar(&provider, "provider", "", "agent provider (anthropic|openai); omit to let the resolver pick")
	fs.Int64Var(&memBytes, "mem-limit", 0, "container memory limit in bytes (0 = config default)")
	fs.Float64Var(&cpuLimit, "cpu-limit", 0, "container CPU limit in cores (0 = config default)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl start [--plain] [--name NAME] [--mcps a,b] [--no-mcp x] [--repo URL ...] [--model MODEL] [--mem-limit N] [--cpu-limit N]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Creates a new session and attaches to its event stream. On a TTY a")
		fmt.Fprintln(env.Stderr, "fullscreen TUI is shown (markdown, tool blocks, status bar, input box).")
		fmt.Fprintln(env.Stderr, "When piped or with --plain, falls back to line-based streaming output and")
		fmt.Fprintln(env.Stderr, "reads stdin for messages. Ctrl-D / Ctrl-C detaches; the session keeps running.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Examples:")
		fmt.Fprintln(env.Stderr, "  agentctl start --repo https://github.com/me/repo.git")
		fmt.Fprintln(env.Stderr, "  agentctl start --name auth-refactor --mcps github,internal-jira")
		fmt.Fprintln(env.Stderr, "  agentctl start --no-mcp github --model claude-opus-4-7")
		fmt.Fprintln(env.Stderr, "  agentctl start --plain | tee transcript.log")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		printFlagsHidingProvider(fs, env)
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}

	cfg, _ := config.Load(env.Layout.ConfigFile)
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "start: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()

	req := proto.CreateSessionRequest{
		Name:          name,
		MCPs:          splitCSV(mcpsCSV),
		ExcludeMCPs:   splitCSV(noMCPCSV),
		Repos:         repoArr,
		Model:         model,
		Provider:      provider,
		MemLimitBytes: memBytes,
		CPULimitCores: cpuLimit,
	}
	var resp proto.CreateSessionResponse
	if err := c.Call(proto.OpCreateSession, req, &resp, 30*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "create session: %v\n", err)
		return ExitGeneric
	}
	webURL := fmt.Sprintf("http://%s/sessions/%s", cfg.Agentd.WebAddr, resp.SessionID)
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	if *plain || !stdoutIsTTY(env) {
		fmt.Fprintf(env.Stdout, "session %s\nweb:     %s\n", resp.SessionID, webURL)
		stdinDone := make(chan struct{})
		go func() {
			defer close(stdinDone)
			readStdinAndSend(streamCtx, env.Layout.SocketFile, resp.SessionID, env.Stdin, env.Stderr)
			cancelStream()
		}()
		code := attachAndRender(streamCtx, c, resp.SessionID, env)
		cancelStream()
		if ctx.Err() == nil {
			<-stdinDone
		}
		return code
	}
	// TUI takes over the screen; print the session id/url to stderr so it
	// survives in the scrollback after detach.
	fmt.Fprintf(env.Stderr, "session %s · %s\n", resp.SessionID, webURL)
	return attachAndRunTUI(streamCtx, c, resp.SessionID, env)
}

// printFlagsHidingProvider mirrors fs.PrintDefaults() but suppresses the
// --provider flag while fewer than two providers are configured. This is
// the "provider invisibility" rule from ADR 0020 §UX principles: a user
// with only Anthropic configured sees the same --help they always have.
// The count is read locally from secrets.json — the CLI already owns
// that file, so this is a host-local decision that doesn't need to
// round-trip the daemon.
func printFlagsHidingProvider(fs *flag.FlagSet, env *Env) {
	hide := localEnabledProviderCount(env) < 2
	fs.VisitAll(func(f *flag.Flag) {
		if hide && f.Name == "provider" {
			return
		}
		fmt.Fprintf(env.Stderr, "  -%s", f.Name)
		if f.DefValue != "" {
			fmt.Fprintf(env.Stderr, " %s", f.DefValue)
		}
		fmt.Fprintf(env.Stderr, "\n\t%s\n", f.Usage)
	})
}

// localEnabledProviderCount counts enabled providers by loading
// secrets.json from the user's config dir. Returns 0 when the file
// can't be read (e.g. fresh install, permission error); the help-text
// caller treats that as "hide --provider" which matches the
// pre-upgrade experience.
func localEnabledProviderCount(env *Env) int {
	layout := env.Layout
	if layout.SecretsFile == "" {
		layout = paths.Resolve()
	}
	sec, err := secrets.Load(layout.SecretsFile)
	if err != nil {
		return 0
	}
	credsPathFor := func(provider string) string {
		switch provider {
		case secrets.ProviderAnthropic:
			return layout.ClaudeCredsFile
		case secrets.ProviderOpenAI:
			return layout.CodexCredsFile
		}
		return ""
	}
	return len(sec.EnabledProviders(credsPathFor))
}

func splitCSV(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

// readStdinAndSend forwards each line of stdin as a SendMessage RPC. It opens
// its own socket connection rather than sharing one with the attach stream:
// Call() sets a per-request read deadline, and a shared conn would let that
// deadline fire on the stream goroutine's blocking Recv (surfaced as "attach:
// i/o timeout") while also racing the two goroutines for inbound frames.
func readStdinAndSend(ctx context.Context, socketPath, sessionID string, in io.Reader, errw io.Writer) {
	sender, err := cliclient.Dial(socketPath, 3*time.Second)
	if err != nil {
		fmt.Fprintf(errw, "send: %v\n", err)
		return
	}
	defer func() { _ = sender.Close() }()

	br := bufio.NewReader(in)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		line, err := br.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if line != "" {
			var resp proto.SendMessageResponse
			if cerr := sender.Call(proto.OpSendMessage, proto.SendMessageRequest{
				SessionID: sessionID, Content: line, ClientID: "cli",
			}, &resp, 5*time.Second); cerr != nil {
				fmt.Fprintf(errw, "send: %v\n", cerr)
			}
		}
		if err != nil {
			return
		}
	}
}
