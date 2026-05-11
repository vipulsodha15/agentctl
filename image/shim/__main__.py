"""Production runtime shim entrypoint.

Pipeline (container-and-image.md §2.6):

1.  Read ``secrets.env`` (already exposed via ``--env-file``); pick up the
    session_token from ``AGENTCTL_SESSION_TOKEN`` and the host control
    endpoint from ``AGENTCTL_CONTROL_ADDR`` (``host.docker.internal:<port>``).
2.  TCP-dial that address and send ``runtime.hello`` carrying
    ``session_token``.
3.  Receive ``agentd.greet``; clone any ``repos[]`` (record SHAs to
    ``/work/.agentctl/repo-bases.json``); send ``runtime.ready``.
4.  Translate SDK events into ``runtime.event`` frames; service inbound
    ``agentd.message`` / ``.interrupt`` / ``.snapshot_request`` / ``.shutdown``.
5.  Watch ``/work/<repo>`` (excluding ``.git/objects``) and emit throttled
    ``repo.changed`` frames.
6.  Heartbeat every 5s.
"""

from __future__ import annotations

import json
import os
import signal
import sys
import threading
import time
from typing import Any, Optional

import base64

from . import control, git as gitops, repos, runtime as rt
from .watcher import RepoWatcher


SHIM_VERSION = "1.0.0-m2"
SDK_VERSION = "0.1.80"
HEARTBEAT_SECONDS = 5.0
SHUTDOWN_GRACE_SECONDS = 30.0
# AGENTCTL_CONTROL_ADDR (TCP, ``host:port``) is what production agentd sets so
# the container reaches the host control channel through host.docker.internal
# instead of a bind-mounted unix socket — Docker Desktop's filesystem share
# does not pass unix sockets through reliably. AGENTCTL_CONTROL_SOCK is kept
# for unit tests that drive the shim against a local unix socket.
CONTROL_ADDR_ENV = "AGENTCTL_CONTROL_ADDR"
CONTROL_SOCK_ENV = "AGENTCTL_CONTROL_SOCK"
SESSION_META_DEFAULT = "/run/agentctl/control/session.json"


class Shim:
    def __init__(self, *, control_addr: str, session_meta: str) -> None:
        self._control_addr = control_addr
        self._session_meta = session_meta
        self._client: Optional[control.ControlClient] = None
        self._driver: Optional[rt.RuntimeDriver] = None
        self._watcher: Optional[RepoWatcher] = None
        self._stopping = threading.Event()
        self._heartbeat_thread: Optional[threading.Thread] = None
        self._sdk_session_id: Optional[str] = None
        self._meta: dict = {}

    def _read_meta(self) -> dict:
        if not os.path.exists(self._session_meta):
            return {}
        with open(self._session_meta, "r", encoding="utf-8") as f:
            return json.load(f)

    def run(self) -> int:
        self._meta = self._read_meta()
        session_token = self._meta.get("session_token") or os.environ.get("AGENTCTL_SESSION_TOKEN", "")
        if not session_token:
            sys.stderr.write("shim: AGENTCTL_SESSION_TOKEN missing\n")
            return 64

        self._client = control.ControlClient.connect_address(self._control_addr)
        self._client.send(
            control.KIND_HELLO,
            control.hello_payload(session_token, shim_version=SHIM_VERSION, sdk_version=SDK_VERSION),
        )

        greet = self._client.recv()
        if greet is None:
            return 0
        if greet.get("kind") != control.KIND_GREET:
            self._client.send(control.KIND_ERROR, {
                "code": "protocol_error",
                "message": f"expected agentd.greet, got {greet.get('kind')!r}",
                "fatal": True,
            })
            return 65
        greet_data = greet.get("data") or {}
        self._sdk_session_id = greet_data.get("sdk_session_id") or self._meta.get("sdk_session_id")

        repo_specs = repos.parse_repo_specs(greet_data.get("repos") or [])
        clone_results = repos.clone_all(
            repo_specs,
            on_error=lambda r: self._client.send(control.KIND_ERROR, {
                "code": "repo_clone_failed",
                "message": f"{r.name}: {r.error}",
                "fatal": False,
            }),
        )
        repos.write_repo_bases(clone_results)

        skills = self._discover_skills()
        ready = {
            "repos": [
                {"name": r.name, "url": r.url, "base_sha": r.base_sha, "branch": r.branch}
                for r in clone_results
                if not r.error
            ],
            "skills": skills,
            "started_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        }
        self._client.send(control.KIND_READY, ready)

        self._driver = rt.RuntimeDriver(
            rt.RuntimeConfig(
                model=greet_data.get("model") or os.environ.get("AGENTCTL_MODEL", ""),
                cwd="/work",
                resume=self._sdk_session_id,
                mcp_servers=_render_mcp_servers(greet_data.get("mcps")),
            ),
            emit_event=self._emit_event,
            emit_session_id=self._emit_session_id,
        )
        self._driver.start()

        self._watcher = RepoWatcher(
            work_root="/work",
            repos=[r.name for r in clone_results if not r.error],
            emit=lambda payload: self._safe_send(control.KIND_REPO_CHANGED, payload),
        )
        self._watcher.start()

        self._heartbeat_thread = threading.Thread(target=self._heartbeat_loop, name="shim-heartbeat", daemon=True)
        self._heartbeat_thread.start()

        if threading.current_thread() is threading.main_thread():
            signal.signal(signal.SIGTERM, lambda *_: self._stopping.set())
            signal.signal(signal.SIGINT, lambda *_: self._stopping.set())

        rc = self._inbound_loop()
        self._teardown()
        return rc

    def _discover_skills(self) -> list[dict]:
        out: list[dict] = []
        skills_dir = "/skills"
        if not os.path.isdir(skills_dir):
            return out
        for entry in sorted(os.listdir(skills_dir)):
            manifest = os.path.join(skills_dir, entry, "manifest.json")
            if os.path.exists(manifest):
                try:
                    with open(manifest, "r", encoding="utf-8") as f:
                        m = json.load(f)
                except Exception:  # noqa: BLE001
                    m = {}
                out.append({"name": m.get("name", entry), "description": m.get("description", "")})
        return out

    def _inbound_loop(self) -> int:
        assert self._client is not None
        for frame in self._client.iter_frames():
            if self._stopping.is_set():
                break
            kind = frame.get("kind")
            data = frame.get("data") or {}
            if kind == control.KIND_MESSAGE:
                self._handle_message(data)
            elif kind == control.KIND_INTERRUPT:
                self._handle_interrupt(data)
            elif kind == control.KIND_SNAPSHOT_REQUEST:
                self._handle_snapshot_request(data)
            elif kind == control.KIND_DIFF_REQUEST:
                self._handle_diff_request(data, fmt=data.get("format") or "unified", patch=False)
            elif kind == control.KIND_EXPORT_PATCH_REQUEST:
                self._handle_diff_request(data, fmt="unified", patch=True)
            elif kind == control.KIND_EXPORT_PUSH_REQUEST:
                self._handle_export_push_request(data)
            elif kind == control.KIND_SHUTDOWN:
                self._stopping.set()
                break
            else:
                self._safe_send(control.KIND_ERROR, {
                    "code": "unknown_inbound_kind",
                    "message": f"shim does not handle {kind!r}",
                    "fatal": False,
                })
        return 0

    def _handle_message(self, data: dict) -> None:
        if self._driver is None:
            return
        turn_id = data.get("message_id") or data.get("turn_id") or ""
        content = data.get("content") or ""
        self._driver.submit_turn(turn_id=turn_id, content=content)

    def _handle_interrupt(self, _data: dict) -> None:
        if self._driver is None:
            return
        self._driver.interrupt()

    def _handle_snapshot_request(self, data: dict) -> None:
        request_id = data.get("request_id", "")
        messages = rt.read_snapshot_jsonl("/work", self._sdk_session_id)
        self._safe_send(control.KIND_SNAPSHOT, {
            "request_id": request_id,
            "messages": messages,
        })

    def _handle_diff_request(self, data: dict, *, fmt: str, patch: bool) -> None:
        request_id = data.get("request_id", "")
        repo_name = data.get("repo") or ""
        bases = gitops.load_repo_bases()
        targets: list[gitops.RepoBase]
        if repo_name:
            sel = gitops.select_repo(bases, repo_name)
            if sel is None:
                self._safe_send(control.KIND_DIFF_END, {
                    "request_id": request_id,
                    "repo": repo_name,
                    "exit_code": 64,
                    "error": "repo not found",
                })
                return
            targets = [sel]
        else:
            targets = bases
        if not targets:
            self._safe_send(control.KIND_DIFF_END, {
                "request_id": request_id,
                "repo": "",
                "exit_code": 0,
                "note": "no repos recorded",
            })
            return
        for repo in targets:
            try:
                stream = (gitops.export_patch(repo) if patch
                          else gitops.diff(repo, fmt=fmt))
                for chunk in stream:
                    self._safe_send(control.KIND_DIFF_CHUNK, {
                        "request_id": request_id,
                        "repo": repo.name,
                        "data": base64.b64encode(chunk).decode("ascii"),
                    })
                payload = {
                    "request_id": request_id,
                    "repo": repo.name,
                    "exit_code": 0,
                    "branch": repo.branch,
                    "base_sha": repo.base_sha,
                }
                if repo.note:
                    payload["note"] = repo.note
                self._safe_send(control.KIND_DIFF_END, payload)
            except Exception as exc:  # noqa: BLE001
                self._safe_send(control.KIND_DIFF_END, {
                    "request_id": request_id,
                    "repo": repo.name,
                    "exit_code": 1,
                    "error": str(exc),
                })

    def _handle_export_push_request(self, data: dict) -> None:
        request_id = data.get("request_id", "")
        repo_name = data.get("repo") or ""
        branch = data.get("branch") or ""
        message = data.get("message") or ""
        bases = gitops.load_repo_bases()
        repo = gitops.select_repo(bases, repo_name) if repo_name else (bases[0] if bases else None)
        if repo is None:
            self._safe_send(control.KIND_EXPORT_PUSH_RESULT, {
                "request_id": request_id,
                "repo": repo_name,
                "branch": branch,
                "success": False,
                "output": "",
                "error": "repo not found",
            })
            return
        try:
            res = gitops.export_push(repo, branch, message)
        except Exception as exc:  # noqa: BLE001
            self._safe_send(control.KIND_EXPORT_PUSH_RESULT, {
                "request_id": request_id,
                "repo": repo.name,
                "branch": branch,
                "success": False,
                "output": "",
                "error": str(exc),
            })
            return
        self._safe_send(control.KIND_EXPORT_PUSH_RESULT, {
            "request_id": request_id,
            "repo": repo.name,
            "branch": res.branch,
            "success": res.success,
            "output": res.output,
            "error": res.error,
        })

    def _heartbeat_loop(self) -> None:
        while not self._stopping.is_set():
            self._safe_send(control.KIND_HEARTBEAT, {})
            self._stopping.wait(HEARTBEAT_SECONDS)

    def _emit_event(self, event_kind: str, payload: dict) -> None:
        # agentd's RuntimeEventData expects {"kind": ..., "data": {...}} — nest
        # the payload, don't flatten it into the parent envelope.
        self._safe_send(control.KIND_EVENT, {"kind": event_kind, "data": payload})

    def _emit_session_id(self, sid: str) -> None:
        self._sdk_session_id = sid
        self._safe_send(control.KIND_SESSION_ID, {"sdk_session_id": sid})

    def _safe_send(self, kind: str, data: dict) -> None:
        if self._client is None:
            return
        try:
            self._client.send(kind, data)
        except Exception:  # noqa: BLE001
            self._stopping.set()

    def _teardown(self) -> None:
        if self._watcher is not None:
            self._watcher.stop()
        if self._driver is not None:
            self._driver.shutdown(SHUTDOWN_GRACE_SECONDS)
        if self._client is not None:
            try:
                self._client.close()
            except Exception:  # noqa: BLE001
                pass


def _render_mcp_servers(mcps):
    """Translate agentd.greet's MCP list into ClaudeAgentOptions.mcp_servers.

    The wire format is a list of dicts with `name`, `url`, `transport`,
    `kind`, optional `headers`. The SDK wants a dict keyed by name where
    each value matches McpServerConfig (an SDK Union of Stdio/SSE/HTTP/SDK
    server configs). Unknown transports are skipped.
    """
    if not mcps:
        return {}
    if isinstance(mcps, dict):
        return mcps
    out = {}
    for m in mcps:
        if not isinstance(m, dict):
            continue
        name = m.get("name")
        transport = m.get("transport") or "http"
        if not name or transport not in ("http", "sse"):
            continue
        cfg = {"type": transport, "url": m.get("url", "")}
        headers = m.get("headers")
        if isinstance(headers, dict) and headers:
            cfg["headers"] = headers
        out[name] = cfg
    return out


def main(argv: Optional[list[str]] = None) -> int:
    addr = os.environ.get(CONTROL_ADDR_ENV) or os.environ.get(CONTROL_SOCK_ENV, "")
    if not addr:
        sys.stderr.write(
            f"shim: neither {CONTROL_ADDR_ENV} nor {CONTROL_SOCK_ENV} is set\n"
        )
        return 64
    meta = os.environ.get("AGENTCTL_SESSION_META", SESSION_META_DEFAULT)
    return Shim(control_addr=addr, session_meta=meta).run()


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
