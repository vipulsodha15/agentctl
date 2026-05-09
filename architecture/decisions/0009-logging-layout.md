# ADR 0009 — Logging and observability layout (§15.9)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

`agentd` runs as a system service and manages many sessions. Logs need
to be:

- discoverable (a developer wonders "what happened in session X?"
  without learning new tools);
- structured enough to query;
- bounded in disk usage;
- aligned with platform conventions (journald on Linux, unified log on
  macOS).

R4's CLI surface includes `agentctl logs <session>` but doesn't specify
sources.

## Decision

Two-tier logging:

1. **Daemon log** — cross-session, `agentd`-wide. On Linux, written to
   stderr and captured by systemd `--user` (`journalctl --user -u
   agentd`). On macOS, written to stderr (captured by launchd to
   `~/Library/Logs/agentctl/agentd.log`) plus `os_log` for high-level
   events.
2. **Per-session log** — `~/.local/share/agentctl/sessions/<session_id>/agentd.log`.
   NDJSON. Rotated in-process (50 MB or daily, keep 7 generations,
   gzip).

Format: NDJSON for both tiers. Schema in observability.md §2.3. Required
fields: `ts`, `level`, `component`, `msg`. `session_id` when relevant.

`agentctl logs <session>` tails the per-session file (pretty by
default; `--raw` for NDJSON). `agentctl logs --daemon` shells out to
`journalctl --user -u agentd -f` on Linux, tails the file on macOS.
`agentctl logs <session> --container` proxies to `docker logs`.

Conversation contents (user messages, assistant outputs) are **never**
logged. Tool-call metadata (tool name, duration, result-is-error) is
logged; tool-result bodies are not. A redactor wraps the logger to
strip secret-shaped strings as defense-in-depth.

## Consequences

- Linux developers use familiar journalctl tooling for daemon-wide
  triage; per-session tailing is a CLI-shipped convenience.
- macOS developers don't get journalctl, but they get a file at the
  expected `~/Library/Logs/` path.
- Disk use per session is bounded (~120 MB worst case) and surfaced
  in `agentctl ls --verbose`.
- Privacy is enforceable: no conversation bodies on disk in
  agentctl-owned files.
- Rotation is in-process, not tied to system tools (logrotate,
  newsyslog), so it works the same everywhere.

## Alternatives considered

- **Single combined log file.** Easier reading but worse for "scope
  to one session" and worse for retention (long-running session = huge
  file). Rejected.
- **One log per session day.** Cleaner for retention but more files;
  no clear win for the developer. Rejected.
- **Send logs out via OpenTelemetry.** Violates the no-telemetry
  promise and adds dependencies. Rejected for v1.

## References

- requirements.md §4, §15.9.
- observability.md (full design).
- security.md §2.6 (redaction).
