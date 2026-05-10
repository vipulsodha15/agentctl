# agentctl

Local AI coding-agent sessions on your machine.

## Quick start

    bash installer/install.sh
    agentctl init
    agentctl start --repo https://github.com/me/myrepo.git
    # an interactive session opens; type messages, watch the agent work

`agentctl init` walks through the first-time setup: a Docker check, a
local image build, prompts for `ANTHROPIC_API_KEY` and `GITHUB_PAT`,
seeds the MCP registry, installs a `systemd --user` service (or
`launchd` on macOS), and waits for the daemon to come up. Re-running
`init` is idempotent and acts as a repair.

## What it does

agentctl runs each AI coding session inside its own Docker container
with its own working volume and bridge network. The session has the
Claude Agent SDK, MCP servers you've registered, your Skills, and (if
you passed `--repo`) a clone of one or more git repos. Sessions are
durable: stopping `agentctl` does not stop the daemon, and the daemon
restarts cleanly across reboots, picking up running containers it
recognises by label.

The Web UI at `http://127.0.0.1:7777` (open it via `agentctl ui`)
mirrors the CLI: list sessions, open a console, view diffs against
the recorded base SHA, push branches, browse cost. The CLI and the
UI talk to the same `agentd` daemon over the same internal API.

MCP servers are stored in a registry (`agentctl mcp ...`) and
attached to sessions explicitly. Skills are bind-mounted into each
session's container at `/skills/`; built-in skills come from
`~/.local/share/agentctl/builtin-skills/`, custom skills from
`~/.local/share/agentctl/custom-skills/`. Skill changes do not
require an image rebuild — they take effect at the next session
start (or `agentctl restart <session>` to pick them up on a running
session).

## Architecture

agentctl is a single Go binary that runs as either the CLI or, when
launched by systemd / launchd, the `agentd` daemon. agentd owns
sqlite (`~/.local/share/agentctl/agentd.db`), the Docker SDK, and a
per-session actor that orchestrates the container lifecycle and a
control-channel socket bind-mounted into each container. See
`architecture/overview.md` for the full picture.

## Commands at a glance

| Command          | Purpose                                                   |
|------------------|-----------------------------------------------------------|
| `init`           | Set up agentctl on this machine.                          |
| `update`         | Rebuild the session base image and repin its id.          |
| `config`         | Read or write a config.toml key.                          |
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

Run `agentctl <command> --help` for command-specific flags and
examples.

## Troubleshooting

See `TROUBLESHOOTING.md` for common failure modes, or run

    agentctl doctor

which prints the state of every install + connectivity check. Add
`--fix` to apply known repairs idempotently, `--repair-db` to vacuum
the sqlite database, and `--json` for machine-readable output.

## Developing

    git clone https://github.com/agentctl/agentctl.git
    cd agentctl
    bash installer/install.sh           # lays down image build context + binary
    go test ./...
    cd web && npm ci && npm run build   # rebuild the SPA bundle

The repository layout:

- `cmd/agentctl/`  - the single-binary entry point (CLI + agentd).
- `internal/`      - Go packages.
- `image/`         - Docker build context for the session base image.
- `builtin-skills/` - the curated baseline skills shipped with installs.
- `web/`           - the React + Vite SPA served by agentd.
- `architecture/`  - design docs and ADRs.
- `installer/`     - `install.sh` and signature payload.

## License

TBD.
