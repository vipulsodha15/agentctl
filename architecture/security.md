# Security

Per §15.1, the agent runs inside the container with permission prompting
disabled. The container is the safety boundary. This document spells out
what that boundary protects, where it doesn't, and how secrets and
network posture are handled end-to-end.

## 1. Threat model

### 1.1 Trusted

- The OS user running `agentd` and `agentctl`. Anything that user can
  do, agentctl can do; we make no attempt to defend against malware
  running as the same user. (See §3.)
- The Docker daemon. agentctl trusts the Docker socket.
- The `agentctl` and `agentd` binaries (signed by the project's release
  process; signature verified by `install.sh` against an embedded public
  key before the binary is written to disk — see install-and-update.md
  §1.2).
- The base image, by digest, after cosign verification
  (container-and-image.md §1.5).

### 1.2 Half-trusted

- The agent runtime running inside a session container. We expect it to
  be Claude Code at a known version, but the container's network access
  to public endpoints, MCPs, and GitHub creates surface where a
  compromised tool result could try to escape. The container's isolation
  contains the damage.
- The Web UI in the developer's browser. Same-origin browser code is
  trusted; cross-origin code is not (§3).

### 1.3 Untrusted

- Other session containers (peer access denied at the network layer).
- Anything on the public internet talking to a session container, except
  the configured outbound endpoints (Anthropic, MCPs, GitHub).
- Other processes on the host machine that don't run as the same user
  (file perms enforce; `127.0.0.1:7777` is reachable cross-user, but
  the bearer token gates).

### 1.4 Blast radius of a malicious or buggy agent

If the agent runtime is somehow malicious or controlled by a bad input:

| Reachable | Notes |
|---|---|
| `/work` | Yes, full control. The session volume is the blast cap. `agentctl stop` deletes it. |
| Other sessions' `/work` | No. Volumes are not shared. |
| Host filesystem outside the volume mount | No. Bind-mounts are limited to volume + control sock dir. |
| Host loopback / `agentd` admin API | Network-reachable via the host bridge gateway, but **gated by the bearer token** in `web_token` which the container cannot read (no bind-mount). Unauthenticated requests return `401`. The control sock is also available to the container but verifies `session_token`. v2 may add network-layer blocking; v1 relies on auth. |
| Other session containers | No. Per-session networks with ICC off. |
| Public internet | Yes, **without restriction** in v1. The container can reach Anthropic, GitHub, configured MCPs, and arbitrary other endpoints. v2 may add egress allowlisting (`v2-requirements.md` §V2.1). |
| The developer's LAN / corporate intranet | Yes, in v1. Routable from the container. v2 may block RFC1918 except configured CIDRs. |
| The developer's GitHub account | Yes, with the developer's PAT. The agent could push branches, open PRs (if PAT has scope). This is documented in §15.1's "developer is responsible for the consequences." |
| Anthropic API spend | Yes — agent can spend up to whatever model+rate the configured key allows. R10 makes spend visible; budgets are post-v1. |
| `agentd` itself | No. The control sock is the only inbound surface; agentd validates everything. |

The cap on each is a layered control (filesystem isolation, network
policy, control-channel auth). Removing any one would widen the blast
radius; we don't.

### 1.5 Out-of-scope adversaries

- **Root on the host.** Game over; nothing in v1 mitigates this.
- **Same-user malware.** Reads `secrets.json`, `web_token`, drives the
  Web UI. We raise the bar (file perms, token, Origin) but document the
  limitation.
- **A hostile registry pushing a malicious image.** cosign verification
  on the configured identity is the mitigation. A break here is a
  registry-compromise event the team responds to out-of-band.
- **A compromised PAT.** The developer rotates via `agentctl init
  --reset-token github`.
- **Side-channel timing attacks across containers** (Spectre etc.).
  Docker default isolation; not a v1 concern (and §16 OOS hardened
  sandbox).

## 2. Secrets handling end-to-end

### 2.1 At rest

- `~/.config/agentctl/secrets.json`: mode `0600`. Contains
  `ANTHROPIC_API_KEY`, `GITHUB_PAT`. Plain JSON; no encryption beyond
  fs perms (per R7's secrets-at-rest target). OS keychain is §16 OOS.
- `~/.config/agentctl/web_token`: mode `0600`. 256-bit URL-safe random.
- `~/.config/agentctl/config.toml`: mode `0600`. Includes price tables
  and the image digest pin; no secrets.
- `~/.local/share/agentctl/sessions/<id>/secrets.env`: mode `0600`.
  Created at container start, **deleted after the container is
  running** (the env vars have been inherited; the file is no longer
  needed). We deliberately don't keep it around to minimize the window.

`agentd` enforces these perms on every boot (the `fs.perms` doctor
check). Any drift is fixed and logged.

### 2.2 In transit (CLI ↔ agentd)

Unix-socket only; same OS user. No network. Token never appears in this
channel.

### 2.3 In transit (Browser ↔ agentd)

- Loopback HTTP. The bearer token in `Authorization` header **never** in
  query strings or paths.
- One-time exception: the loader URL fragment (`#t=…`) carries the
  token; fragments are not sent to the server (so they don't appear in
  access logs) and the loader strips them from the URL after extraction.

### 2.4 In transit (agentd ↔ container)

- Unix-socket only; no network. The bind-mount perms (`0660` on the
  socket file, `0700` on the dir) bound exposure to processes running
  as the agent uid (which is uid 1000 inside the container; the host
  user has the same uid).
- The shim sends `session_token` in `runtime.hello`. agentd verifies
  before sending `agentd.greet`. Failure ⇒ socket closed.
- Per-message: no further auth. The single-connection invariant
  (api.md §4.4) means once handshake is done, the socket is the
  session for its lifetime.

### 2.5 Inside the container

- `ANTHROPIC_API_KEY` and `GITHUB_TOKEN` are environment variables on
  PID 1, inherited by the runtime. `/proc/<pid>/environ` is readable by
  the same uid; that's the runtime itself, no other actor in the
  container.
- The git credential helper file at `/work/.config/git/credentials` is
  `0600` and contains the PAT inline. This is necessary for `git push`
  to work without prompting. The volume is per-session, so this file is
  not visible to other sessions.

### 2.6 In logs

A redactor wraps the structured logger. Before each line is written:

- Anthropic key (`sk-ant-…`) → `***ANTHROPIC***`.
- GitHub PAT (`ghp_…`, `ghs_…`, `gho_…`) → `***GH_PAT***`.
- Web bearer token (256-bit) → `***WEB_TOKEN***` if it ever appears in
  a log line (it shouldn't; defense-in-depth).
- `session_token` (256-bit) → `***SESSION_TOKEN***`.

The redactor runs on both daemon and per-session logs. Tests assert
these patterns never appear in CI test artifacts.

### 2.7 Rotation

- Anthropic / GitHub: `agentctl init --reset-token <kind>`. Re-prompts
  and validates; takes effect on next session start.
- Web token: `agentctl init --reset-web-token`. Rewrites `web_token`,
  emits a `web.token_rotated` daemon event; the SPA's existing cookie
  is invalidated server-side; the developer needs to re-open the UI
  with `agentctl ui`.
- `session_token`: not rotatable mid-session in v1. New session ⇒ new
  token.

## 3. Web UI auth on localhost (§15.7)

### 3.1 Threat

Any process running as the same OS user can connect to
`127.0.0.1:7777` and `~/.local/share/agentctl/agentd.sock`. Without auth,
local malware could:

- Start sessions and consume Anthropic budget.
- Read conversation history.
- `git push` to the developer's repos via the PAT.

### 3.2 Mitigation

Per-install bearer token + strict Origin enforcement (see §15.7
RESOLVED entry; api.md §3.3-3.6 for wire detail).

The CSRF angle: a webpage the developer visits in a normal browser
session **cannot** submit forms or `fetch()` to `127.0.0.1:7777` and
have them succeed because:

- They lack the bearer token.
- The Origin enforcement rejects any non-`http://127.0.0.1:7777`
  origin.
- Browsers automatically attach `Sec-Fetch-Site` headers we can use as
  a second check on modern browsers.

### 3.3 Residual risk

A process running as the same user that reads `~/.config/agentctl/web_token`
defeats this. v1 documents this; future v2 could add OS-keychain
storage for the token (§16 OOS for now).

A browser extension running in the developer's browser with permission
on `127.0.0.1` could steal the cookie. Mitigation: SameSite=Strict
helps for cross-site browser-driven attacks; doesn't help against
malicious extensions. The same is true of any local web UI.

## 4. Network posture

(Cross-reference: container-and-image.md §4.)

### 4.1 What v1 enforces

| Goal | Method |
|---|---|
| Container does **not** reach peer containers | Per-session Docker bridge network with `enable_icc=false`. Costs nothing beyond Docker config. |
| `agentd` admin API is not driveable from a session container | Bearer-token auth on every `/v1/*` request. The token lives in `~/.config/agentctl/web_token` (`0600`) which the container has no bind-mount to read. |
| `agentd` accepts no remote connections | Binds CLI socket and HTTP/WS to `127.0.0.1` and `::1` only. |
| Control sock auth | Shim presents `session_token`; agentd verifies before greeting. |

### 4.2 What v1 does **not** enforce

The container has Docker's default outbound network posture:

- **Public internet:** unrestricted. The container can reach Anthropic,
  GitHub, configured MCPs, and arbitrary other URLs.
- **Developer LAN / internal services:** routable from the container.
  Nothing in v1 blocks reaching the dev's NAS, router admin, or
  internal corp services.
- **Host loopback:** reachable network-wise via the bridge gateway IP.
  Gated by bearer-token auth on `agentd`, not at the network layer.

This matches the posture of comparable tools (GitHub Codespaces,
Gitpod, devcontainers, Cursor background agents). The threat model
trade-off is documented in `v2-requirements.md` §V2.1 along with the
previously considered iptables-based design.

### 4.3 What we explicitly do not try to do in v1

- Allowlist or deny outbound by hostname, IP, or CIDR.
- Manipulate iptables / nftables on the host.
- Run an egress proxy.
- Probe macOS Docker Desktop's VM-internal networking.

If a v2 customer threat model demands egress filtering, the leading
candidate is an egress-proxy sidecar (FQDN allowlist) rather than
host-firewall manipulation, because the latter requires privileged
helpers a user-level `agentd` does not have.

## 5. Container hardening

Beyond network policy:

| Control | Setting | Why |
|---|---|---|
| Run as non-root | uid 1000 (`agent`) | A break inside the container starts unprivileged. |
| `--cap-drop ALL` | always | Drop all Linux capabilities; runtime needs none. |
| `--security-opt no-new-privileges` | always | setuid binaries cannot escalate. |
| `--read-only` rootfs | always | Tampering in `/usr` etc. requires escaping the read-only mount. |
| `--pids-limit 512` | always | Fork-bomb defense. |
| `--memory-swap == --memory` | always | No swap; OOM-kill rather than swap host into the dirt. |
| seccomp profile | Docker default | We do not customize; the default is solid. |
| AppArmor/SELinux | system default | We do not customize. |

## 6. Auditability

- `session_lifecycle` table (data-model.md §2) records every state
  transition with timestamps. A `agentctl audit <session>` post-v1
  command would surface this; in v1 the developer reads the table
  directly with `sqlite3 ~/.local/share/agentctl/agentd.db`.
- `usage` table provides spend audit per session and per range.
- Per-session NDJSON log captures lifecycle and tool-call metadata for
  forensic review (without bodies — see §2.6).
- Daemon log (journald) captures cross-session events.

## 7. Incident response checklist

If a developer suspects a session went rogue:

1. `agentctl interrupt <session>` — stop in-flight turn.
2. `agentctl stop <session>` — destroy container + volume.
3. `agentctl init --reset-token github` — rotate the PAT (the agent
   had access to it via env + git creds).
4. Inspect any branches the agent pushed: `git fetch && git log
   --all`.
5. Run `agentctl doctor` for environmental sanity check.

If `agentd` itself misbehaves:

1. `systemctl --user stop agentd` / `launchctl bootout`.
2. `agentctl init --repair` — reinstall service.
3. Check `journalctl --user -u agentd` (or `~/Library/Logs/agentctl/`)
   for the stack.
4. File an issue with the recent log excerpt.
