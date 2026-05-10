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
	"github.com/agentctl/agentctl/internal/proto"
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
		memBytes int64
		cpuLimit float64
	)
	fs.StringVar(&name, "name", "", "human-readable name for the session")
	fs.StringVar(&mcpsCSV, "mcps", "", "comma-separated MCP names to enable (default: registry defaults)")
	fs.StringVar(&noMCPCSV, "no-mcp", "", "comma-separated MCP names to exclude")
	fs.Var(&repoArr, "repo", "repo URL to clone (repeatable)")
	fs.StringVar(&model, "model", "", "model id override (e.g. claude-sonnet-4-6)")
	fs.Int64Var(&memBytes, "mem-limit", 0, "container memory limit in bytes (0 = config default)")
	fs.Float64Var(&cpuLimit, "cpu-limit", 0, "container CPU limit in cores (0 = config default)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl start [--name NAME] [--mcps a,b] [--no-mcp x] [--repo URL ...] [--model MODEL] [--mem-limit N] [--cpu-limit N]")
		fs.PrintDefaults()
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Creates a session, attaches to its event stream, and renders messages live.")
		fmt.Fprintln(env.Stderr, "Type a message + Enter to send. Ctrl-D / Ctrl-C detaches.")
	}
	if err := fs.Parse(args); err != nil {
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
		MemLimitBytes: memBytes,
		CPULimitCores: cpuLimit,
	}
	var resp proto.CreateSessionResponse
	if err := c.Call(proto.OpCreateSession, req, &resp, 30*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "create session: %v\n", err)
		return ExitGeneric
	}
	webURL := fmt.Sprintf("http://%s/sessions/%s", cfg.Agentd.WebAddr, resp.SessionID)
	fmt.Fprintf(env.Stdout, "session %s\nweb:     %s\n", resp.SessionID, webURL)

	stdinDone := make(chan struct{})
	streamCtx, cancelStream := context.WithCancel(ctx)
	defer cancelStream()

	go func() {
		defer close(stdinDone)
		readStdinAndSend(streamCtx, c, resp.SessionID, env.Stdin, env.Stderr)
		cancelStream()
	}()

	code := attachAndRender(streamCtx, c, resp.SessionID, env)
	cancelStream()
	<-stdinDone
	return code
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

func readStdinAndSend(ctx context.Context, c *cliclient.Client, sessionID string, in io.Reader, errw io.Writer) {
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
			if cerr := c.Call(proto.OpSendMessage, proto.SendMessageRequest{
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
