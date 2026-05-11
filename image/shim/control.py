"""NDJSON framing on the control channel.

Wire spec lives in architecture/api.md §4. Both directions speak
``{ "v":1, "seq":N, "kind":"...", "ts":"...", "data":{} }``; max line length
is 1 MiB. Backpressure (drop-on-overflow of ``runtime.event`` of kind
``assistant.delta``) is enforced by ``agentd`` on its read side; the shim
only frames and ships.

Transport is a TCP connection to ``host.docker.internal:<port>`` (see
``connect_address``). Access is gated by the ``session_token`` carried in
``runtime.hello``; the listener is bound to ``127.0.0.1`` on the host so
only the local Docker daemon's containers can reach it.
"""

from __future__ import annotations

import json
import os
import socket
import threading
import time
from typing import Any, Iterator, Optional


PROTOCOL_VERSION = 1
MAX_FRAME_BYTES = 1 << 20

KIND_HELLO = "runtime.hello"
KIND_READY = "runtime.ready"
KIND_EVENT = "runtime.event"
KIND_ERROR = "runtime.error"
KIND_SESSION_ID = "runtime.session_id"
KIND_HEARTBEAT = "runtime.heartbeat"
KIND_SNAPSHOT = "runtime.snapshot"
KIND_REPO_CHANGED = "repo.changed"
KIND_DIFF_CHUNK = "runtime.diff_chunk"
KIND_DIFF_END = "runtime.diff_end"
KIND_EXPORT_PUSH_RESULT = "runtime.export_push_result"

KIND_GREET = "agentd.greet"
KIND_MESSAGE = "agentd.message"
KIND_INTERRUPT = "agentd.interrupt"
KIND_SNAPSHOT_REQUEST = "agentd.snapshot_request"
KIND_SHUTDOWN = "agentd.shutdown"
KIND_AGENTD_ERROR = "agentd.error"
KIND_DIFF_REQUEST = "agentd.diff_request"
KIND_EXPORT_PATCH_REQUEST = "agentd.export_patch_request"
KIND_EXPORT_PUSH_REQUEST = "agentd.export_push_request"


class FrameTooLarge(Exception):
    pass


def _now() -> str:
    return time.strftime("%Y-%m-%dT%H:%M:%S.000Z", time.gmtime())


class ControlClient:
    def __init__(self, sock: socket.socket) -> None:
        self._sock = sock
        self._reader = sock.makefile("rb", buffering=0)
        self._writer = sock.makefile("wb", buffering=0)
        self._write_lock = threading.Lock()
        self._seq_out = 0
        self._closed = False

    @classmethod
    def connect(cls, path: str, timeout: float = 5.0) -> "ControlClient":
        s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        s.settimeout(timeout)
        s.connect(path)
        s.settimeout(None)
        return cls(s)

    @classmethod
    def connect_address(cls, address: str, timeout: float = 5.0) -> "ControlClient":
        """Connect to *address*.

        Accepts a unix socket path (anything starting with ``/``, kept for
        tests) or a TCP ``host:port``. Production agentd hands the shim a
        ``host.docker.internal:<port>`` address via ``AGENTCTL_CONTROL_ADDR``
        because Docker Desktop's host-fs share refuses to pass a bind-mounted
        unix socket through to the container.
        """
        if not address:
            raise OSError("control: empty address")
        if address.startswith("/"):
            return cls.connect(address, timeout=timeout)
        host, sep, port = address.rpartition(":")
        if not sep or not host or not port:
            raise OSError(f"control: malformed address {address!r}")
        host = host.strip("[]")
        port_num = int(port)
        s = socket.create_connection((host, port_num), timeout=timeout)
        s.settimeout(None)
        return cls(s)

    def send(self, kind: str, data: Optional[dict] = None) -> None:
        if self._closed:
            raise OSError("control client is closed")
        self._seq_out += 1
        frame = {
            "v": PROTOCOL_VERSION,
            "seq": self._seq_out,
            "kind": kind,
            "ts": _now(),
            "data": data or {},
        }
        body = json.dumps(frame, separators=(",", ":")).encode("utf-8")
        if len(body) + 1 > MAX_FRAME_BYTES:
            raise FrameTooLarge(f"frame {kind} of {len(body)} bytes exceeds 1 MiB")
        with self._write_lock:
            self._writer.write(body + b"\n")
            self._writer.flush()

    def recv(self) -> Optional[dict]:
        line_parts: list[bytes] = []
        size = 0
        while True:
            chunk = self._reader.readline()
            if not chunk:
                if line_parts:
                    raise OSError("control sock closed mid-frame")
                return None
            size += len(chunk)
            if size > MAX_FRAME_BYTES:
                raise FrameTooLarge(f"inbound frame exceeds {MAX_FRAME_BYTES} bytes")
            line_parts.append(chunk)
            if chunk.endswith(b"\n"):
                break
        line = b"".join(line_parts).rstrip(b"\r\n")
        if not line:
            return self.recv()
        return json.loads(line.decode("utf-8"))

    def iter_frames(self) -> Iterator[dict]:
        while True:
            f = self.recv()
            if f is None:
                return
            yield f

    def close(self) -> None:
        self._closed = True
        try:
            self._writer.close()
        except Exception:
            pass
        try:
            self._reader.close()
        except Exception:
            pass
        try:
            self._sock.close()
        except Exception:
            pass


def write_session_metadata(directory: str, payload: dict) -> str:
    """Write a ``session.json`` style payload to ``directory`` mode 0640.

    Used in tests and by callers that need to seed the shim's expected
    metadata file. Production agentd writes this from internal/sm.
    """

    os.makedirs(directory, exist_ok=True)
    path = os.path.join(directory, "session.json")
    fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_TRUNC, 0o640)
    with os.fdopen(fd, "w") as f:
        json.dump(payload, f)
    return path


def read_session_metadata(directory: str) -> dict:
    path = os.path.join(directory, "session.json")
    with open(path, "r", encoding="utf-8") as f:
        return json.load(f)


def hello_payload(session_token: str, *, shim_version: str, sdk_version: str) -> dict:
    return {
        "session_token": session_token,
        "shim_version": shim_version,
        "sdk_version": sdk_version,
        "sdk": "claude-agent-sdk-python",
        "pid": os.getpid(),
        "capabilities": [
            KIND_HELLO,
            KIND_READY,
            KIND_EVENT,
            KIND_ERROR,
            KIND_SESSION_ID,
            KIND_HEARTBEAT,
            KIND_SNAPSHOT,
            KIND_REPO_CHANGED,
        ],
    }


def stderr_filter(line: str) -> bool:
    """Return True if *line* should be forwarded to agentd's stderr capture.

    Filters the upstream-known-noisy ``Error in hook callback hook_0: Stream
    closed`` lines that ``bypassPermissions`` mode emits per tool call (issue
    anthropics/claude-code#23728). Errors are non-fatal; tools complete
    normally.
    """

    return "Error in hook callback hook_0: Stream closed" not in line
