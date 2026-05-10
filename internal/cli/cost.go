package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"text/tabwriter"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runCost(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("cost", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	since := fs.String("since", "", "aggregate over the given range (e.g., today, 7d, 30d, 2026-05-01..2026-05-09)")
	sessionFlag := fs.String("session", "", "filter aggregate to a single session id")
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage:")
		fmt.Fprintln(env.Stderr, "  agentctl cost <session>            per-session detail (total, model breakdown, turn timeline)")
		fmt.Fprintln(env.Stderr, "  agentctl cost --since <range>      aggregate over a date range across all sessions")
		fmt.Fprintln(env.Stderr, "  agentctl cost --since <range> --session <id>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}

	positional := ""
	if fs.NArg() > 0 {
		positional = fs.Arg(0)
	}
	if positional == "" && *since == "" && *sessionFlag == "" {
		fs.Usage()
		return ExitUsage
	}

	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "cost: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()

	req := proto.GetCostRequest{Since: *since, SessionID: *sessionFlag}
	if positional != "" && req.SessionID == "" && req.Since == "" {
		req.SessionID = positional
	}

	var resp proto.GetCostResponse
	if err := c.Call(proto.OpGetCost, req, &resp, 10*time.Second); err != nil {
		fmt.Fprintf(env.Stderr, "cost: %v\n", err)
		return ExitGeneric
	}

	if *asJSON {
		out, _ := json.MarshalIndent(resp, "", "  ")
		fmt.Fprintln(env.Stdout, string(out))
		return ExitOK
	}

	if resp.PerSession != nil {
		renderPerSessionCost(env, resp.PerSession)
		return ExitOK
	}
	if resp.Range != nil {
		renderRangeCost(env, resp.Range)
		return ExitOK
	}
	fmt.Fprintln(env.Stdout, "no cost data")
	return ExitOK
}

func renderPerSessionCost(env *Env, p *proto.SessionCostTotals) {
	fmt.Fprintf(env.Stdout, "Session %s\n", p.SessionID)
	fmt.Fprintf(env.Stdout, "  Total cost:     %s\n", formatUSD(p.CostUSD))
	fmt.Fprintf(env.Stdout, "  Total tokens:   in=%s out=%s cache_r=%s cache_w=%s\n",
		commaInt(p.InputTokens), commaInt(p.OutputTokens),
		commaInt(p.CacheReadTokens), commaInt(p.CacheWriteTokens))
	fmt.Fprintf(env.Stdout, "  Turns:          %d\n", p.Turns)
	if p.HasUnknown {
		fmt.Fprintln(env.Stdout, "  (some turns used a model not in the price table; cost shown is partial)")
	}

	if len(p.ByModel) > 0 {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "By model:")
		tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  MODEL\tTURNS\tIN\tOUT\tCACHE_R\tCACHE_W\tCOST")
		for _, m := range p.ByModel {
			cost := formatUSD(m.CostUSD)
			if m.HasUnknown {
				cost += "*"
			}
			fmt.Fprintf(tw, "  %s\t%d\t%s\t%s\t%s\t%s\t%s\n",
				m.Model, m.Turns,
				commaInt(m.InputTokens), commaInt(m.OutputTokens),
				commaInt(m.CacheReadTokens), commaInt(m.CacheWriteTokens),
				cost)
		}
		_ = tw.Flush()
	}

	if len(p.Timeline) > 0 {
		fmt.Fprintln(env.Stdout, "")
		fmt.Fprintln(env.Stdout, "Recent turns:")
		tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "  AT\tTURN\tMODEL\tIN\tOUT\tCOST")
		// Show the last 50 turns (most recent at the bottom).
		start := 0
		if len(p.Timeline) > 50 {
			start = len(p.Timeline) - 50
		}
		for _, t := range p.Timeline[start:] {
			cost := "—"
			if t.CostUSD != nil {
				cost = formatUSD(*t.CostUSD)
			}
			ts := "—"
			if !t.At.IsZero() {
				ts = t.At.Format("2006-01-02 15:04:05")
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n",
				ts, shortTurn(t.TurnID), t.Model,
				commaInt(t.InputTokens), commaInt(t.OutputTokens), cost)
		}
		_ = tw.Flush()
	}
}

func renderRangeCost(env *Env, r *proto.RangeCostTotals) {
	fmt.Fprintf(env.Stdout, "Range: %s .. %s\n",
		r.Start.Format(time.RFC3339), r.End.Format(time.RFC3339))
	fmt.Fprintf(env.Stdout, "  Total cost:    %s\n", formatUSD(r.CostUSD))
	fmt.Fprintf(env.Stdout, "  Total tokens:  in=%s out=%s\n",
		commaInt(r.InputTokens), commaInt(r.OutputTokens))
	fmt.Fprintf(env.Stdout, "  Turns:         %d\n", r.Turns)
	if r.HasUnknown {
		fmt.Fprintln(env.Stdout, "  (some turns used a model not in the price table; cost shown is partial)")
	}
	fmt.Fprintln(env.Stdout, "")
	if len(r.BySession) == 0 {
		fmt.Fprintln(env.Stdout, "No usage rows in this range.")
		return
	}
	fmt.Fprintln(env.Stdout, "By session:")
	tw := tabwriter.NewWriter(env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "  SESSION\tNAME\tSTATUS\tTURNS\tIN\tOUT\tCOST")
	for _, s := range r.BySession {
		cost := formatUSD(s.CostUSD)
		if s.HasUnknown {
			cost += "*"
		}
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			shortSession(s.SessionID), valueOr(s.Name, "—"), valueOr(s.Status, "—"),
			s.Turns, commaInt(s.InputTokens), commaInt(s.OutputTokens), cost)
	}
	_ = tw.Flush()
}

func formatUSD(v float64) string {
	return fmt.Sprintf("$%.2f", v)
}

func commaInt(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	digits := fmt.Sprintf("%d", n)
	parts := []byte{}
	for i, d := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			parts = append(parts, ',')
		}
		parts = append(parts, byte(d))
	}
	if neg {
		return "-" + string(parts)
	}
	return string(parts)
}

func shortTurn(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[len(id)-12:]
}

func shortSession(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[len(id)-12:]
}

func valueOr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
