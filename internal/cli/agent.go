package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
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
	case "add":
		return runAgentAdd(ctx, env, rest)
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
	fmt.Fprintln(env.Stderr, "  ls                            List agents (name, source, colour, description).")
	fmt.Fprintln(env.Stderr, "  show <name>                   Print the agent YAML body.")
	fmt.Fprintln(env.Stderr, "  add [name] --from <path>      Create a custom agent from a YAML file (or stdin).")
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

func runAgentAdd(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("agent add", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	from := fs.String("from", "", "path to YAML file (use '-' or omit to read stdin)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl agent add [name] --from <path>")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "If [name] is given it must match the 'name:' in the YAML body.")
		fmt.Fprintln(env.Stderr, "If --from is omitted (or is '-'), the YAML is read from stdin.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}

	var (
		yamlBytes []byte
		err       error
	)
	switch {
	case *from == "" || *from == "-":
		yamlBytes, err = io.ReadAll(env.Stdin)
		if err != nil {
			fmt.Fprintf(env.Stderr, "agent add: read stdin: %v\n", err)
			return ExitGeneric
		}
	default:
		yamlBytes, err = os.ReadFile(*from)
		if err != nil {
			fmt.Fprintf(env.Stderr, "agent add: read %s: %v\n", *from, err)
			return ExitGeneric
		}
	}
	if len(bytes.TrimSpace(yamlBytes)) == 0 {
		fmt.Fprintln(env.Stderr, "agent add: YAML body is empty (pass --from <path> or pipe via stdin)")
		return ExitUsage
	}

	pathName := ""
	if fs.NArg() >= 1 {
		pathName = fs.Arg(0)
	}

	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	target := "/v1/agents"
	method := http.MethodPost
	if pathName != "" {
		target = "/v1/agents/" + pathName
		method = http.MethodPut
	}
	body, code := client.do(ctx, env, method, target, bytes.NewReader(yamlBytes), "application/x-yaml")
	if code != ExitOK {
		return code
	}
	var saved ttl.Agent
	if err := json.Unmarshal(body, &saved); err != nil {
		fmt.Fprintf(env.Stderr, "agent add: parse response: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "added agent %s\n", saved.Name)
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
