# ADR 0007 — Web UI authentication on localhost (§15.7)

- **Status:** Accepted.
- **Date:** 2026-05-09.
- **Deciders:** Architect.

## Context

The Web UI binds to `127.0.0.1:7777` only (R4, §4 NFRs). Any process on
the same host running as the same user can reach this port. Without
auth, local malware (or a buggy browser extension on
`http://127.0.0.1`) could drive sessions, read history, or push to
GitHub via the developer's PAT.

We can't make this port unreachable (the browser needs to reach it).
The question is what gating to place.

## Decision

Three layers:

1. **Per-install bearer token**, 256-bit URL-safe random, stored in
   `~/.config/agentctl/web_token` mode `0600`. Required on every
   `/v1/*` request and WebSocket upgrade as `Authorization: Bearer
   <token>` or as the `agentctl_token` cookie.
2. **Strict Origin enforcement**. Every state-changing request (`POST`,
   `PATCH`, `DELETE`) and every WS upgrade must carry `Origin:
   http://127.0.0.1:<bind-port>`. Missing or mismatched ⇒ `403`. When
   present, `Sec-Fetch-Site: same-origin` is also required.
3. **Token handoff via URL fragment, never via query string**. The CLI
   constructs `http://127.0.0.1:7777/#t=<token>` and shells out to the
   browser. The fragment is not sent to the server (so it doesn't show
   up in access logs). The loader page reads the fragment, sets the
   `agentctl_token` cookie (`SameSite=Strict; Path=/`), and uses
   `history.replaceState` to strip the fragment from the URL.

Token rotation: `agentctl init --reset-web-token` regenerates and
invalidates the old token server-side; the developer re-opens the UI
with `agentctl ui`.

`/healthz` is the **one** path that does not require auth so doctor
checks work.

## Consequences

- A page on the open internet cannot drive the Web UI: even if it
  reaches `127.0.0.1`, it lacks the token and the wrong Origin will
  fail the CSRF check.
- A process on the host running as the same user can read `web_token`
  and forge requests. We've raised the bar but not eliminated this; v1
  documents the limitation. OS-keychain integration is §16 OOS.
- A browser extension with permission on `127.0.0.1` could steal the
  cookie. SameSite=Strict cookies don't help here. This is a known
  residual.
- Operationally simple: one token per install; rotated on demand.

## Alternatives considered

- **No auth (rely on loopback only).** Rejected because of local
  malware risk.
- **Per-session tokens.** More principled but increases UX complexity
  (the developer needs to thread a token per session). Doesn't add
  meaningful security since the UI's blast radius is the install, not
  per-session.
- **OS-keychain storage now.** The MVP for "raise the bar" is the file
  with `0600`. Keychain plumbing across macOS Keychain Services and
  Linux Secret Service is platform-specific and wide; defer to v2.

## References

- requirements.md R4, §4, §15.7, §16.
- api.md §3.3, §3.4, §3.6.
- security.md §3.
