package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runDiff(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("diff", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	repo := fs.String("repo", "", "repo name (default: all repos in the session)")
	stat := fs.Bool("stat", false, "show diff --stat instead of unified diff")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage: agentctl diff <session> [--repo NAME] [--stat]")
		fs.PrintDefaults()
		fmt.Fprintln(env.Stderr, "")
		fmt.Fprintln(env.Stderr, "Streams the working-tree diff against the recorded base SHA per repo.")
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "diff: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()
	format := "unified"
	if *stat {
		format = "stat"
	}
	return streamDiff(c, env.Stdout, env.Stderr, proto.OpDiff, proto.DiffRequest{
		SessionID: sessionID, Repo: *repo, Format: format,
	})
}

func streamDiff(c *cliclient.Client, stdout, stderr io.Writer, op string, req proto.DiffRequest) int {
	stream, err := c.StartStream(op, req)
	if err != nil {
		fmt.Fprintf(stderr, "diff: %v\n", err)
		return ExitGeneric
	}
	defer stream.Close()
	exit := ExitOK
	for {
		fr := stream.Recv()
		if fr.Err != nil {
			if apiErr, ok := fr.Err.(*cliclient.APIError); ok {
				if apiErr.Code == proto.ErrNotFound {
					fmt.Fprintf(stderr, "diff: %v\n", apiErr)
					return ExitSessionState
				}
				fmt.Fprintf(stderr, "diff: %v\n", apiErr)
				return ExitGeneric
			}
			fmt.Fprintf(stderr, "diff: %v\n", fr.Err)
			return ExitGeneric
		}
		if fr.EndCode != "" {
			return exit
		}
		if fr.Kind != "chunk" {
			continue
		}
		var ch proto.DiffChunkData
		if err := json.Unmarshal(fr.Data, &ch); err != nil {
			fmt.Fprintf(stderr, "diff: malformed chunk: %v\n", err)
			return ExitGeneric
		}
		if ch.End {
			if ch.Note != "" {
				fmt.Fprintf(stderr, "diff: %s: %s\n", ch.Repo, ch.Note)
			}
			if ch.Error != "" {
				fmt.Fprintf(stderr, "diff: %s: %s\n", ch.Repo, ch.Error)
				exit = ExitRuntime
			}
			continue
		}
		if len(ch.Data) > 0 {
			_, _ = stdout.Write(ch.Data)
		}
	}
}
