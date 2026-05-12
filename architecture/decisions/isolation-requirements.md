# Product Requirements: Session Isolation

## Purpose

agentctl runs AI coding-agent sessions on a developer's own machine. Each
session is given an agent that can read, write, run commands, and reach the
network on the developer's behalf. Because the agent acts autonomously, every
session must be **isolated** — from the host, from other sessions, and from
its own past mistakes. This document captures the product-level isolation
guarantees agentctl owes the developer. It deliberately avoids implementation
detail; the technical design lives in `architecture/`.

## Goals

1. A developer can safely run an autonomous agent against an untrusted repo
   without worrying that the agent will damage the host, leak secrets across
   sessions, or interfere with other concurrent sessions.
2. Throwing away a session — including everything it touched — is a single
   command and leaves no residue on the host.
3. The developer can run many sessions side-by-side and trust that each one
   sees its own world.

## Non-goals

- Hardened multi-tenant isolation against a sophisticated kernel-level
  attacker. This is a developer tool on a single user's machine, not a public
  cloud.
- Network-level egress filtering or per-session firewalling in v1.
- Sandboxing the developer's own actions (e.g. files the developer copies in
  by hand).

---

## 1. Container isolation

Every session runs inside its own dedicated container. The container is the
session's universe: the agent and everything it spawns lives there.

**The developer can expect:**

- **One container per session.** No session ever shares a container with
  another session, and no two sessions ever run inside the same process tree.
  Restarting a session gives it a fresh container; stopping a session destroys
  its container.
- **The agent cannot see the host.** From inside a session, the agent does not
  see the developer's home directory, SSH keys, browser profile, other
  projects, or any other application running on the machine. It sees only
  what agentctl chose to make available to it.
- **The agent cannot see other sessions.** A session cannot enumerate, talk
  to, or interfere with another session's container, files, processes, or
  in-flight work. Two sessions started from the same repo behave as if the
  other did not exist.
- **The agent cannot escalate on the host.** Nothing the agent does inside a
  session container — installing packages, running scripts, modifying files —
  changes the host. The host's package set, system services, and global
  configuration are unaffected by session activity.
- **Resource caps are enforced per session.** A runaway agent in one session
  cannot starve the host or other sessions of CPU, memory, or disk. Each
  session has a default memory and CPU cap, configurable per-install.
- **The container is disposable.** "Recreate this session's container" is
  always safe and never loses the developer's work, because work lives on the
  session's disk (§2), not in the container.

**The developer is responsible for:**

- Not bind-mounting host paths into a session by hand and then expecting the
  agent not to touch them.
- Treating an agent session as approximately as trustworthy as the code and
  MCP servers it runs — the container is a strong boundary, but it is not a
  hardened sandbox against a determined attacker (see Non-goals).

---

## 2. Disk isolation

Every session has its own private working disk. This is where the agent's
working directory, repo clones, scratch files, and any output it produces
live.

**The developer can expect:**

- **One working disk per session.** Each session has exactly one persistent
  working area, dedicated to that session. No two sessions share it.
- **Disks are not visible across sessions.** A file written by session A
  cannot be read, listed, or modified from session B. If the developer wants
  to move work between sessions, they do it explicitly (e.g. `git push`).
- **Disks are not visible from the host's normal workspace.** The session's
  working disk is managed by agentctl; it does not pollute the developer's
  home directory, `~/Documents`, or any project directory they already work
  in. The developer's normal files are unaffected by anything an agent does
  in a session.
- **Disks survive container restarts.** Stopping a session's container,
  rebooting the machine, or upgrading agentctl does not lose the session's
  on-disk state. Reattaching to the session finds the working tree as it
  was.
- **Stopping a session reclaims its disk.** `agentctl stop` removes the
  session's container *and* its working disk. After stop, the session leaves
  no residual files on the host. There is no "delete session data" step the
  developer has to remember.
- **Skills and MCP configuration are read-only inputs.** The skills and MCP
  registrations the developer has set up at the install level are made
  available to each session, but a session cannot edit them in a way that
  affects other sessions or the install as a whole. Changes to the install's
  skills take effect on the *next* session start, never retroactively on
  running sessions.
- **Secrets live outside the session disk.** API keys and tokens the install
  knows about (Anthropic, GitHub) are not written into the session's working
  disk in a form that survives the session, and are not visible to other
  sessions.

**Sizing and cleanup.**

- Per-session disk usage is expected to stay small (typically well under
  500 MB for an idle session with one repo clone). agentctl does not cap
  session disk size in v1, but it does surface per-session disk usage so the
  developer can spot a runaway session.
- A separate `agentctl gc` / `prune` flow can be used to clean up disks of
  sessions the developer has already stopped; stopped sessions never leave
  orphan disks behind in the normal flow.

---

## 3. Session isolation

A session is the developer-facing unit: a named, durable conversation with an
agent, with its own history, its own container, its own disk, and its own
identity. Session isolation is what makes container and disk isolation
*useful*: it gives the developer a stable handle for an isolated world.

**The developer can expect:**

- **Sessions have stable identity.** Each session has a unique, stable
  identifier and (optionally) a human-readable name. Identifiers are not
  reused. Stopping a session and starting a new one produces a new session,
  not a resurrection of the old one.
- **Sessions are independent.** Each session has:
  - its own conversation history,
  - its own attached MCP servers and per-session MCP enable/disable
    decisions,
  - its own model selection and per-session settings,
  - its own cost / token accounting,
  - its own log stream,
  - its own attached repos (if any).
  None of these cross between sessions. Changing the model on session A does
  not change it on session B. Disabling an MCP for session A does not affect
  session B. Costs are reported per session.
- **Sessions are addressable equivalently from CLI and Web UI.** The same
  session can be opened from the terminal or from the local browser UI, and
  both surfaces show the same conversation, same state, same controls. There
  is no "CLI session" vs "UI session" distinction.
- **Concurrent sessions don't interfere.** A developer can run multiple
  sessions at the same time. The agent in one session cannot observe another
  session's prompts, responses, tool calls, or files. The default install
  supports at least 10 concurrent sessions on a typical developer machine.
- **Idle sessions are paused, not lost.** A session that has been idle past
  the configured threshold has its container stopped to free resources, but
  the session itself — its history, disk, identity — persists. Re-attaching
  resumes it from where it left off. A hard inactivity cutoff (default 24h)
  eventually stops abandoned sessions automatically.
- **Stopping a session is final and complete.** `agentctl stop <session>`
  removes the container, removes the disk, and removes the session from the
  list. The developer does not need to do follow-up cleanup. A stopped
  session cannot be resumed.
- **Sessions survive daemon restarts and reboots.** Restarting the local
  agentctl daemon, or rebooting the host, does not destroy sessions. On
  startup, agentctl recognises the sessions it owns and makes them
  re-attachable.
- **Interrupts are session-scoped.** Cancelling an in-flight turn in one
  session never cancels work in another.

---

## 4. What isolation does *not* mean (developer expectations)

To set expectations honestly:

- **Isolation is not anonymity from the developer.** agentctl itself can list
  sessions, read their cost and status, and (with the developer's action) tear
  them down. The developer is always the administrator of their own machine.
- **Isolation is not network containment in v1.** From inside a session, the
  agent can reach the public internet, configured MCP servers, and the
  developer's LAN. agentctl does not firewall outbound traffic per session in
  v1. Future versions may add egress allowlisting (see
  `v2-requirements.md`).
- **Isolation does not protect the developer's git remotes.** If the
  developer hands a session a GitHub token, the agent in that session can
  push branches, open PRs, and otherwise act as the developer on GitHub. The
  container boundary does not block this — by design, because the developer
  asked for it.
- **Isolation does not survive deliberate weakening.** If the developer
  bind-mounts their home directory into a session, or pastes secrets into the
  conversation, those things become reachable inside the session. agentctl
  isolates the *default* surface; it does not police explicit overrides.

---

## 5. Acceptance criteria (product-level)

A v1 install is considered to meet these isolation requirements when:

1. Starting two sessions from the same repo produces two independent working
   trees; edits in one are not visible in the other.
2. Stopping a session removes its container and its working disk; running
   `agentctl ls` no longer shows it, and disk usage on the host drops
   accordingly.
3. From inside a session, no host file outside what agentctl provisioned is
   visible, and no other session's container or disk is visible.
4. Rebooting the host and waiting for agentctl to come back up leaves
   previously-running sessions re-attachable, with their history and working
   trees intact.
5. A session pinned at its resource cap does not measurably degrade
   throughput in a peer session running on the same host.
6. Per-session cost, logs, and MCP enable state are reported and changed
   independently per session, with no cross-talk.

---

## 6. Open questions

- Should sessions support an explicit "export" / "snapshot" so the developer
  can hand off an isolated session's working state to another session or to
  the host workspace without manually copying? Out of scope for v1, candidate
  for v2.
- Should agentctl offer a per-session disk size cap (vs only surfacing
  usage)? Currently optional / out of scope for v1.
- Should the developer be able to share a single session across multiple
  attached terminals at once (read-only "watch" mode)? Out of scope for v1.
