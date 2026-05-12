"""End-to-end integration test for the shim's control loop.

Mocks claude-agent-sdk by replacing ``RuntimeDriver`` with a fake before
``Shim`` boots. Verifies:

  * runtime.hello carries the session_token from session.json
  * agentd.greet causes runtime.ready to be sent with started_at
  * agentd.message calls driver.submit_turn
  * agentd.interrupt calls driver.interrupt
  * agentd.snapshot_request returns runtime.snapshot with the JSONL body
  * agentd.shutdown causes a clean exit
"""

from __future__ import annotations

import json
import os
import socket
import sys
import tempfile
import threading
import time
import unittest
from typing import Any

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import control, __main__ as shim_main  # noqa: E402


class FakeDriver:
    def __init__(self, cfg, emit_event, emit_session_id, emit_message_record=None):
        self.cfg = cfg
        self.emit_event = emit_event
        self.emit_session_id = emit_session_id
        self.emit_message_record = emit_message_record
        self.turns: list[tuple[str, str]] = []
        self.interrupts = 0
        self.started = False
        self.shutdown_called = False

    def start(self):
        self.started = True

    def submit_turn(self, *, turn_id: str, content: str):
        self.turns.append((turn_id, content))

    def interrupt(self):
        self.interrupts += 1

    def shutdown(self, grace_seconds: float = 30.0):
        self.shutdown_called = True


class FakeWatcher:
    def __init__(self, **kwargs):
        self.kwargs = kwargs
    def start(self):
        pass
    def stop(self):
        pass


def listen_unix(path: str) -> socket.socket:
    if os.path.exists(path):
        os.remove(path)
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.bind(path)
    s.listen(1)
    return s


class ShimMainTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.sock_path = os.path.join(self.tmp.name, "agentd.sock")
        self.meta_path = os.path.join(self.tmp.name, "session.json")
        with open(self.meta_path, "w") as f:
            json.dump({
                "session_id": "sess-1",
                "session_token": "good",
                "sdk_session_id": "sdk-prev",
                "model": "claude-3-5-sonnet",
            }, f)

        self.captured_driver: FakeDriver | None = None
        original_driver = shim_main.rt.RuntimeDriver
        original_watcher = shim_main.RepoWatcher

        def driver_factory(cfg, emit_event, emit_session_id, emit_message_record=None):
            self.captured_driver = FakeDriver(
                cfg, emit_event, emit_session_id, emit_message_record=emit_message_record,
            )
            return self.captured_driver

        shim_main.rt.RuntimeDriver = driver_factory  # type: ignore[assignment]
        shim_main.RepoWatcher = lambda **kwargs: FakeWatcher(**kwargs)  # type: ignore[assignment]

        def restore():
            shim_main.rt.RuntimeDriver = original_driver
            shim_main.RepoWatcher = original_watcher

        self.addCleanup(restore)

    def test_full_session_lifecycle(self):
        listener = listen_unix(self.sock_path)
        self.addCleanup(listener.close)

        shim = shim_main.Shim(control_addr=self.sock_path, session_meta=self.meta_path)
        rc_holder: dict[str, Any] = {}

        def shim_runner():
            rc_holder["rc"] = shim.run()

        t = threading.Thread(target=shim_runner, daemon=True)
        t.start()

        listener.settimeout(5.0)
        agentd_sock, _ = listener.accept()
        agentd = control.ControlClient(agentd_sock)

        hello = agentd.recv()
        self.assertEqual(hello["kind"], control.KIND_HELLO)
        self.assertEqual(hello["data"]["session_token"], "good")

        agentd.send(control.KIND_GREET, {
            "session_id": "sess-1",
            "model": "claude-3-5-sonnet",
            "repos": [],
            "mcps": [],
            "limits": {},
        })

        # First post-greet frame should be runtime.ready.
        ready = agentd.recv()
        self.assertEqual(ready["kind"], control.KIND_READY)
        self.assertIn("started_at", ready["data"])

        # Sending a message must reach the driver.
        agentd.send(control.KIND_MESSAGE, {"message_id": "m-1", "content": "hi"})
        deadline = time.time() + 2.0
        while time.time() < deadline:
            if self.captured_driver and self.captured_driver.turns:
                break
            time.sleep(0.02)
        self.assertIsNotNone(self.captured_driver)
        self.assertEqual(self.captured_driver.turns, [("m-1", "hi")])

        agentd.send(control.KIND_INTERRUPT, {"reason": "user"})
        deadline = time.time() + 2.0
        while time.time() < deadline:
            if self.captured_driver.interrupts >= 1:
                break
            time.sleep(0.02)
        self.assertEqual(self.captured_driver.interrupts, 1)

        # Snapshot reads the JSONL on the volume; create one.
        os.makedirs("/tmp/test-volume/.claude/projects/-work", exist_ok=True)
        jsonl_path = "/tmp/test-volume/.claude/projects/-work/sdk-prev.jsonl"
        with open(jsonl_path, "w") as f:
            f.write(json.dumps({"role": "user", "content": "prior"}) + "\n")
            f.write(json.dumps({"role": "assistant", "content": "ack"}) + "\n")

        # The shim hard-codes /work; bypass by patching the snapshot helper.
        original_read = shim_main.rt.read_snapshot_jsonl
        shim_main.rt.read_snapshot_jsonl = lambda root, sid: original_read("/tmp/test-volume", sid)
        try:
            agentd.send(control.KIND_SNAPSHOT_REQUEST, {"request_id": "req-1"})
            snap = self._wait_for_kind(agentd, control.KIND_SNAPSHOT, timeout=3.0)
            self.assertEqual(snap["data"]["request_id"], "req-1")
            self.assertEqual(len(snap["data"]["messages"]), 2)
        finally:
            shim_main.rt.read_snapshot_jsonl = original_read

        agentd.send(control.KIND_SHUTDOWN, {"grace_seconds": 1})
        t.join(timeout=5.0)
        self.assertFalse(t.is_alive(), "shim did not exit on shutdown")
        self.assertEqual(rc_holder.get("rc"), 0)
        self.assertTrue(self.captured_driver.shutdown_called)

    def _wait_for_kind(self, client: control.ControlClient, kind: str, timeout: float) -> dict:
        deadline = time.time() + timeout
        while time.time() < deadline:
            f = client.recv()
            if f is None:
                self.fail("connection closed before kind=" + kind)
            if f["kind"] == kind:
                return f
        self.fail("timed out waiting for kind=" + kind)


if __name__ == "__main__":
    unittest.main()
