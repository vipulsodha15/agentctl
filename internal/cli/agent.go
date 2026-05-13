package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"text/tabwriter"

	"github.com/agentctl/agentctl/internal/ttl"
	"gopkg.in/yaml.v3"
)

func runAgent(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		agentUsage(env)
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "ls", "list":
		return runAgentList(ctx, env, rest)
	case "show":
		return runAgentShow(ctx, env, rest)
	case "-h", "--help", "help":
		agentUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl agent: unknown subcommand %q\n\n", sub)
		agentUsage(env)
		return ExitUsage
	}
}

func agentUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl agent <subcommand>")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands:")
	fmt.Fprintln(env.Stderr, "  ls            List agents (name, source, colour, description).")
	fmt.Fprintln(env.Stderr, "  show <name>   Print the agent YAML body.")
}

func runAgentList(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("agent ls", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl agent ls [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodGet, "/v1/agents", nil, "")
	if code != ExitOK {
		return code
	}
	var resp struct {
		Agents []ttl.Agent `json:"agents"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Fprintf(env.Stderr, "agent ls: parse response: %v\n", err)
		return ExitGeneric
	}
	if *asJSON {
		out, _ := json.MarshalIndent(resp.Agents, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tCOLOUR\tDESCRIPTION")
	for _, a := range resp.Agents {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.Name, a.Source, a.Colour, truncate(a.Description, 60))
	}
	_ = tw.Flush()
	return ExitOK
}

func runAgentShow(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("agent show", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl agent show <name>")
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	name := fs.Arg(0)
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodGet, "/v1/agents/"+name, nil, "")
	if code != ExitOK {
		return code
	}
	var a ttl.Agent
	if err := json.Unmarshal(body, &a); err != nil {
		fmt.Fprintf(env.Stderr, "agent show: parse response: %v\n", err)
		return ExitGeneric
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&a); err != nil {
		fmt.Fprintf(env.Stderr, "agent show: encode yaml: %v\n", err)
		return ExitGeneric
	}
	_ = enc.Close()
	fmt.Fprint(env.Stdout, buf.String())
	return ExitOK
}
