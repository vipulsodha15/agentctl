"""Tests for the Codex driver — JSONL translation and per-turn lifecycle.

The Codex CLI JSONL schema is the per-ADR (0020 §"verify at impl")
unknown. The fixtures here encode the project's best-effort reading of
``codex exec --json`` as of refactor time; every shape that depends on
a guessed event field is also marked ``TODO(verify-codex-jsonl)`` in
the driver. Once we can run the pinned CLI version, swap these
fixtures for captured real output and the assertions should still
hold without code changes.

Two layers of coverage:

  * Pure translation: feed JSONL events to :func:`translate_codex_event`
    and assert the emitted internal-vocabulary tuples. No subprocess.
  * End-to-end driver: point the driver at a fake ``codex`` script that
    writes a fixture transcript to stdout, then assert the same events
    arrive at the emit callbacks plus that the session id is captured
    and ``turn.end`` ships.
"""

from __future__ import annotations

import json
import os
import stat
import sys
import tempfile
import threading
import time
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import runtime  # noqa: E402
from shim.runtime import codex_driver  # noqa: E402
from shim.runtime.translate import (  # noqa: E402
    EVENT_ASSISTANT_DELTA,
    EVENT_ASSISTANT_MESSAGE,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    EVENT_TURN_CANCELLED,
    EVENT_TURN_END,
    EVENT_USAGE,
)


# ---------------------------------------------------------------------------
# Fixture transcript — TODO(verify-codex-jsonl): synthesized from public
# docs; replace with a captured run once the CLI version is pinned.
# ---------------------------------------------------------------------------


FIXTURE_LINES = [
    {"type": "session.created", "session": {"id": "codex-sess-123"}},
    {
        "type": "item.delta",
        "item_id": "msg_1",
        "delta": {"text": "Hello"},
    },
    {
        "type": "item.delta",
        "item_id": "msg_1",
        "delta": {"text": ", world"},
    },
    {
        "type": "item.completed",
        "item": {
            "id": "msg_1",
            "type": "message",
            "content": [{"type": "output_text", "text": "Hello, world"}],
        },
    },
    {
        "type": "item.completed",
        "item": {
            "id": "call_42",
            "type": "function_call",
            "name": "shell",
            "arguments": "{\"cmd\": \"ls /work\"}",
        },
    },
    {
        "type": "item.completed",
        "item": {
            "id": "out_42",
            "type": "function_call_output",
            "call_id": "call_42",
            "output": "file.txt\n",
            "is_error": False,
        },
    },
    {
        "type": "turn.completed",
        "model": "gpt-5.5",
        "usage": {
            "input_tokens": 12,
            "output_tokens": 7,
            "cached_input_tokens": 3,
        },
    },
]


class TranslateCodexEventTests(unittest.TestCase):
    """Pure-translation coverage — no subprocess, no driver."""

    def test_session_created_yields_no_event(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[0], turn_id="t1",
        )
        # The session id is consumed via _extract_codex_session_id; the
        # translator itself emits nothing for the bare session frame.
        self.assertEqual(out, [])

    def test_item_delta_emits_assistant_delta_with_delta_key(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[1], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_ASSISTANT_DELTA)
        self.assertEqual(data["turn_id"], "t1")
        self.assertEqual(data["delta"], "Hello")
        # Wire-key strictness — the Go renderer drops anything that
        # isn't named `delta`, same as the Claude assistant.delta frame.
        self.assertNotIn("text", data)

    def test_item_completed_message_emits_assistant_message(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[3], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_ASSISTANT_MESSAGE)
        self.assertEqual(data["content"], "Hello, world")
        self.assertNotIn("text", data)

    def test_function_call_emits_tool_call_with_decoded_input(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[4], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_CALL)
        self.assertEqual(data["tool_use_id"], "call_42")
        self.assertEqual(data["name"], "shell")
        self.assertEqual(data["input"], {"cmd": "ls /work"})

    def test_function_call_output_emits_tool_result(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[5], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_RESULT)
        self.assertEqual(data["tool_use_id"], "call_42")
        self.assertEqual(data["content"], "file.txt\n")
        self.assertFalse(data["is_error"])

    def test_turn_completed_emits_usage(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[6], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_USAGE)
        self.assertEqual(data["model"], "gpt-5.5")
        self.assertEqual(data["input_tokens"], 12)
        self.assertEqual(data["output_tokens"], 7)
        self.assertEqual(data["cache_read_tokens"], 3)
        self.assertEqual(data["cache_write_tokens"], 0)

    def test_legacy_prompt_completion_token_names_are_accepted(self) -> None:
        # Older Codex builds emitted ``prompt_tokens`` /
        # ``completion_tokens`` — keep them flowing into the same usage
        # event so a CLI flip doesn't zero our cost rows.
        event = {
            "type": "turn.completed",
            "model": "gpt-5.5",
            "usage": {"prompt_tokens": 4, "completion_tokens": 9},
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(len(out), 1)
        _, data = out[0]
        self.assertEqual(data["input_tokens"], 4)
        self.assertEqual(data["output_tokens"], 9)

    def test_error_event_emits_failed_turn_end(self) -> None:
        out = codex_driver.translate_codex_event(
            {"type": "error", "message": "boom"}, turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TURN_END)
        self.assertFalse(data["ok"])
        self.assertEqual(data["error"], "boom")

    def test_unknown_event_type_drops_silently(self) -> None:
        # Forward-compat: future CLI revisions may add event types we
        # don't recognize; same rule as the Claude translator.
        out = codex_driver.translate_codex_event(
            {"type": "future.shape", "foo": "bar"}, turn_id="t1",
        )
        self.assertEqual(out, [])


class CodexDriverFactoryTests(unittest.TestCase):
    """``runtime.get_driver`` dispatches the right concrete class."""

    def _make_options(self, **overrides) -> dict:
        opts = {
            "model": "gpt-5.5",
            "cwd": "/work",
            "emit_event": lambda *_a, **_kw: None,
            "emit_session_id": lambda *_a, **_kw: None,
        }
        opts.update(overrides)
        return opts

    def test_get_driver_openai_returns_codex_driver(self) -> None:
        drv = runtime.get_driver("openai", self._make_options())
        self.assertIsInstance(drv, codex_driver.CodexDriver)

    def test_get_driver_codex_alias_returns_codex_driver(self) -> None:
        drv = runtime.get_driver("codex", self._make_options())
        self.assertIsInstance(drv, codex_driver.CodexDriver)

    def test_get_driver_default_is_anthropic(self) -> None:
        # An older agentd that doesn't send `provider` must still land on
        # the Claude driver — backward compat for one release per the
        # ADR-0020 §7 dispatcher rule.
        drv = runtime.get_driver(None, self._make_options())
        self.assertIsInstance(drv, runtime.RuntimeDriver)

    def test_get_driver_empty_string_is_anthropic(self) -> None:
        drv = runtime.get_driver("", self._make_options())
        self.assertIsInstance(drv, runtime.RuntimeDriver)

    def test_get_driver_unknown_provider_raises(self) -> None:
        with self.assertRaises(ValueError):
            runtime.get_driver("gemini", self._make_options())


def _write_fake_codex(tmpdir: str, transcript: list[dict], exit_code: int = 0) -> str:
    """Write a tiny shell script that mimics ``codex exec --json``.

    Emits the transcript verbatim to stdout (one JSON object per line)
    and exits with ``exit_code``. The script ignores all CLI args — the
    driver's argv assembly is exercised elsewhere; here we only care
    about the JSONL parse / emit path.
    """

    script_path = os.path.join(tmpdir, "fake-codex.sh")
    body_lines = ["#!/bin/sh"]
    for ev in transcript:
        body_lines.append("printf '%s\\n' " + _shell_quote(json.dumps(ev)))
    body_lines.append(f"exit {exit_code}")
    with open(script_path, "w") as f:
        f.write("\n".join(body_lines) + "\n")
    os.chmod(script_path, os.stat(script_path).st_mode | stat.S_IEXEC | stat.S_IXGRP | stat.S_IXOTH)
    return script_path


def _shell_quote(s: str) -> str:
    # Single-quote, escape embedded single quotes.
    return "'" + s.replace("'", "'\\''") + "'"


class CodexDriverSubprocessTests(unittest.TestCase):
    """End-to-end: drive a fake `codex` and assert event flow."""

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)

    def _collector(self):
        events: list[tuple[str, dict]] = []
        session_ids: list[str] = []
        records: list[dict] = []
        lock = threading.Lock()

        def emit_event(kind: str, data: dict) -> None:
            with lock:
                events.append((kind, dict(data)))

        def emit_sid(sid: str) -> None:
            with lock:
                session_ids.append(sid)

        def emit_record(rec: dict) -> None:
            with lock:
                records.append(dict(rec))

        return events, session_ids, records, emit_event, emit_sid, emit_record

    def _wait_for_turn_end(self, events: list[tuple[str, dict]], timeout: float = 5.0) -> None:
        deadline = time.time() + timeout
        while time.time() < deadline:
            if any(k in (EVENT_TURN_END, EVENT_TURN_CANCELLED) for k, _ in events):
                return
            time.sleep(0.02)
        self.fail(
            "timed out waiting for turn.end/turn.cancelled; "
            f"events so far: {[k for k, _ in events]}"
        )

    def test_one_turn_translates_full_fixture(self) -> None:
        fake_codex = _write_fake_codex(self.tmp.name, FIXTURE_LINES)
        events, sids, records, emit_event, emit_sid, emit_record = self._collector()

        cfg = codex_driver.CodexConfig(
            model="gpt-5.5",
            cwd=self.tmp.name,  # the cwd just needs to exist for the spawn
            codex_bin=fake_codex,
        )
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=emit_event,
            emit_session_id=emit_sid,
            emit_message_record=emit_record,
        )
        drv.start()
        try:
            drv.submit_turn(turn_id="t1", content="hi")
            self._wait_for_turn_end(events)
        finally:
            drv.shutdown(grace_seconds=2.0)

        kinds = [k for k, _ in events]
        # First turn captures the session id from session.created.
        self.assertEqual(sids, ["codex-sess-123"])
        # Deltas, the completed message, the tool call+result, usage,
        # then turn.end. The exact ordering within a turn is preserved
        # by the line-by-line consumer.
        self.assertIn(EVENT_ASSISTANT_DELTA, kinds)
        self.assertIn(EVENT_ASSISTANT_MESSAGE, kinds)
        self.assertIn(EVENT_TOOL_CALL, kinds)
        self.assertIn(EVENT_TOOL_RESULT, kinds)
        self.assertIn(EVENT_USAGE, kinds)
        self.assertEqual(kinds[-1], EVENT_TURN_END)
        end_payload = events[-1][1]
        self.assertEqual(end_payload["turn_id"], "t1")
        self.assertTrue(end_payload["ok"])

        # The optional message-record callback gets every JSONL line
        # verbatim — agentd dedups on the daemon side.
        self.assertEqual(len(records), len(FIXTURE_LINES))

    def test_nonzero_exit_emits_failed_turn_end(self) -> None:
        # Subset of the transcript followed by a nonzero exit: the
        # driver should still flush the events it saw and then ship a
        # turn.end with ok=False.
        transcript = FIXTURE_LINES[:2]
        fake_codex = _write_fake_codex(self.tmp.name, transcript, exit_code=2)
        events, _sids, _records, emit_event, emit_sid, _emit_record = self._collector()

        cfg = codex_driver.CodexConfig(
            model="gpt-5.5",
            cwd=self.tmp.name,
            codex_bin=fake_codex,
        )
        drv = codex_driver.CodexDriver(
            cfg, emit_event=emit_event, emit_session_id=emit_sid,
        )
        drv.start()
        try:
            drv.submit_turn(turn_id="t-bad", content="x")
            self._wait_for_turn_end(events)
        finally:
            drv.shutdown(grace_seconds=2.0)

        last_kind, last_data = events[-1]
        self.assertEqual(last_kind, EVENT_TURN_END)
        self.assertFalse(last_data["ok"])

    def test_missing_binary_emits_failed_turn_end(self) -> None:
        events, _sids, _records, emit_event, emit_sid, _emit_record = self._collector()
        cfg = codex_driver.CodexConfig(
            model="gpt-5.5",
            cwd=self.tmp.name,
            codex_bin="/does/not/exist/codex",
        )
        drv = codex_driver.CodexDriver(
            cfg, emit_event=emit_event, emit_session_id=emit_sid,
        )
        drv.start()
        try:
            drv.submit_turn(turn_id="t-nox", content="x")
            self._wait_for_turn_end(events, timeout=2.0)
        finally:
            drv.shutdown(grace_seconds=1.0)

        last_kind, last_data = events[-1]
        self.assertEqual(last_kind, EVENT_TURN_END)
        self.assertFalse(last_data["ok"])
        self.assertIn("codex", last_data["error"].lower())

    def test_interrupt_kills_running_turn(self) -> None:
        # Sleep long enough that we can interrupt before it exits. The
        # script doesn't emit anything so the driver is parked in
        # readline() when we kill it.
        script_path = os.path.join(self.tmp.name, "sleeper.sh")
        with open(script_path, "w") as f:
            # ``exec`` so the shell hands its pid off to sleep — without
            # it our SIGTERM hits the shell but the sleep grandchild
            # keeps the stdout pipe open and the driver's reader never
            # unblocks.
            f.write("#!/bin/sh\nexec sleep 30\n")
        os.chmod(script_path, os.stat(script_path).st_mode | stat.S_IEXEC)

        events, _sids, _records, emit_event, emit_sid, _emit_record = self._collector()
        cfg = codex_driver.CodexConfig(
            model="gpt-5.5",
            cwd=self.tmp.name,
            codex_bin=script_path,
        )
        drv = codex_driver.CodexDriver(
            cfg, emit_event=emit_event, emit_session_id=emit_sid,
        )
        drv.start()
        try:
            drv.submit_turn(turn_id="t-int", content="hang")
            # Give the subprocess a moment to start, then interrupt.
            time.sleep(0.2)
            drv.interrupt()
            self._wait_for_turn_end(events, timeout=3.0)
        finally:
            drv.shutdown(grace_seconds=2.0)

        last_kind, _ = events[-1]
        self.assertEqual(last_kind, EVENT_TURN_CANCELLED)


class CodexDriverArgvTests(unittest.TestCase):
    """Argv assembly per ADR 0020 §7 — keeps the codex CLI contract crisp."""

    def test_argv_includes_all_required_flags(self) -> None:
        cfg = codex_driver.CodexConfig(model="gpt-5.5", cwd="/work")
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        argv = drv._build_argv("hello world")
        self.assertEqual(argv[0:2], [codex_driver.CODEX_BIN_DEFAULT, "exec"])
        self.assertIn("--json", argv)
        # Model, sandbox, ask-for-approval, cd — these are the four
        # things ADR-0020 §7 says every Codex turn must carry.
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.5")
        self.assertEqual(argv[argv.index("--sandbox") + 1], "workspace-write")
        self.assertEqual(argv[argv.index("--ask-for-approval") + 1], "never")
        self.assertEqual(argv[argv.index("--cd") + 1], "/work")
        self.assertEqual(argv[-1], "hello world")
        # No --resume flag on a fresh session (resume=None).
        self.assertNotIn("--resume", argv)

    def test_argv_resume_appended_when_session_id_known(self) -> None:
        cfg = codex_driver.CodexConfig(
            model="gpt-5.5", cwd="/work", resume="codex-sess-existing",
        )
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        argv = drv._build_argv("continue")
        self.assertEqual(argv[argv.index("--resume") + 1], "codex-sess-existing")


class CodexDriverSetModelTests(unittest.TestCase):
    """ADR 0020 §4.3 — set_model on the Codex driver is just a config
    bump because codex respawns per turn. No subprocess interaction; the
    next _build_argv reflects the new model.
    """

    def _make_driver(self, model: str = "gpt-5.5") -> codex_driver.CodexDriver:
        cfg = codex_driver.CodexConfig(model=model, cwd="/work")
        return codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )

    def test_set_model_updates_argv_on_next_turn(self) -> None:
        drv = self._make_driver(model="gpt-5.5")
        drv.set_model("gpt-5.3-codex")
        argv = drv._build_argv("hi")
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.3-codex")

    def test_set_model_empty_is_noop(self) -> None:
        drv = self._make_driver(model="gpt-5.5")
        drv.set_model("")
        argv = drv._build_argv("hi")
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.5")

    def test_set_model_unchanged_is_noop(self) -> None:
        drv = self._make_driver(model="gpt-5.5")
        # Should not raise / not change anything observable.
        drv.set_model("gpt-5.5")
        argv = drv._build_argv("hi")
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.5")


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
