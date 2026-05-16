"""Shim-side tests for the mid-session model swap (ADR 0020 §2).

Two axes:

  * RuntimeDriver.set_model() — config update + client teardown wiring,
    exercised without a real claude-agent-sdk so the test runs anywhere.
  * Shim main dispatcher — verifies agentd.set_model frames reach the
    active driver's set_model(...). Mirrors the lifecycle test's mocking
    pattern so we don't actually spawn an SDK client.
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

from shim import control, runtime, __main__ as shim_main  # noqa: E402


class RuntimeDriverSetModelTests(unittest.TestCase):
    """Direct unit tests against RuntimeDriver.set_model."""

    def setUp(self) -> None:
        # Stub ClaudeAgentOptions so _options() doesn't require the real SDK.
        self._real_options = runtime.ClaudeAgentOptions
        runtime.ClaudeAgentOptions = type("FakeOptions", (), {
            "__init__": lambda self, **kw: setattr(self, "kwargs", kw)
        })
        self.addCleanup(self._restore_options)

    def _restore_options(self) -> None:
        runtime.ClaudeAgentOptions = self._real_options  # type: ignore[assignment]

    def _driver(self, model: str) -> runtime.RuntimeDriver:
        cfg = runtime.RuntimeConfig(model=model)
        return runtime.RuntimeDriver(
            cfg,
            emit_event=lambda *a, **kw: None,
            emit_session_id=lambda *a, **kw: None,
        )

    def test_set_model_updates_config(self) -> None:
        d = self._driver("claude-sonnet-4-6")
        d.set_model("claude-opus-4-7")
        self.assertEqual(d._cfg.model, "claude-opus-4-7")
        # Next _options() rebuild should reflect it.
        opts = d._options()
        self.assertEqual(opts.kwargs["model"], "claude-opus-4-7")

    def test_set_model_empty_is_noop(self) -> None:
        d = self._driver("claude-sonnet-4-6")
        d.set_model("")
        self.assertEqual(d._cfg.model, "claude-sonnet-4-6")

    def test_set_model_same_is_noop(self) -> None:
        d = self._driver("claude-sonnet-4-6")
        # Even if the loop were running, same-model should not swap. Without
        # a loop we just verify config stays the same and no exception.
        d.set_model("claude-sonnet-4-6")
        self.assertEqual(d._cfg.model, "claude-sonnet-4-6")

    def test_set_model_before_loop_starts_only_updates_cfg(self) -> None:
        """Loop not started → no swap scheduling, just the config update.

        The driver doesn't crash, and the next ensure_client (when start()
        is called) will pick up the new model from the rebuilt _options().
        """
        d = self._driver("claude-sonnet-4-6")
        # Don't start the loop. set_model should still work.
        d.set_model("claude-opus-4-7")
        self.assertEqual(d._cfg.model, "claude-opus-4-7")
        # No client created, no loop, no exceptions raised.
        self.assertIsNone(d._client)


class _CaptureDriver:
    """Minimal driver double for the main-loop dispatcher test."""

    def __init__(self, *_a, **_kw) -> None:
        self.started = False
        self.shutdown_called = False
        self.turns: list[tuple[str, str]] = []
        self.interrupts = 0
        self.model_calls: list[str] = []
        self.model = "claude-sonnet-4-6"

    def start(self) -> None:
        self.started = True

    def submit_turn(self, *, turn_id: str, content: str) -> None:
        self.turns.append((turn_id, content))

    def interrupt(self) -> None:
        self.interrupts += 1

    def set_model(self, new_model: str) -> None:
        self.model_calls.append(new_model)
        self.model = new_model

    def shutdown(self, grace_seconds: float = 30.0) -> None:
        self.shutdown_called = True


class _NoopWatcher:
    def __init__(self, **_kw) -> None:
        pass

    def start(self) -> None:
        pass

    def stop(self) -> None:
        pass


def _listen_unix(path: str) -> socket.socket:
    if os.path.exists(path):
        os.remove(path)
    s = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    s.bind(path)
    s.listen(1)
    return s


class ShimMainSetModelDispatchTests(unittest.TestCase):
    """End-to-end-ish: send agentd.set_model over the control channel and
    confirm it lands on the active driver."""

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.sock_path = os.path.join(self.tmp.name, "agentd.sock")
        self.meta_path = os.path.join(self.tmp.name, "session.json")
        with open(self.meta_path, "w") as f:
            json.dump({
                "session_id": "sess-set-model",
                "session_token": "tok",
                "model": "claude-sonnet-4-6",
            }, f)
        self.captured: _CaptureDriver | None = None

        original_driver = shim_main.rt.RuntimeDriver
        original_watcher = shim_main.RepoWatcher

        def factory(cfg, emit_event, emit_session_id, emit_message_record=None):
            self.captured = _CaptureDriver()
            return self.captured

        shim_main.rt.RuntimeDriver = factory  # type: ignore[assignment]
        shim_main.RepoWatcher = lambda **kw: _NoopWatcher(**kw)  # type: ignore[assignment]

        def restore() -> None:
            shim_main.rt.RuntimeDriver = original_driver
            shim_main.RepoWatcher = original_watcher

        self.addCleanup(restore)

    def test_set_model_frame_reaches_driver(self) -> None:
        listener = _listen_unix(self.sock_path)
        self.addCleanup(listener.close)

        shim = shim_main.Shim(control_addr=self.sock_path, session_meta=self.meta_path)
        rc_holder: dict[str, Any] = {}

        def shim_runner() -> None:
            rc_holder["rc"] = shim.run()

        t = threading.Thread(target=shim_runner, daemon=True)
        t.start()

        listener.settimeout(5.0)
        agentd_sock, _ = listener.accept()
        agentd = control.ControlClient(agentd_sock)

        hello = agentd.recv()
        self.assertEqual(hello["kind"], control.KIND_HELLO)
        agentd.send(control.KIND_GREET, {
            "session_id": "sess-set-model",
            "model": "claude-sonnet-4-6",
            "repos": [],
            "mcps": [],
            "limits": {},
        })
        # Drain runtime.ready so subsequent frames are seen in order.
        ready = agentd.recv()
        self.assertEqual(ready["kind"], control.KIND_READY)

        # Send the swap.
        agentd.send(control.KIND_SET_MODEL, {"model": "claude-opus-4-7"})
        deadline = time.time() + 2.0
        while time.time() < deadline:
            if self.captured and self.captured.model_calls:
                break
            time.sleep(0.02)
        self.assertIsNotNone(self.captured)
        self.assertEqual(self.captured.model_calls, ["claude-opus-4-7"])
        self.assertEqual(self.captured.model, "claude-opus-4-7")

        # Sanity: after a swap a subsequent turn still routes through the
        # driver (no broken dispatcher state).
        agentd.send(control.KIND_MESSAGE, {"message_id": "m-1", "content": "after swap"})
        deadline = time.time() + 2.0
        while time.time() < deadline:
            if self.captured.turns:
                break
            time.sleep(0.02)
        self.assertEqual(self.captured.turns, [("m-1", "after swap")])

        agentd.send(control.KIND_SHUTDOWN, {"grace_seconds": 1})
        t.join(timeout=5.0)
        self.assertFalse(t.is_alive(), "shim did not exit on shutdown")

    def test_empty_model_in_frame_is_dropped(self) -> None:
        """Bad input from a misbehaving daemon should be a no-op, not a crash."""

        listener = _listen_unix(self.sock_path)
        self.addCleanup(listener.close)

        shim = shim_main.Shim(control_addr=self.sock_path, session_meta=self.meta_path)

        t = threading.Thread(target=shim.run, daemon=True)
        t.start()

        listener.settimeout(5.0)
        sock, _ = listener.accept()
        agentd = control.ControlClient(sock)

        agentd.recv()  # hello
        agentd.send(control.KIND_GREET, {
            "session_id": "sess-set-model",
            "model": "claude-sonnet-4-6",
            "repos": [],
            "mcps": [],
            "limits": {},
        })
        agentd.recv()  # ready

        agentd.send(control.KIND_SET_MODEL, {"model": ""})
        # Give the shim a beat to ignore it.
        time.sleep(0.1)
        self.assertIsNotNone(self.captured)
        self.assertEqual(self.captured.model_calls, [])

        agentd.send(control.KIND_SHUTDOWN, {"grace_seconds": 1})
        t.join(timeout=5.0)


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
