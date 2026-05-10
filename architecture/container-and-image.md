# Container and base image

## 1. Base image

The base image is the **runtime + dev tooling** the agent runs inside.
It is **built locally on the developer's machine** by `agentctl init`
(and rebuilt by `agentctl update`); there is no OCI registry pull and
no publisher-side image distribution in v1. Skills are **not** baked
in; they are bind-mounted at session start (§2.4, ADR 0014).

### 1.1 Image identity and pinning

- **Local tag:** `agentctl/session-base:local`. Single tag, machine-local.
- **Pin:** the image's content-addressable ID
  (`docker inspect agentctl/session-base:local --format '{{.Id}}'`)
  is stored in `config.toml` `[image].pinned_id`. The previous ID
  is retained as `[image].previous_id` for rollback.
- **Local caching:** Docker's content store. Old image IDs accumulate
  there until `docker image prune` (manual; `agentctl update --gc` is
  post-v1).
- **No remote registry, no cosign verification of the image.** Trust
  derives from `install.sh` having signature-verified the build context
  (Dockerfile + shim source + entrypoint) before laying it down at
  `~/.local/share/agentctl/image/`.

### 1.2 Build context location

`install.sh` lays down the build context at:

```
~/.local/share/agentctl/image/
├── Dockerfile
├── shim/                       # Python source for the runtime shim
│   ├── __main__.py             # entrypoint; wires control sock ↔ claude-agent-sdk
│   ├── control.py              # NDJSON framing on the bind-mounted Unix socket
│   ├── runtime.py              # claude_agent_sdk integration
│   ├── repos.py                # --repo cloning + repo-bases.json
│   └── requirements.txt        # claude-agent-sdk, watchdog, ...
├── entrypoint                  # /usr/local/bin/agentctl-entrypoint
└── config-templates/           # /etc/agentctl/templates/*
```

Owner: same OS user as `agentd`. Mode `0755` on directories, `0644`
on files. A site-wide install (`INSTALL_DIR=/usr/local/bin`) places
this at `/usr/local/share/agentctl/image/` instead.

The `agentd` build context **does not contain skills** — those live
under sibling directories (`builtin-skills/`, `custom-skills/`) and
are mounted, not COPYed.

### 1.3 Layered build plan

The Dockerfile is split into layers from "rarely changes" to
"frequently changes" so rebuilds reuse cache aggressively:

```dockerfile
# Single-stage build. The shim is Python, so no compile step is needed;
# we COPY the source and `pip install` its dependencies.

# Layer 1 — base OS + system packages (rarely changes)
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client jq tini gosu \
      build-essential pkg-config \
      && rm -rf /var/lib/apt/lists/*

# Layer 2 — language runtimes the AGENT will use for coding work (occasional).
# The agent runtime itself is Python-based via claude-agent-sdk (Layer 3);
# Node and Python here are dev tooling for whatever projects the agent
# touches inside /work.
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    apt-get install -y --no-install-recommends python3 python3-pip python3-venv && \
    rm -rf /var/lib/apt/lists/*

# Layer 3 — agent runtime SDK (per-release)
# We use the Python claude-agent-sdk: the shim drives a model session
# in-process and streams typed events back to agentd over the control
# sock. No subprocess of a separate "claude-code" CLI; no parsing of
# stdout. See architecture/agentd.md and ADR 0014.
ARG CLAUDE_AGENT_SDK_VERSION
RUN python3 -m venv /opt/agentctl/venv && \
    /opt/agentctl/venv/bin/pip install --no-cache-dir \
        "claude-agent-sdk==${CLAUDE_AGENT_SDK_VERSION}" \
        watchdog
ENV PATH="/opt/agentctl/venv/bin:${PATH}"

# Layer 4 — runtime shim (Python source; per-release)
COPY shim/ /opt/agentctl/shim/
RUN /opt/agentctl/venv/bin/pip install --no-cache-dir -r /opt/agentctl/shim/requirements.txt

# Layer 5 — entrypoint and runtime config templates (per-release)
COPY entrypoint /usr/local/bin/agentctl-entrypoint
COPY config-templates /etc/agentctl/templates/
RUN useradd --create-home --uid 1000 --shell /bin/bash agent && \
    mkdir -p /work /skills /run/agentctl/control && \
    chown -R agent:agent /work /skills /home/agent /opt/agentctl && \
    chmod 0755 /run/agentctl/control

USER agent
WORKDIR /work
ENTRYPOINT ["/usr/sbin/tini", "--", "/usr/local/bin/agentctl-entrypoint"]
```

Notes:

- **No `COPY skills/` step.** `/skills` is created as an empty,
  agent-owned directory; it serves only as the bind-mount target for
  the per-session skills snapshot (§2.4).
- **No Go build stage.** The shim is Python; no compile step. The
  developer's host does not need a Go toolchain to build the image.
- **`claude-agent-sdk` is the agent runtime in v1.** The shim drives
  the SDK directly — no subprocess of a `claude-code` CLI, no
  stdout parsing. Typed events from the SDK map cleanly to the
  control-sock `runtime.event` frames defined in `api.md` §4.3.
- `tini` as PID 1 ensures clean signal forwarding; without it,
  `docker stop`'s SIGTERM would be eaten by the shim's child
  reaper. The shim itself runs as `tini`'s child.
- Build args (`CLAUDE_AGENT_SDK_VERSION`) are pinned by the build
  context's `Dockerfile`; the active value travels with the agentctl
  release.

### 1.4 What is **not** in the image

- No secrets. `ANTHROPIC_API_KEY` and `GITHUB_PAT` are injected at
  container start (§2.4).
- No MCP URLs. The MCP set for a session is injected per-start (§2.5).
- No model configuration. `AGENTCTL_MODEL` is injected.
- No host paths. The image has no awareness of the developer's
  filesystem.
- **No skills.** Built-in and custom skills are composed by `agentd` at
  session start and bind-mounted at `/skills/` (§2.4).

### 1.5 Trust and supply chain

- The Dockerfile, shim source, entrypoint, and config templates ship
  inside the same release tarball as the agentctl binary. `install.sh`
  verifies the tarball's signature against an embedded public key
  before extracting any of it (install-and-update.md §1.2).
- The locally-built image inherits trust from those signature-verified
  inputs. We do **not** sign the resulting local image; signing a build
  artifact whose entire input chain is already verified would add
  ceremony without improving the trust story.
- External inputs the build relies on:
  - `debian:bookworm-slim` from Docker Hub (authenticated by Docker's
    content trust if enabled; otherwise standard Docker pull semantics).
  - apt packages from official Debian repositories.
  - Node.js from `deb.nodesource.com`.
  - The agent runtime npm package from npm's registry.
  - `golang:1.23-bookworm` from Docker Hub (used only in stage A).

  These are standard supply-chain risks we do not attempt to eliminate
  in v1; reproducible-build hardening (pinned package digests, vendored
  dependencies, restricted pull sources) is a v2 concern.
- The local image ID is recorded on every session row
  (`sessions.image_id`) and surfaced in `agentctl ls --verbose` and the
  Web UI session detail.

## 2. Container creation parameters

`agentd` calls Docker with the following exhaustive set per session.
Reference: Docker SDK `containers.create()`. Where Docker has CLI flags
they are named; where it doesn't, the Engine API field is named.

### 2.1 Identity and labels

- **Name:** `agentctl-<short_session_id>` (last 8 chars of ULID for human
  recognizability). Labeled, so the canonical filter is by label.
- **Labels:**
  - `agentctl.session=<full_session_id>`
  - `agentctl.image_id=<sha256:…>`
  - `agentctl.created_at=<RFC3339>`
  - `agentctl.user=<os_user>` (informational)

### 2.2 Network mode

- Per-session bridge network: `docker network create
  --driver=bridge --label agentctl.session=<id> --opt
  com.docker.network.bridge.enable_icc=false agentctl-<short_id>`.
- The container is attached to **only** this network. No `host`, no
  default `bridge`. ICC (inter-container communication) on the network is
  off — even if two containers somehow ended up on the same network they
  couldn't talk. This is the sole network-level isolation v1 enforces;
  it satisfies R7's peer-isolation requirement and costs nothing beyond
  Docker config.
- `enable_ip_masquerade` is on (default) so egress works through the host
  NAT. The container can reach the public internet, the developer's LAN,
  and the host's loopback. Strict egress filtering is deferred to v2
  (`v2-requirements.md` §V2.1).
- IPv6 disabled at the network level for v1 (default Docker; we reaffirm
  it).

The runtime can resolve external DNS names via the host's resolver
(Docker injects `/etc/resolv.conf`).

### 2.3 Resource caps and runtime flags

| Flag | Default | Override | Why |
|---|---|---|---|
| `--memory` | `4g` | `agentctl start --mem-limit`, `config.session.mem_limit` | R7 isolation; avoids host swap. |
| `--memory-swap` | equal to `--memory` (no swap) | n/a | Don't let one session swap us into the dirt. |
| `--cpus` | `2.0` | `--cpu-limit`, `config.session.cpu_limit` | Scaler from §4. |
| `--pids-limit` | `512` | n/a | Fork-bomb defense. |
| `--read-only` | true | n/a | Root fs read-only; only `/work`, `/tmp`, `/run/agentctl/control` are writable (the latter via bind). `/home/agent` is on a tmpfs (next row). |
| `--tmpfs /home/agent:size=512m,mode=0755,uid=1000,gid=1000` | always | n/a | Runtime needs a writable HOME without leaking into the volume. |
| `--security-opt no-new-privileges` | always | n/a | Belt-and-suspenders for `setuid` binaries baked into the image. |
| `--cap-drop ALL` | always | n/a | Drop all caps; runtime needs none for normal coding work. |
| `--cap-add` | none | n/a | Reserved if a future skill needs e.g. `NET_RAW`; not granted in v1. |
| `--restart=no` | always | n/a | `agentd` is the lifecycle owner (§3 of requirements). |

### 2.4 Mounts

| Source | Target | Mode | Purpose |
|---|---|---|---|
| `~/.local/share/agentctl/sessions/<id>/volume/` | `/work` | `rw` | The session volume. Owned by uid 1000 to match the `agent` user. |
| `~/.local/share/agentctl/sessions/<id>/control/` | `/run/agentctl/control/` | `rw` | Control socket dir. Only path that touches host loopback equivalent. |
| `~/.local/share/agentctl/sessions/<id>/skills/` | `/skills/` | `ro` | Per-session skills snapshot composed at start by `agentd` from the install's built-in skills + the developer's custom skills. Frozen for the session's lifetime; live reload is v2. |

No other host paths. In particular: not `/var/run/docker.sock`, not
`~/.config/agentctl`, not `/etc/passwd`, not the secrets file. The
host-side `~/.local/share/agentctl/builtin-skills/` and `custom-skills/`
directories are **not** mounted directly — only the per-session
snapshot is.

#### Skills snapshot composition

At session start, `agentd`:

1. Creates `sessions/<id>/skills/` (empty).
2. Walks `~/.local/share/agentctl/builtin-skills/` and copies each
   skill subdirectory into the snapshot (cp `-r`; on COW filesystems
   this is fast via reflink when available).
3. Walks `~/.local/share/agentctl/custom-skills/` and copies each
   skill subdirectory into the snapshot. On name collision with a
   built-in: the custom version replaces the built-in in the snapshot,
   and `agentd` emits a `skill.collision { name, overrides:
   "builtin" }` event so attached clients can surface it.
4. Computes the sha256 of the snapshot tree (sorted file list +
   contents) and stores it on the session row as
   `skills_snapshot_hash`. The snapshot path is stored as
   `skills_snapshot_path`.
5. Bind-mounts the snapshot read-only into the container at `/skills/`.

Cleanup: the snapshot is removed when the session is `TerminateSession`'d
(part of the per-session-dir teardown described in data-model.md §4).

### 2.5 Environment variables

Set at create time, never via mounted file at runtime (we use
`--env-file` from the per-session `secrets.env` so secrets aren't on the
docker command line):

| Var | Value | Source |
|---|---|---|
| `ANTHROPIC_API_KEY` | from secrets.json | per-session secrets.env |
| `GITHUB_TOKEN` | from secrets.json | per-session secrets.env |
| `SESSION_ID` | full ULID | per-session secrets.env |
| `SESSION_NAME` | from create request | per-session secrets.env |
| `AGENTCTL_MODEL` | the model id | per-session secrets.env |
| `AGENTCTL_SESSION_TOKEN` | session_token (api.md §4.4) | per-session secrets.env |
| `HOME` | `/home/agent` | image |
| `LANG`, `LC_ALL` | `C.UTF-8` | image |
| `XDG_CACHE_HOME` | `/work/.cache` | image |
| `GIT_TERMINAL_PROMPT` | `0` | image (no interactive prompts on git ops) |
| `GIT_AUTHOR_NAME`, `GIT_AUTHOR_EMAIL` | derived from PAT user.email/name (queried at init) | per-session secrets.env |

Two helper files are written into `/work/.config/git/` at session create
by the shim before runtime starts:

- `credentials` — `https://x-access-token:<PAT>@github.com\n` mode `0600`.
- `config` — `[credential] helper = store --file=/work/.config/git/credentials`.

This is what makes `git clone <github URL>` "just work" with the PAT
without inlining it in command output (R3 acceptance criterion).

### 2.6 Entrypoint and runtime invocation

The image's `ENTRYPOINT` is `/usr/local/bin/agentctl-entrypoint`. It runs
as `agent` (uid 1000) and:

1. Reads `secrets.env` already exposed via `--env-file`, plus a
   `session.json` written into the control dir at create time
   (mode `0640`).
2. For each repo in `repos`: `git clone <url> /work/<basename>`; record
   `git rev-parse HEAD` and current branch. Errors are captured but do
   not abort start; the shim emits `runtime.error{fatal:false}` and
   leaves the repo placeholder absent.
3. Connects to `/run/agentctl/control/agentd.sock` and sends
   `runtime.hello` with `session_token` (verified by agentd).
4. Receives `agentd.greet` with the resolved MCP set (each entry carries
   `url`, `transport` (`http`/`sse`/…), `kind`, and any auth-derived
   headers). MCP entries whose `transport` or `kind` are not recognized
   by this image's runtime are dropped from the rendered config and
   reported as `mcp.skipped` events.
5. Hands control to the Python shim:

   ```bash
   exec /opt/agentctl/venv/bin/python -m shim
   ```

   (`tini` is PID 1 from the image ENTRYPOINT; the shim runs as
   `tini`'s child via `exec`.) The shim is the long-running process
   in the container for the rest of the session.

6. Inside the shim (`shim/__main__.py`):

   - Configures `claude-agent-sdk` with `model=AGENTCTL_MODEL`,
     `permission_mode=bypass` (R3 §15.1), the per-session MCP set,
     `cwd=/work`, and the skills directory at `/skills/`.
   - On each `agentd.message` frame received over the control sock,
     drives an SDK session turn — typically by calling the SDK's
     async query/stream interface — and translates each yielded SDK
     event into the corresponding `runtime.event` frame:
     - SDK assistant deltas → `runtime.event{kind="assistant.delta"}`
     - Tool calls and results → `runtime.event{kind="tool.call|tool.result"}`
     - Per-turn usage block → `runtime.event{kind="usage", model, input_tokens, output_tokens, ...}`
     - End of turn → `runtime.event{kind="turn.end"}`
   - Maintains the SDK's conversation history on the volume at
     `/work/.history/` (the SDK's persistence target), so resume
     after idle-stop reads that history back.

7. Listens for control-sock inbound: `agentd.message` enqueues a turn
   into the SDK; `agentd.interrupt` cancels the in-flight SDK stream
   (the SDK exposes a cancel handle); `agentd.shutdown` initiates
   graceful exit with a 30s grace, then SIGKILL.
8. Watches `/work/<repo>/` (excluding `.git/objects`) via `watchdog`
   and emits throttled `repo.changed` events on the control sock.

If the shim exits with a non-zero status, agentd's read loop sees the
control sock close. Docker reports the container as exited; `agentd`
marks the session `stopped` (or `error` if exit was unclean per the
shim's last `runtime.error` frame).

### 2.7 Working directory and user

- `WORKDIR /work`. The runtime's `cwd` is `/work` so plain "look at the
  repo I cloned" works without configuration.
- All container processes run as uid 1000 (`agent`). Root inside the
  container is unused; `--cap-drop ALL` plus `no-new-privileges` make
  privilege escalation as an attacker the same as on the host.

## 3. Diff and export inside the container

R8 requires `agentctl diff` and `agentctl export` to work without
attaching to the container. Implementation:

- The shim records each repo's clone-time SHA and branch in
  `/work/.history/repo-bases.json`.
- `Diff` op: `agentd` issues `agentd.diff_request{repo}` on the control
  sock; the shim runs `git -C /work/<repo> diff --no-color
  <recorded_base_sha>` (and `git ls-files --others --exclude-standard`
  for untracked, formatted as patch) and streams stdout back as
  `runtime.diff_chunk` events that agentd forwards as the response body.
- `ExportPatch`: same as `Diff` but with `--patch` formatting.
- `ExportPush`: shim runs:

  ```bash
  cd /work/<repo>
  git checkout -B <branch>
  git add -A
  git commit -m "<msg>" || true   # tolerate "nothing to commit"
  git push -u origin <branch>
  ```

  Output is streamed back; non-zero exit is an `error` response.

`agentd` does **not** attempt to run `git` itself or shell into the
container. All git operations live in the shim where credentials are
already wired.

## 4. Network posture (v1)

v1 ships with Docker-native isolation only. Two requirements are met:

- **Peer isolation.** Each session has its own bridge network with
  `enable_icc=false` (§2.2). Two session containers cannot reach each
  other by hostname or IP.
- **No inbound exposure.** No ports are published from session
  containers; nothing on the host or LAN can connect *into* a session.

What v1 does **not** restrict:

- Outbound egress to the public internet, the developer's LAN, or the
  host's loopback. The container can `curl` arbitrary URLs.
- Reaching `agentd`'s admin API on `127.0.0.1:7777` over the host
  bridge gateway. This is gated by the bearer token in
  `~/.config/agentctl/web_token`, which the container has no
  bind-mount to read; without the token every `/v1/*` request returns
  `401`.

Strict outbound egress allowlisting (Anthropic / GitHub / configured
MCPs only) is deferred to v2. See `v2-requirements.md` §V2.1 for the
goal and the previously proposed iptables-based design.

## 5. Container teardown

- `agentd.shutdown` → SIGTERM via the shim, 30s grace, then `docker stop
  -t 5` then `docker kill`.
- After exit: `docker rm -f` only on `TerminateSession`. Idle-stop leaves
  the container around (`docker ps -a` shows it) so resume is a fast
  `docker start`. We `docker rm` only on terminate or when reconcile
  finds an orphan.
- The per-session network is removed on `TerminateSession`. On idle-stop
  we keep the network around; same speedup logic.
- Tombstones: see `data-model.md` §4.

## 6. Why no host bind-mounts (§15.2 reminder)

The natural temptation is "just bind-mount the developer's
`~/code/myrepo` into `/work/myrepo`." We explicitly do not, because:

- A runaway agent could write the host source tree (auto-approval
  tools, §15.1).
- Two sessions cannot then share isolation guarantees (different mounts
  could overlap).
- The blast radius escapes the session volume — `agentctl stop` no
  longer "destroys all session state." That defeats R6.

`--repo <url>` and in-session `git clone` are sufficient for v1; a future
"workspace mount with explicit risk acknowledgement" is a v2 design call.
