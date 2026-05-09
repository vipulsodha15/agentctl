# Container and base image

## 1. Base image

The base image is the unit of "what the agent has at its fingertips."
Updates ship a new image; sessions adopt the new image on next
stop+resume cycle (§15.8).

### 1.1 Image identity and pinning

- **Repository:** `agentctl/session-base` (placeholder; team replaces in
  registry seed). Always pulled by digest in v1.
- **Tag scheme:** `vN.YYYY-MM-DD` (e.g. `v1.2026-05-01`). Tags are mutable
  for convenience; what's authoritative is the digest stored in
  `config.toml` `[image].pinned_digest`.
- **Distribution:** any OCI-compliant registry. `agentctl init` pulls
  the configured ref and writes `pinned_digest`.
- **Local caching:** Docker's content store. We do not garbage-collect
  old images automatically; `agentctl update --gc` (post-v1) would.

### 1.2 Layered build plan

The Dockerfile is split into layers from "rarely changes" to "frequently
changes" so updates ship small diffs:

```dockerfile
# Layer 1 — base OS + system packages (rarely changes)
FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates curl git openssh-client jq tini gosu \
      build-essential pkg-config \
      && rm -rf /var/lib/apt/lists/*

# Layer 2 — language runtimes (occasional)
RUN curl -fsSL https://deb.nodesource.com/setup_20.x | bash - && \
    apt-get install -y --no-install-recommends nodejs && \
    apt-get install -y --no-install-recommends python3 python3-pip python3-venv && \
    rm -rf /var/lib/apt/lists/*

# Layer 3 — agent runtime + shim (per-release)
ARG AGENT_RUNTIME_VERSION
RUN npm install -g @anthropic-ai/claude-code@${AGENT_RUNTIME_VERSION}
COPY --from=shim-build /out/agent-runtime-shim /usr/local/bin/
COPY --from=shim-build /out/agent-runtime-shim.sha256 /usr/local/share/agentctl/

# Layer 4 — skills (per-release, frequent)
COPY skills/ /skills/
RUN chmod -R a-w /skills/

# Layer 5 — entrypoint and runtime config templates (per-release)
COPY entrypoint /usr/local/bin/agentctl-entrypoint
COPY config-templates /etc/agentctl/templates/
RUN useradd --create-home --uid 1000 --shell /bin/bash agent && \
    mkdir -p /work /run/agentctl/control && \
    chown -R agent:agent /work /home/agent && \
    chmod 0755 /run/agentctl/control

USER agent
WORKDIR /work
ENTRYPOINT ["/usr/sbin/tini", "--", "/usr/local/bin/agentctl-entrypoint"]
```

Why these splits:

- Layer 4 (skills) changes most often. Keeping it late minimizes pull
  size when only skills update.
- The runtime shim lives in layer 3 with the runtime since they ship
  together (`api.md` §4.6).
- `tini` as PID 1 ensures clean signal forwarding to the runtime; without
  it, `docker stop`'s SIGTERM would be eaten by the shim's child reaper.

### 1.3 Skills packaging

- Skills are directories under `/skills/<skill-name>/` containing a
  `manifest.json` (name, description, args schema) plus the skill's
  implementation files.
- The runtime auto-loads everything in `/skills/`. `/help` (R9) reads the
  manifests to render descriptions.
- The container makes `/skills` read-only after the COPY (`chmod -R
  a-w`); the agent cannot modify or add skills at runtime (R9 OOS).

### 1.4 What is **not** in the image

- No secrets. `ANTHROPIC_API_KEY` and `GITHUB_PAT` are injected at
  container start (§2.4).
- No MCP URLs. The MCP set for a session is injected per-start (§2.5).
- No model configuration. `AGENTCTL_MODEL` is injected.
- No host paths. The image has no awareness of the developer's
  filesystem.

### 1.5 Image distribution and supply chain

- We sign the image with cosign (keyless, OIDC). `agentctl init` and
  `agentctl update` verify signature against the configured public key in
  `config.toml` `[image].cosign_identity`. Sig-verify failure aborts the
  pull.
- SBOM (CycloneDX) shipped as an OCI artifact alongside the image; not
  consumed by v1, present for auditability.
- Image tags and digests are emitted in `agentctl ls --verbose` and the
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
  - `agentctl.image_digest=<sha256:…>`
  - `agentctl.created_at=<RFC3339>`
  - `agentctl.user=<os_user>` (informational)

### 2.2 Network mode

- Per-session bridge network: `docker network create
  --driver=bridge --label agentctl.session=<id> --opt
  com.docker.network.bridge.enable_icc=false agentctl-<short_id>`.
- The container is attached to **only** this network. No `host`, no
  default `bridge`. ICC (inter-container communication) on the network is
  off — even if two containers somehow ended up on the same network they
  couldn't talk.
- `enable_ip_masquerade` is on (default) so egress works through the host
  NAT.
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

No other host paths. In particular: not `/var/run/docker.sock`, not
`~/.config/agentctl`, not `/etc/passwd`, not the secrets file.

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

1. Reads `/run/agentctl/control/session.json` (bind-mounted? No — see
   below; the shim reads `secrets.env` already exposed via env, plus a
   `session.json` written into the control dir at create time, mode
   `0640`).
2. For each repo in `repos`: `git clone <url> /work/<basename>`; record
   `git rev-parse HEAD` and current branch. Errors are captured but do
   not abort start; the shim emits `runtime.error{fatal:false}` and
   leaves the repo placeholder absent.
3. Connects to `/run/agentctl/control/agentd.sock` and sends
   `runtime.hello` with `session_token` (verified by agentd).
4. Receives `agentd.greet` with the resolved MCP set + headers.
5. Writes the runtime config file (`/home/agent/.config/agent/config.json`)
   from the template `/etc/agentctl/templates/config.json.tmpl`,
   substituting MCP URLs and any per-session settings.
6. Starts the agent runtime as a child process:

   ```bash
   exec claude-code \
       --print-mode stream \
       --headless \
       --dangerously-skip-permissions \
       --model "${AGENTCTL_MODEL}" \
       --config /home/agent/.config/agent/config.json
   ```

   (Flag names placeholders; the actual flags depend on the runtime
   version the image pins. The constraint: permission prompting off and
   the runtime emits structured stream events on stdout.)

7. Bridges runtime stdout → control sock as `runtime.event` frames.
8. Listens for control-sock inbound: `agentd.message` writes a user
   message to runtime stdin; `agentd.interrupt` sends SIGINT to the
   runtime (the runtime cancels its model stream); `agentd.shutdown`
   sends SIGTERM with a 30s grace, then SIGKILL.
9. The shim watches `/work/<repo>/` (excluding `.git/objects`) and emits
   throttled `repo.changed` events.

If the runtime exits with a non-zero status, the shim emits
`runtime.error{fatal:true, exit_code: N}` and exits with the same status.
Docker reports the container as exited; `agentd` marks the session
`stopped` (or `error` if exit was unclean).

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

## 4. Network policy (realizing R7)

The hard requirements:

- **Allow** egress to Anthropic (`api.anthropic.com:443`).
- **Allow** egress to configured internal MCP CIDRs and `github.com:443`
  (for the GitHub MCP and `git push`).
- **Deny** access to peer session containers.
- **Deny** access to host loopback (where `agentd`'s Web UI lives) — the
  control sock is the **only** allowed channel.

### 4.1 Mechanism

We layer three controls:

1. **Per-session Docker network with ICC off** (§2.2). This already
   blocks peer containers on the same install — they cannot resolve or
   reach each other.
2. **iptables FORWARD rules** managed by `agentd`. On Linux, when
   `agentd` creates a session network, it inserts ordered rules in a
   custom chain `AGENTCTL-EGRESS` (jumped to from `DOCKER-USER`):

   ```text
   # Pseudocode for agentd's iptables installer.
   iptables -N AGENTCTL-EGRESS                                    (idempotent)
   iptables -I DOCKER-USER 1 -j AGENTCTL-EGRESS                   (idempotent)
   iptables -I DOCKER-USER 1 -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
   # For each session network <net-name>:
   iptables -A AGENTCTL-EGRESS -i <net-name> \
            -d <host-bridge-ip>/32 -j DROP                         # Block host loopback path
   iptables -A AGENTCTL-EGRESS -i <net-name> \
            -d 127.0.0.0/8 -j DROP                                 # Defensive
   for cidr in <RFC1918 ranges except configured-mcp-cidrs>:
       iptables -A AGENTCTL-EGRESS -i <net-name> -d $cidr -j DROP
   for cidr in <configured-mcp-cidrs>:
       iptables -A AGENTCTL-EGRESS -i <net-name> -d $cidr -j ACCEPT
   iptables -A AGENTCTL-EGRESS -i <net-name> -p tcp --dport 443 -j ACCEPT
   iptables -A AGENTCTL-EGRESS -i <net-name> -p udp --dport 53 -j ACCEPT
   iptables -A AGENTCTL-EGRESS -i <net-name> -j DROP
   ```

   On macOS, Docker Desktop runs Linux in a VM; iptables runs inside the
   VM. `agentd` shells out to Docker's `dockerd` via `docker network`
   labels and a `--internal=false` flag plus a custom Docker plugin? No —
   we instead **use Docker's IPAM and a tighter trick**: each session
   network is created with `--internal=true` (no external by default) and
   we add a **userland egress proxy** sidecar pattern. Too complex for v1.

   **macOS approach for v1 (simpler):** Docker Desktop on macOS isolates
   networks effectively from the host (VM boundary). The host loopback
   IS reachable from a container only via `host.docker.internal` (a
   Docker-injected hostname). We **explicitly do not inject**
   `--add-host=host.docker.internal:host-gateway`, and we set
   `--add-host=host.docker.internal:127.0.0.1` so any inadvertent
   reference resolves to the container's own loopback. Combined with
   per-session networks and ICC=off, peer-to-peer is blocked. Egress
   filtering on macOS is best-effort (we cannot install host-side
   iptables); we document this gap in `security.md` §4.

3. **Network self-test** (`agentd doctor`) verifies all four
   constraints in §4 by spinning up an ephemeral diagnostic container on
   a session network and checking each is enforced.

### 4.2 MCP CIDRs

`config.toml` has `[network.allowed_mcp_cidrs]`:

```toml
[network]
allowed_mcp_cidrs = ["10.20.0.0/16", "192.168.42.0/24"]
allow_github = true       # github.com IP pool resolved at start
allow_anthropic = true    # api.anthropic.com IP pool resolved at start
```

Public MCPs (e.g., the GitHub MCP) have their effective CIDRs resolved
at `agentd` startup via DNS (and refreshed every hour). Internal MCPs are
configured by CIDR, not hostname, to keep the policy crisp.

### 4.3 What's intentionally not blocked

- DNS to the host's resolver (the bridge gateway IP on UDP/53 — actually
  Docker's embedded resolver on `127.0.0.11` inside the container, which
  forwards via the host). We allow it; without DNS, nothing works.
- The control sock — by definition.

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
