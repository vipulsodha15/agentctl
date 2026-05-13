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

func runWorkflow(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		workflowUsage(env)
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "ls", "list":
		return runWorkflowList(ctx, env, rest)
	case "show":
		return runWorkflowShow(ctx, env, rest)
	case "-h", "--help", "help":
		workflowUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl workflow: unknown subcommand %q\n\n", sub)
		workflowUsage(env)
		return ExitUsage
	}
}

func workflowUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl workflow <subcommand>")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands:")
	fmt.Fprintln(env.Stderr, "  ls            List workflows (name, source, stages, description).")
	fmt.Fprintln(env.Stderr, "  show <name>   Print the workflow YAML body.")
}

func runWorkflowList(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("workflow ls", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl workflow ls [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodGet, "/v1/workflows", nil, "")
	if code != ExitOK {
		return code
	}
	var resp struct {
		Workflows []ttl.Workflow `json:"workflows"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Fprintf(env.Stderr, "workflow ls: parse response: %v\n", err)
		return ExitGeneric
	}
	if *asJSON {
		out, _ := json.MarshalIndent(resp.Workflows, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tSOURCE\tSTAGES\tDESCRIPTION")
	for _, w := range resp.Workflows {
		stages := make([]string, 0, len(w.Stages))
		for _, s := range w.Stages {
			stages = append(stages, s.Agent)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", w.Name, w.Source, joinComma(stages), truncate(w.Description, 60))
	}
	_ = tw.Flush()
	return ExitOK
}

func runWorkflowShow(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("workflow show", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl workflow show <name>")
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
	body, code := client.do(ctx, env, http.MethodGet, "/v1/workflows/"+name, nil, "")
	if code != ExitOK {
		return code
	}
	var w ttl.Workflow
	if err := json.Unmarshal(body, &w); err != nil {
		fmt.Fprintf(env.Stderr, "workflow show: parse response: %v\n", err)
		return ExitGeneric
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&w); err != nil {
		fmt.Fprintf(env.Stderr, "workflow show: encode yaml: %v\n", err)
		return ExitGeneric
	}
	_ = enc.Close()
	fmt.Fprint(env.Stdout, buf.String())
	return ExitOK
}

func joinComma(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += ","
		}
		out += p
	}
	if out == "" {
		return "-"
	}
	return out
}
