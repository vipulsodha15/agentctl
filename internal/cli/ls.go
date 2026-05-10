package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runLs(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("ls", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	verbose := fs.Bool("verbose", false, "include extended columns")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl ls [--verbose] [--json]")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Lists sessions known to agentd, ordered by last activity.")
		fmt.Fprintln(env.Stderr, "Default columns: ID, NAME, STATUS, LAST ACTIVITY, IMAGE_ID, COST.")
		fmt.Fprintln(env.Stderr, "--verbose adds: IN_FLIGHT, QUEUE, MEM, CPU.")
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(reorderArgs(args)); err != nil {
		return ExitUsage
	}
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "ls: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	var resp proto.ListSessionsResponse
	if err := c.Call(proto.OpListSessions, proto.ListSessionsRequest{}, &resp, 5*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "ls: %v\n", err)
		return ExitGeneric
	}
	sort.Slice(resp.Sessions, func(i, j int) bool {
		return resp.Sessions[i].LastActivityAt.After(resp.Sessions[j].LastActivityAt)
	})
	if *asJSON {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	if *verbose {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLAST ACTIVITY\tIMAGE_ID\tIN_FLIGHT\tQUEUE\tMEM\tCPU\tCOST")
	} else {
		fmt.Fprintln(tw, "ID\tNAME\tSTATUS\tLAST ACTIVITY\tIMAGE_ID\tCOST")
	}
	now := time.Now().UTC()
	for _, s := range resp.Sessions {
		cost := "—"
		if s.CostUSD != nil {
			cost = fmt.Sprintf("$%.2f", *s.CostUSD)
		}
		if *verbose {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\t%d\t%dB\t%.2f\t%s\n",
				s.ID, s.Name, s.Status, humanAge(now, s.LastActivityAt), shortImage(s.ImageID),
				s.InFlight, s.QueueDepth, s.MemLimitBytes, s.CPULimitCores, cost)
		} else {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
				s.ID, s.Name, s.Status, humanAge(now, s.LastActivityAt), shortImage(s.ImageID), cost)
		}
	}
	_ = tw.Flush()
	return ExitOK
}

func humanAge(now, t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

func shortImage(id string) string {
	if len(id) > 16 {
		return id[len(id)-12:]
	}
	return id
}
