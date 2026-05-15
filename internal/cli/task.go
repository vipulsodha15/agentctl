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

	"github.com/agentctl/agentctl/internal/tm"
)

func runTask(ctx context.Context, env *Env, args []string) int {
	if len(args) == 0 {
		taskUsage(env)
		return ExitUsage
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "ls", "list":
		return runTaskList(ctx, env, rest)
	case "create":
		return runTaskCreate(ctx, env, rest)
	case "show":
		return runTaskShow(ctx, env, rest)
	case "handoff":
		return runTaskAction(ctx, env, rest, "handoff")
	case "complete":
		return runTaskAction(ctx, env, rest, "complete")
	case "abandon":
		return runTaskAction(ctx, env, rest, "abandon")
	case "-h", "--help", "help":
		taskUsage(env)
		return ExitOK
	default:
		fmt.Fprintf(env.Stderr, "agentctl task: unknown subcommand %q\n\n", sub)
		taskUsage(env)
		return ExitUsage
	}
}

func taskUsage(env *Env) {
	fmt.Fprintln(env.Stderr, "Usage: agentctl task <subcommand> [flags]")
	fmt.Fprintln(env.Stderr, "")
	fmt.Fprintln(env.Stderr, "Subcommands:")
	fmt.Fprintln(env.Stderr, "  ls                                  List tasks.")
	fmt.Fprintln(env.Stderr, "  create [--assembly-line N | --agent N]   Create a task from stdin or --issue-file.")
	fmt.Fprintln(env.Stderr, "         [--repo URL] [--name N]")
	fmt.Fprintln(env.Stderr, "         [--issue-file P]")
	fmt.Fprintln(env.Stderr, "  show <id>                           Show task details + messages.")
	fmt.Fprintln(env.Stderr, "  handoff <id>                        Advance to the next stage.")
	fmt.Fprintln(env.Stderr, "  complete <id>                       Mark task as done.")
	fmt.Fprintln(env.Stderr, "  abandon <id>                        Abandon the task.")
}

func runTaskList(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("task ls", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl task ls [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodGet, "/v1/tasks", nil, "")
	if code != ExitOK {
		return code
	}
	var resp struct {
		Tasks []tm.Task `json:"tasks"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Fprintf(env.Stderr, "task ls: parse response: %v\n", err)
		return ExitGeneric
	}
	if *asJSON {
		out, _ := json.MarshalIndent(resp.Tasks, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tASSEMBLY-LINE\tCURRENT STAGE")
	for _, t := range resp.Tasks {
		stageLabel := "-"
		if t.CurrentStageID != "" {
			for _, s := range t.Stages {
				if s.ID == t.CurrentStageID {
					stageLabel = fmt.Sprintf("%d:%s", s.Position, s.AgentName)
					break
				}
			}
			if stageLabel == "-" {
				stageLabel = shortID(t.CurrentStageID)
			}
		}
		wf := t.AssemblyLineName
		if wf == "" {
			wf = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", shortID(t.ID), defaultDash(t.Name), t.Status, wf, stageLabel)
	}
	_ = tw.Flush()
	return ExitOK
}

func runTaskCreate(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("task create", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	assemblyLine := fs.String("assembly-line", "", "assembly line name to attach")
	agent := fs.String("agent", "", "single agent to assign (alternative to --assembly-line)")
	repo := fs.String("repo", "", "repository URL")
	name := fs.String("name", "", "task name")
	issueFile := fs.String("issue-file", "", "path to issue markdown (defaults to stdin)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl task create [--assembly-line N | --agent N] [--repo URL] [--name N] [--issue-file PATH]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "If --issue-file is omitted, the issue body is read from stdin.")
		fmt.Fprintln(env.Stderr, "Use --assembly-line for a multi-stage chain, or --agent to chat with a single agent.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	var issueMD []byte
	var err error
	if *issueFile != "" {
		issueMD, err = os.ReadFile(*issueFile)
		if err != nil {
			fmt.Fprintf(env.Stderr, "task create: read issue file: %v\n", err)
			return ExitGeneric
		}
	} else {
		issueMD, err = io.ReadAll(env.Stdin)
		if err != nil {
			fmt.Fprintf(env.Stderr, "task create: read stdin: %v\n", err)
			return ExitGeneric
		}
	}
	if len(bytes.TrimSpace(issueMD)) == 0 {
		fmt.Fprintln(env.Stderr, "task create: issue body is empty (pass --issue-file or pipe via stdin)")
		return ExitUsage
	}
	if *assemblyLine != "" && *agent != "" {
		fmt.Fprintln(env.Stderr, "task create: pass --assembly-line or --agent, not both")
		return ExitUsage
	}
	req := tm.CreateTaskRequest{
		Name:             *name,
		AssemblyLineName: *assemblyLine,
		AgentName:        *agent,
		RepoURL:          *repo,
		IssueMD:          string(issueMD),
		SourceKind:       tm.SourceFreeform,
	}
	payload, _ := json.Marshal(&req)
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodPost, "/v1/tasks", bytes.NewReader(payload), "application/json")
	if code != ExitOK {
		return code
	}
	var task tm.Task
	if err := json.Unmarshal(body, &task); err != nil {
		fmt.Fprintf(env.Stderr, "task create: parse response: %v\n", err)
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "created %s\n", task.ID)
	return ExitOK
}

func runTaskShow(ctx context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("task show", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit JSON")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl task show <id> [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	id := fs.Arg(0)
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	body, code := client.do(ctx, env, http.MethodGet, "/v1/tasks/"+id, nil, "")
	if code != ExitOK {
		return code
	}
	var resp struct {
		Task     *tm.Task     `json:"task"`
		Messages []tm.Message `json:"messages"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		fmt.Fprintf(env.Stderr, "task show: parse response: %v\n", err)
		return ExitGeneric
	}
	if *asJSON {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	t := resp.Task
	if t == nil {
		fmt.Fprintln(env.Stderr, "task show: empty response")
		return ExitGeneric
	}
	fmt.Fprintf(env.Stdout, "ID:        %s\n", t.ID)
	fmt.Fprintf(env.Stdout, "Name:      %s\n", defaultDash(t.Name))
	fmt.Fprintf(env.Stdout, "Status:    %s\n", t.Status)
	fmt.Fprintf(env.Stdout, "Assembly line:  %s\n", defaultDash(t.AssemblyLineName))
	if t.RepoURL != "" {
		fmt.Fprintf(env.Stdout, "Repo:      %s\n", t.RepoURL)
	}
	if t.SourceURL != "" {
		fmt.Fprintf(env.Stdout, "Source:    %s (%s)\n", t.SourceURL, t.SourceKind)
	}
	if !t.CreatedAt.IsZero() {
		fmt.Fprintf(env.Stdout, "Created:   %s\n", t.CreatedAt.Format("2006-01-02 15:04:05"))
	}
	if len(t.Stages) > 0 {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Stages:")
		tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  POS\tAGENT\tSTATUS\tSESSION")
		for _, s := range t.Stages {
			marker := "  "
			if s.ID == t.CurrentStageID {
				marker = "* "
			}
			fmt.Fprintf(tw, "%s%d\t%s\t%s\t%s\n", marker, s.Position, s.AgentName, s.Status, defaultDash(s.SessionID))
		}
		_ = tw.Flush()
	}
	if len(resp.Messages) > 0 {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Messages:")
		for _, m := range resp.Messages {
			ts := m.At.Format("15:04:05")
			who := m.Role
			if m.AgentName != "" {
				who = m.AgentName + "/" + m.Role
			}
			fmt.Fprintf(env.Stdout, "  [%s] %s: %s\n", ts, who, m.Content)
		}
	}
	return ExitOK
}

func runTaskAction(ctx context.Context, env *Env, args []string, action string) int {
	fs := flag.NewFlagSet("task "+action, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "Usage: agentctl task %s <id>\n", action)
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	id := fs.Arg(0)
	client, code := newWebClient(env)
	if client == nil {
		return code
	}
	_, code = client.do(ctx, env, http.MethodPost, "/v1/tasks/"+id+"/"+action, nil, "")
	if code != ExitOK {
		return code
	}
	fmt.Fprintf(env.Stdout, "%s %s\n", action, id)
	return ExitOK
}

func shortID(id string) string {
	if len(id) <= 6 {
		return id
	}
	return id[len(id)-6:]
}

func defaultDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
