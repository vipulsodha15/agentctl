# agentctl

Run AI coding-agent sessions on your own machine. Each session lives in its
own Docker container with the Claude Agent SDK, your MCP servers, your
skills, and (optionally) a clone of your repo. The CLI and Web UI both
talk to a single local daemon (`agentd`) that owns the database and
container lifecycle, so you can detach from a session and reattach
later without losing state.

## Why agentctl

- **Sandboxed by default.** Every session runs in its own Docker
  container on its own bridge network with its own working volume.
  The agent never touches your host filesystem outside the repo you
  hand it, and one session can't see another. Stop the session and
  the blast radius goes with it.
- **You control the toolbox.** Skills and MCP servers are first-class
  and explicit. You register MCP servers in a local registry and
  attach them to a session by name; skills are folders you drop into
  `~/.local/share/agentctl/custom-skills/` and edit in place. No
  hidden tool surface, no "what is this agent allowed to do?"
  guesswork.
- **Curate narrow agents.** Instead of one omniscient agent, define
  small, role-scoped agents (e.g. `bug-investigator`,
  `bug-planner`, `bug-executor`) with their own system prompts, tool
  allow-lists, and skill sets. Narrow agents stay on task and are
  cheaper to reason about.
- **Assembly-line workflows for tasks.** Chain those narrow agents
  into a workflow (see
  `internal/ttl/builtins/assembly-lines/bug.yaml` for a worked
  example). A task moves through stages — investigate → plan →
  execute → review — with each stage owned by the right agent and
  the right tools.
- **Mix providers across the line.** Each stage can pin its own
  provider (`anthropic`, `openai`), so one assembly line can
  investigate on Claude and execute on OpenAI Codex without forking
  agent YAMLs. The shipped `bug-multi-provider` built-in
  (`internal/ttl/builtins/assembly-lines/bug-multi-provider.yaml`)
  is the reference example; the run view shows each stage's
  provider/model as a first-class chip when stages differ, and
  stays invisible when they don't.
- **Explicit handoff between stages.** When one stage finishes, run
  `agentctl task handoff <id>` (or click Handoff in the UI) to
  advance the task to the next agent, carrying the prior stage's
  output forward as context. You stay in the loop at every seam
  instead of letting one agent drift end-to-end.
- **CLI and Web UI, same daemon.** Drive everything from your
  terminal (`agentctl start`, `attach`, `ls`, `task handoff`, …) or
  from the local Web UI at `http://127.0.0.1:7777` (`agentctl ui`).
  Both clients talk to the same `agentd` over the same internal API,
  so a session you started on the CLI shows up in the UI and vice
  versa — pick whichever fits the moment.

---

## Install

Requires Docker (Desktop or Engine) and a Linux/macOS host. Then:

    bash installer/install.sh
    agentctl init

`init` does the first-time setup end-to-end: Docker reachability check,
session base image build, prompts for credentials, MCP registry seed,
and `systemd --user` / `launchd` service install. It is idempotent —
re-run it any time to repair drift.

To upgrade an existing checkout — pull the latest changes, rebuild,
and restart the daemon in one step:

    bash installer/update.sh

## Authenticate with Claude

agentctl supports **two ways** to authenticate sessions. Pick whichever
matches how you already use Claude:

### Option A — Claude subscription (recommended if you have Pro/Max)

    agentctl auth login

This builds a one-shot helper container, runs `claude auth login` inside
it, and stores the OAuth credentials under
`~/.config/agentctl/claude/.credentials.json`. The browser flow opens on
your host; paste the code back into the terminal. From then on, every
session bind-mounts those credentials and authenticates as you — no API
key billing, no `ANTHROPIC_API_KEY` env var.

Check what's configured at any time:

    agentctl auth status

### Option B — Anthropic API key

If `init` doesn't find OAuth credentials, it prompts for
`ANTHROPIC_API_KEY` and validates it with a minimal authenticated
request. The key lives in `~/.config/agentctl/secrets.json` (`0600`) and
is injected into each session container at start.

You can pre-supply it non-interactively:

    agentctl init --anthropic-key sk-ant-…

### Option C — Custom Anthropic-compatible gateway

For self-hosted LLM gateways:

    agentctl init --anthropic-base-url https://gw.example/v1 \
                  --anthropic-auth-token <bearer>

### Switching auth methods later

- API key → subscription: run `agentctl auth login`.
- Subscription → API key: run `agentctl init --reset-token anthropic`.

## Start your first session

    agentctl start --repo https://github.com/me/myrepo.git

An interactive console opens. Type messages, watch the agent work, hit
Ctrl-D to detach (the session keeps running). Reattach any time:

    agentctl ls
    agentctl attach <session-id>

Prefer a UI?

    agentctl ui

…opens `http://127.0.0.1:7777` in your browser. The Web UI mirrors the
CLI: list sessions, open a console, view diffs against the recorded
base SHA, push branches, browse cost.

## Skills and MCP servers

**Skills** are bind-mounted into every session container at `/skills/`.
Two sources are combined:

- `~/.local/share/agentctl/builtin-skills/` — curated baseline shipped
  with installs.
- `~/.local/share/agentctl/custom-skills/` — your own.

Skill edits do **not** require an image rebuild — they take effect at
the next session start, or use `agentctl restart <session>` to pick
them up on a running session.

**MCP servers** are kept in a registry and attached to sessions
explicitly:

    agentctl mcp add ...
    agentctl mcp ls
    agentctl start --repo … --mcp my-server

See `agentctl mcp --help` and `agentctl skill --help` for the full set
of subcommands.

## Commands at a glance

| Command          | Purpose                                                   |
|------------------|-----------------------------------------------------------|
| `init`           | First-time setup; idempotent repair.                      |
| `auth login`     | Authenticate with your Claude subscription (OAuth).       |
| `auth status`    | Show whether sessions use an API key or OAuth.            |
| `update`         | Rebuild the session base image and repin its id.          |
| `config`         | Read or write a `config.toml` key.                        |
| `ui`             | Open the local Web UI in a browser.                       |
| `start`          | Create a session and attach to its event stream.          |
| `attach`         | Attach to a running session's event stream.               |
| `detach`         | Help text: detach is Ctrl-D / Ctrl-C from start/attach.   |
| `ls`             | List sessions.                                            |
| `stop`           | Terminate a session and remove its container + volume.    |
| `restart`        | Recreate a session container from the pinned image.       |
| `interrupt`      | Cancel a session's in-flight turn.                        |
| `logs`           | Tail daemon, session, or container logs.                  |
| `mcp`            | Manage the MCP registry.                                  |
| `skill`          | Manage built-in and custom skills.                        |
| `doctor`         | Run install + connectivity checks (`--fix`, `--repair-db`).|
| `version`        | Print version info.                                       |

Run `agentctl <command> --help` for command-specific flags.

## Troubleshooting

First stop:

    agentctl doctor

…prints the state of every install + connectivity check. Useful flags:

- `--fix` — apply known repairs idempotently.
- `--repair-db` — vacuum the sqlite database.
- `--json` — machine-readable output for scripting.

See [`TROUBLESHOOTING.md`](TROUBLESHOOTING.md) for known failure modes
and recipes.

## Architecture (one-paragraph version)

agentctl is a single Go binary that runs as either the CLI or, when
launched by systemd/launchd, the `agentd` daemon. agentd owns sqlite
(`~/.local/share/agentctl/agentd.db`), the Docker SDK, and a
per-session actor that orchestrates the container lifecycle plus a
control-channel socket bind-mounted into each container. Full details:
[`architecture/overview.md`](architecture/overview.md).

## Developing

    git clone https://github.com/agentctl/agentctl.git
    cd agentctl
    bash installer/install.sh           # lays down image build context + binary
    go test ./...
    cd web && npm ci && npm run build   # rebuild the SPA bundle

Repository layout:

- `cmd/agentctl/`   — single-binary entry point (CLI + agentd).
- `internal/`       — Go packages.
- `image/`          — Docker build context for the session base image.
- `builtin-skills/` — curated baseline skills shipped with installs.
- `web/`            — React + Vite SPA served by agentd.
- `architecture/`   — design docs and ADRs.
- `installer/`      — `install.sh` and signature payload.

## License

TBD.
