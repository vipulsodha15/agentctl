# agentctl — v2 Requirements

This document captures requirements deferred from v1. Items here are
candidates for v2 prioritization, not commitments. They will be
formalized when scoping for v2 begins, driven by real user demand and
evolving threat models.

## V2.1. Network egress restrictions for session containers

**Goal.** Restrict what a session container can reach over the network so a
runaway or misbehaving agent cannot exfiltrate code/secrets to arbitrary
public destinations or pivot into internal services on the developer's
LAN/corporate network.

**Possible scopes (to be decided in v2 design):**

1. **RFC1918 block.** Drop egress to private IP ranges except a configured
   allowlist of internal MCP CIDRs. Cheapest; catches "agent reaches dev's
   NAS / router admin / internal corp services."
2. **Host-loopback hard block.** Drop egress to the host bridge IP and
   `127.0.0.0/8` so a session cannot reach `agentd`'s admin API at all
   (v1 relies on the bearer-token auth as the only control here).
3. **Strict allowlist.** Egress permitted only to Anthropic, GitHub, and
   configured MCP endpoints. Strongest; needed for environments with
   formal data-loss-prevention requirements.

**Why deferred from v1.** v1 ships with Docker-default egress posture: a
session container can reach the public internet, the developer's LAN, and
the host's loopback. Trust boundary is the container's filesystem
(per-session volume) plus `agentd`'s bearer-token authentication on its
admin API. Comparable tools (GitHub Codespaces, Gitpod, devcontainers,
Cursor background agents) ship with the same posture. Real customer
pressure — particularly enterprise DLP requirements — should drive
prioritization.

**v1 still retains** (free Docker-native controls):

- Per-session bridge networks with `enable_icc=false` (no peer-container
  reachability).
- `agentd` binds its CLI socket and HTTP/WS port to `127.0.0.1` only.
- `agentd` admin API requires a per-install bearer token the container
  has no path to read (no relevant bind-mount, file is `0600` outside
  any volume).

### Background: previously proposed v1 solution (now removed)

An earlier v1 architecture proposed strict per-session egress filtering
on Linux. The mechanism:

- A custom iptables chain `AGENTCTL-EGRESS` jumped to from `DOCKER-USER`.
- Per-session rules drop traffic to RFC1918 ranges and the host bridge
  IP; accept egress to DNS-resolved Anthropic IP pools (hourly refresh),
  GitHub IP pools, and configured internal MCP CIDRs; default drop.
- A `network.policy` self-test container in `agentctl doctor` verifies
  each rule by spinning up a probe container on a fresh session network.
- macOS Docker Desktop documented as best-effort because host-side
  iptables doesn't reach the Linux VM where Docker actually networks
  containers; only peer isolation worked there.

This was dropped from v1 for these reasons:

1. **Privilege gap.** `agentd` runs as `systemd --user` and lacks
   `CAP_NET_ADMIN`, so it cannot mutate iptables. The architecture never
   specified the privileged helper required.
2. **CDN fragility.** Anthropic and GitHub front behind CDNs whose IP
   pools rotate; "DNS-resolved CIDR allowlist, refreshed hourly"
   produces intermittent `git push` and API failures during the refresh
   window.
3. **Platform parity.** macOS would have shipped with a documented gap;
   v1 would have had two security postures depending on OS.
4. **Marginal threat coverage.** An agent already holds the developer's
   GitHub PAT; legitimate `git push` to a private branch is an
   unblockable exfil channel. Strict allowlisting catches "agent posts
   to attacker.com" but not "agent commits payload to a fresh GitHub
   branch and pushes."

### v2 directions to consider

- **Egress proxy.** Container's only outbound goes to a sidecar proxy
  (e.g., tinyproxy, mitmproxy) that enforces an FQDN allowlist. Avoids
  DNS/CIDR brittleness and the privileged-iptables problem.
- **Privileged helper.** A small system-level service (`agentctl-netd`)
  mediates nftables/iptables changes on behalf of the user-level
  `agentd`. Gets us the original design with a real privilege story.
- **DNS-only restriction.** Replace the container's resolver with one
  that only answers configured FQDNs. Cheap and platform-portable;
  bypassable by IP literals so it's a soft control.
- **Network namespace + slirp4netns.** Container egress passes through
  a userspace network stack the daemon owns. Strong control, complex.

The `architecture/decisions/0013-network-policy-enforcement.md` ADR
captured the original design and is now marked Superseded; it is kept
for historical reference.

---

## V2.2. Live skill reload mid-session

**Goal.** When the developer adds, edits, or removes a skill via the
`agentctl skill ...` CLI, attached running sessions pick up the change
without a session restart.

**v1 posture.** Skills are bind-mounted as a per-session snapshot
composed at start (R3, R9, ADR 0014). Adding/editing a skill takes
effect on the **next session start**. To pick up the change in a running
session, the developer runs `agentctl restart <session>` (preserves the
volume).

**v2 design sketch.**

- `agentd` watches `~/.local/share/agentctl/{builtin,custom}-skills/` via
  fsnotify.
- On change: re-compose the per-session snapshot for each running
  session (cp -r over the existing snapshot, atomic via swap-and-rename),
  send a control-channel `agentd.skills_reloaded` to the shim, and emit
  `skills.changed` to attached clients so `/help` and autocomplete
  refresh.
- The shim signals the runtime to rescan `/skills/` (SIGHUP-equivalent
  if the runtime supports it; otherwise the runtime is told via the
  control channel).
- Per-install knob `session.skills_live_reload = true|false` (default
  `true`) for developers who want strict per-session immutability.

**Why deferred.** v1 sessions have a clean reproducibility story
(`skills_snapshot_hash` is fixed at start). Live reload weakens that
slightly and adds runtime-coordination plumbing; bundling it with v1
risks the bigger pieces. Restart-to-pick-up-changes is acceptable for
v1.

## V2.3. Team-shared custom skills

**Goal.** A team distributes its custom skills without requiring each
developer to manually `agentctl skill add`.

**v2 design sketch.**

- `agentctl skill sync <git-url>` clones a skills repo (with the same
  `<name>/manifest.json + impl files` layout) into a tracked subdir of
  `~/.local/share/agentctl/custom-skills/team/`.
- Periodic refresh on a configurable interval; manual refresh via
  `agentctl skill sync --refresh`.
- Signature verification: skills repo signs a manifest.lock at the
  root; agentctl verifies against a configured public key before
  activating updates. Optional but recommended.

**Why deferred.** v1 has `agentctl skill export` + `add` for
hand-distribution; that covers small-team use. The git-sync
+ signature workflow is a substantial separate design.

## V2.x. Other deferred items

Placeholders aligned with `requirements.md` §16. These will be expanded
as v2 scoping begins.

- Multi-user `agentd` (one daemon serving multiple OS users).
- Remote `agentd` (CLI on machine A, daemon on machine B).
- Cloud-hosted sessions.
- Live MCP toggling during a running session.
- Session forking, branching, or migration.
- Cost limits / budgets / alerts.
- Backup/restore of DB and volumes to external storage.
- Hardened sandbox beyond Docker defaults (gVisor, Kata Containers).
- Mobile UI.
- Native Windows (currently WSL2 only).
- Pre-warmed container pools.
- Container pause/unpause as a third lifecycle state.
- `agentctl self-update` subcommand (in-binary self-update without curl-pipe; v1 ships re-run-`install.sh` as the upgrade path).
- Pre-built/published base image (skip the local build for fast onboarding in air-gapped or strict-security environments).
- Reproducible-build hardening (pinned apt/npm digests, vendored dependencies, restricted upstream package sources).
- OS keychain integration for secrets at rest.
- `agentctl audit <session>` and `agentctl export-state` for backup.
