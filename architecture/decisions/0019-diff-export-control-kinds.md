# 0019 — Diff and export control-channel kinds

Status: accepted (M5)

## Context

`api.md` §4.3 enumerates the control-sock kinds the shim and agentd speak,
but R8's diff/export surface only lands in M5. The diff/export protocol
adds three request kinds (agentd → container) and three reply kinds
(container → agentd) that need to be pinned before the shim and agentd
can talk to each other.

## Decision

The control channel grows the following kinds. Frame envelope is
unchanged from `api.md` §4.2 — line-delimited JSON, 1 MiB max per frame,
the new kinds slot into `kind` exactly like the existing ones.

### agentd → container

| Kind | Data |
|---|---|
| `agentd.diff_request` | `{ request_id, repo: string?, format: "unified"\|"stat" }` — empty `repo` means "every entry in `/work/.agentctl/repo-bases.json`". |
| `agentd.export_patch_request` | `{ request_id, repo: string? }` — same scoping; format is always `unified --patch`. |
| `agentd.export_push_request` | `{ request_id, repo: string, branch: string, message: string? }` — single repo only. |

### container → agentd

| Kind | Data |
|---|---|
| `runtime.diff_chunk` | `{ request_id, repo, data: string (base64 of raw bytes) }` — multiple per request. |
| `runtime.diff_end` | `{ request_id, repo, exit_code, base_sha?, branch?, note?, error? }` — terminator for one repo's stream. The shim emits a final `repo: ""` end frame for whole-session requests. |
| `runtime.export_push_result` | `{ request_id, repo, branch, success, output, error? }` — single response. |

The 1 MiB frame limit means raw diff output must be split across multiple
`runtime.diff_chunk` frames; the shim's `git.py:_chunk_bytes` does that
at 512 KiB granularity. Base64 inflates roughly 4/3, leaving comfortable
headroom inside the 1 MiB envelope.

If a request names a repo that isn't in `repo-bases.json` the shim
replies with a single `runtime.diff_end{exit_code: 64, error: "repo not
found"}`. If `repo-bases.json` is missing entirely the shim falls back
to `HEAD` and notes that on the end frame so clients can surface why
their diff is empty (R6 idle-resume edge case: a brand-new container
that never recorded base SHAs).

## Why JSON+base64 rather than a binary side-channel

Mixing a binary side-channel into the existing NDJSON control sock
would mean a second framing scheme, a second buffer, and a second set
of backpressure rules. base64 inflates payloads ~33% but keeps the
control sock single-protocol; on a 1 MiB frame budget that's still
~750 KiB of patch text per chunk, which is plenty.

## Consequences

- agentd's session-manager interface grows three streaming methods —
  `Diff`, `ExportPatch`, `ExportPush` — paralleling `runtime.snapshot`'s
  in-memory `pendingSnap` map with `pendingDiffs` / `pendingPush`.
- The Web SPA gains a "Changes" tab that lists session repos, downloads
  per-repo `.patch` files, and surfaces `git push` output verbatim.
- `agentctl diff` / `agentctl export --patch` / `agentctl export --push`
  CLI commands shell directly through the same op surface.
- The new kinds are additive; older shims that don't speak them will
  ignore the requests (and time out client-side), so backwards-compat
  with future shim versions is intact.
