"""M1 placeholder shim. M2 wires this to claude-agent-sdk.

This shim opens the bind-mounted control sock if present, sends
``runtime.hello``, prints any frames it receives, and exits cleanly when it
sees ``agentd.shutdown``. Real SDK integration (run loop, runtime.event,
runtime.snapshot) lands in M2.
"""

from __future__ import annotations

import json
import os
import socket
import sys
import time
from typing import Any


CONTROL_SOCK = "/run/agentctl/control/agentd.sock"


def emit(stream: Any, payload: dict) -> None:
    line = json.dumps(payload, separators=(",", ":"))
    stream.write(line + "\n")
    stream.flush()


def main() -> int:
    if not os.path.exists(CONTROL_SOCK):
        sys.stderr.write(
            "shim: control socket not present at "
            + CONTROL_SOCK
            + " — running in offline placeholder mode\n"
        )
        sys.stderr.flush()
        # M2 lands the real SDK loop. For M1 we exit cleanly so the container
        # does not become a zombie when run for a smoke test.
        return 0

    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.connect(CONTROL_SOCK)
    fp = sock.makefile("rwb")
    hello = {
        "v": 1,
        "seq": 0,
        "kind": "runtime.hello",
        "ts": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "data": {
            "shim_version": "0.1.0-m1",
            "sdk_version": "claude-agent-sdk==0.1.80",
            "sdk": "claude-agent-sdk-python",
            "pid": os.getpid(),
            "capabilities": ["runtime.hello", "agentd.shutdown"],
        },
    }
    fp.write((json.dumps(hello) + "\n").encode("utf-8"))
    fp.flush()

    while True:
        line = fp.readline()
        if not line:
            return 0
        try:
            frame = json.loads(line.decode("utf-8"))
        except json.JSONDecodeError:
            continue
        if frame.get("kind") == "agentd.shutdown":
            return 0


if __name__ == "__main__":
    sys.exit(main())
