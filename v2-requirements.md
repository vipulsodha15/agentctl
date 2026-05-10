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

## V2.x. Other deferred items

Placeholders aligned with `requirements.md` §16. These will be expanded
as v2 scoping begins.

- Multi-user `agentd` (one daemon serving multiple OS users).
- Remote `agentd` (CLI on machine A, daemon on machine B).
- Cloud-hosted sessions.
- Live MCP toggling during a running session.
- User-defined skills added per session.
- Session forking, branching, or migration.
- Cost limits / budgets / alerts.
- Backup/restore of DB and volumes to external storage.
- Hardened sandbox beyond Docker defaults (gVisor, Kata Containers).
- Mobile UI.
- Native Windows (currently WSL2 only).
- Pre-warmed container pools.
- Container pause/unpause as a third lifecycle state.
- `agentctl self-update` subcommand (in-binary self-update without curl-pipe; v1 ships re-run-`install.sh` as the upgrade path).
- OS keychain integration for secrets at rest.
- `agentctl audit <session>` and `agentctl export-state` for backup.
