package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/agentctl/agentctl/internal/cliclient"
	"github.com/agentctl/agentctl/internal/proto"
)

func runExport(_ context.Context, env *Env, args []string) int {
	fs := flag.NewFlagSet("export", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	patch := fs.Bool("patch", false, "export the working-tree diff as a .patch file (path optional)")
	push := fs.String("push", "", "push the working tree to the given branch via the recorded remote")
	repo := fs.String("repo", "", "repo name (default: all for --patch, first repo for --push)")
	message := fs.String("message", "", "commit message used by --push (default: agentctl session changes)")
	fs.Usage = func() {
		fmt.Fprintln(env.Stderr, "Usage:")
		fmt.Fprintln(env.Stderr, "  agentctl export <session> --patch [path]")
		fmt.Fprintln(env.Stderr, "  agentctl export <session> --push <branch> [--repo NAME] [--message MSG]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() < 1 {
		fs.Usage()
		return ExitUsage
	}
	sessionID := fs.Arg(0)
	if !*patch && *push == "" {
		fs.Usage()
		return ExitUsage
	}
	if *patch && *push != "" {
		fmt.Fprintln(env.Stderr, "export: --patch and --push are mutually exclusive")
		return ExitUsage
	}
	c, err := cliclient.Dial(env.Layout.SocketFile, 3*time.Second)
	if err != nil {
		fmt.Fprintf(env.Stderr, "export: %v\n", err)
		return ExitEnvironment
	}
	defer func() { _ = c.Close() }()

	if *patch {
		return runExportPatch(c, env, sessionID, *repo, fs.Args()[1:])
	}
	return runExportPush(c, env, sessionID, *repo, *push, *message)
}

func runExportPatch(c *cliclient.Client, env *Env, sessionID, repo string, rest []string) int {
	dest := ""
	if len(rest) > 0 {
		dest = rest[0]
	}
	switch {
	case dest == "":
		return streamDiff(c, env.Stdout, env.Stderr, proto.OpExportPatch, proto.DiffRequest{
			SessionID: sessionID, Repo: repo, Format: "unified",
		})
	case strings.HasSuffix(dest, "/"):
		return writePatchPerRepo(c, env, sessionID, repo, dest)
	default:
		fi, err := os.Stat(dest)
		if err == nil && fi.IsDir() {
			return writePatchPerRepo(c, env, sessionID, repo, dest)
		}
		f, err := os.Create(dest)
		if err != nil {
			fmt.Fprintf(env.Stderr, "export: %v\n", err)
			return ExitGeneric
		}
		defer func() { _ = f.Close() }()
		return streamDiff(c, f, env.Stderr, proto.OpExportPatch, proto.DiffRequest{
			SessionID: sessionID, Repo: repo, Format: "unified",
		})
	}
}

func writePatchPerRepo(c *cliclient.Client, env *Env, sessionID, repo, dir string) int {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fmt.Fprintf(env.Stderr, "export: %v\n", err)
		return ExitGeneric
	}
	stream, err := c.StartStream(proto.OpExportPatch, proto.DiffRequest{
		SessionID: sessionID, Repo: repo, Format: "unified",
	})
	if err != nil {
		fmt.Fprintf(env.Stderr, "export: %v\n", err)
		return ExitGeneric
	}
	defer stream.Close()
	files := map[string]*os.File{}
	defer func() {
		for _, f := range files {
			_ = f.Close()
		}
	}()
	exit := ExitOK
	for {
		fr := stream.Recv()
		if fr.Err != nil {
			fmt.Fprintf(env.Stderr, "export: %v\n", fr.Err)
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
			continue
		}
		if ch.End {
			f, ok := files[ch.Repo]
			if ok {
				_ = f.Close()
				delete(files, ch.Repo)
			}
			if ch.Error != "" {
				fmt.Fprintf(env.Stderr, "export: %s: %s\n", ch.Repo, ch.Error)
				exit = ExitRuntime
			}
			continue
		}
		if len(ch.Data) == 0 {
			continue
		}
		f, ok := files[ch.Repo]
		if !ok {
			path := filepath.Join(dir, ch.Repo+".patch")
			nf, err := os.Create(path)
			if err != nil {
				fmt.Fprintf(env.Stderr, "export: %v\n", err)
				return ExitGeneric
			}
			files[ch.Repo] = nf
			f = nf
			fmt.Fprintf(env.Stdout, "wrote %s\n", path)
		}
		_, _ = f.Write(ch.Data)
	}
}

func runExportPush(c *cliclient.Client, env *Env, sessionID, repo, branch, message string) int {
	if repo == "" {
		repos, err := fetchSessionRepos(c, sessionID)
		if err != nil {
			fmt.Fprintf(env.Stderr, "export: %v\n", err)
			return ExitGeneric
		}
		if len(repos) == 0 {
			fmt.Fprintln(env.Stderr, "export: no repos in session")
			return ExitSessionState
		}
		repo = repos[0].Name
	}
	var resp proto.ExportPushResponse
	if err := c.Call(proto.OpExportPush, proto.ExportPushRequest{
		SessionID: sessionID, Repo: repo, Branch: branch, Message: message,
	}, &resp, 5*time.Minute); err != nil {
		fmt.Fprintf(env.Stderr, "export: %v\n", err)
		if apiErr, ok := err.(*cliclient.APIError); ok && apiErr.Code == proto.ErrNotFound {
			return ExitSessionState
		}
		return ExitGeneric
	}
	if resp.Output != "" {
		fmt.Fprintln(env.Stdout, strings.TrimRight(resp.Output, "\n"))
	}
	if !resp.Success {
		if resp.Error != "" {
			fmt.Fprintf(env.Stderr, "export: %s\n", resp.Error)
		}
		return ExitRuntime
	}
	fmt.Fprintf(env.Stdout, "pushed %s on %s\n", resp.Branch, resp.Repo)
	return ExitOK
}

func fetchSessionRepos(c *cliclient.Client, sessionID string) ([]proto.RepoState, error) {
	var resp proto.ListSessionReposResponse
	if err := c.Call(proto.OpListSessionRepos, proto.ListSessionReposRequest{SessionID: sessionID}, &resp, 5*time.Second); err != nil {
		return nil, err
	}
	return resp.Repos, nil
}
