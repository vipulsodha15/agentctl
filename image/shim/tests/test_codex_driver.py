"""Tests for the Codex driver — JSONL translation and per-turn lifecycle.

The fixtures here mirror the JSONL schema codex-cli 0.130.0 actually
emits (captured against the pinned CLI in the container image):

  * ``thread.started`` (with ``thread_id``) instead of the older
    ``session.created`` shape.
  * ``item.completed`` with ``item.type == "agent_message"`` and text on
    a top-level ``text`` field.

Older shapes (``session.created`` / ``message`` items / ``content[]``
blocks / ``prompt_tokens`` usage keys) are still covered in dedicated
back-compat tests so a CLI revision flip is caught by CI rather than in
production.

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
# Fixture transcript — matches codex-cli 0.130.0 (captured against the
# pinned CLI in image/Dockerfile). The two function_call frames at the
# tail are kept on the older shape for forward-compat coverage; they
# don't appear on a plain assistant turn but exercise the tool-call /
# tool-result translation paths.
# ---------------------------------------------------------------------------


FIXTURE_LINES = [
    {"type": "thread.started", "thread_id": "codex-sess-123"},
    {"type": "turn.started"},
    {
        "type": "item.completed",
        "item": {
            "id": "item_0",
            "type": "agent_message",
            "text": "Hello, world",
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

    def test_thread_started_yields_no_event(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[0], turn_id="t1",
        )
        # The thread id is consumed via _extract_codex_session_id; the
        # translator itself emits nothing for the bare start frame.
        self.assertEqual(out, [])

    def test_extract_session_id_from_thread_started(self) -> None:
        sid = codex_driver._extract_codex_session_id(FIXTURE_LINES[0])
        self.assertEqual(sid, "codex-sess-123")

    def test_extract_session_id_back_compat_session_created(self) -> None:
        # Older CLI revisions used `session.created` — keep the fallback
        # so a build flip in either direction doesn't silently break.
        sid = codex_driver._extract_codex_session_id(
            {"type": "session.created", "session": {"id": "legacy-sid"}},
        )
        self.assertEqual(sid, "legacy-sid")

    def test_item_completed_agent_message_emits_assistant_message(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[2], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_ASSISTANT_MESSAGE)
        self.assertEqual(data["content"], "Hello, world")
        self.assertNotIn("text", data)

    def test_item_completed_legacy_message_shape_still_works(self) -> None:
        # Back-compat: older CLI revisions emitted item.type == "message"
        # with text in a content[] block list. Keep the translator
        # accepting that shape so a revision rollback doesn't blank out
        # assistant rendering.
        legacy = {
            "type": "item.completed",
            "item": {
                "id": "msg_1",
                "type": "message",
                "content": [{"type": "output_text", "text": "legacy"}],
            },
        }
        out = codex_driver.translate_codex_event(legacy, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_ASSISTANT_MESSAGE)
        self.assertEqual(data["content"], "legacy")

    def test_item_delta_emits_assistant_delta(self) -> None:
        # codex-cli 0.130.0 doesn't stream `item.delta` on plain
        # assistant turns, but the translator still accepts the shape so
        # we're forward-compatible if a future revision adds streaming.
        delta_event = {
            "type": "item.delta",
            "item_id": "msg_1",
            "delta": {"text": "Hello"},
        }
        out = codex_driver.translate_codex_event(delta_event, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_ASSISTANT_DELTA)
        self.assertEqual(data["turn_id"], "t1")
        self.assertEqual(data["delta"], "Hello")
        # Wire-key strictness — the Go renderer drops anything that
        # isn't named `delta`, same as the Claude assistant.delta frame.
        self.assertNotIn("text", data)

    def test_function_call_emits_tool_call_with_decoded_input(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[3], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_CALL)
        self.assertEqual(data["tool_use_id"], "call_42")
        self.assertEqual(data["name"], "shell")
        self.assertEqual(data["input"], {"cmd": "ls /work"})

    def test_function_call_output_emits_tool_result(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[4], turn_id="t1",
        )
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_RESULT)
        self.assertEqual(data["tool_use_id"], "call_42")
        self.assertEqual(data["content"], "file.txt\n")
        self.assertFalse(data["is_error"])

    def test_turn_completed_emits_usage(self) -> None:
        out = codex_driver.translate_codex_event(
            FIXTURE_LINES[5], turn_id="t1",
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
        # First turn captures the thread id from thread.started.
        self.assertEqual(sids, ["codex-sess-123"])
        # Completed message, tool call+result, usage, then turn.end.
        # codex-cli 0.130.0 doesn't stream item.delta on plain messages,
        # so the assistant text only arrives via item.completed.
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
    """Argv assembly verified against codex-cli 0.130.0.

    The 0.130.0 CLI rejects `--ask-for-approval` after `exec` and has no
    top-level `--resume` flag (resume is the `exec resume` subcommand).
    These tests pin both invariants.
    """

    def test_argv_first_turn_has_global_approval_and_exec_options(self) -> None:
        cfg = codex_driver.CodexConfig(model="gpt-5.5", cwd="/work")
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        argv = drv._build_argv("hello world")
        # `--ask-for-approval` is global and must precede `exec`.
        self.assertEqual(
            argv[0:4],
            [codex_driver.CODEX_BIN_DEFAULT, "--ask-for-approval", "never", "exec"],
        )
        self.assertIn("--json", argv)
        self.assertIn("--skip-git-repo-check", argv)
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.5")
        self.assertEqual(argv[argv.index("--sandbox") + 1], "danger-full-access")
        self.assertEqual(argv[argv.index("--cd") + 1], "/work")
        self.assertEqual(argv[-1], "hello world")
        # No resume subcommand on a fresh session.
        self.assertNotIn("resume", argv)
        # No top-level --resume flag (the 0.130.0 CLI doesn't accept it).
        self.assertNotIn("--resume", argv)

    def test_argv_resume_uses_exec_resume_subcommand(self) -> None:
        cfg = codex_driver.CodexConfig(
            model="gpt-5.5", cwd="/work", resume="codex-sess-existing",
        )
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        argv = drv._build_argv("continue")
        # `exec resume <SID>` is positional; sandbox/cd are inherited
        # from the recorded session and must be omitted on resume.
        exec_idx = argv.index("exec")
        self.assertEqual(argv[exec_idx + 1], "resume")
        self.assertEqual(argv[exec_idx + 2], "codex-sess-existing")
        self.assertNotIn("--sandbox", argv)
        self.assertNotIn("--cd", argv)
        # Model and --json still carry through on resume.
        self.assertEqual(argv[argv.index("--model") + 1], "gpt-5.5")
        self.assertIn("--json", argv)

    def test_argv_system_prompt_passes_model_instructions_file(self) -> None:
        cfg = codex_driver.CodexConfig(
            model="gpt-5.5", cwd="/work", system_prompt="be terse",
        )
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        try:
            argv = drv._build_argv("hi")
            # The override sits behind a `-c` config flag; the value is
            # TOML so the path is double-quoted.
            self.assertIn("-c", argv)
            override = argv[argv.index("-c") + 1]
            self.assertTrue(
                override.startswith('model_instructions_file="')
                and override.endswith('"'),
                f"unexpected override shape: {override!r}",
            )
            # And the file was actually written with the prompt.
            path = override[len('model_instructions_file="'):-1]
            with open(path, "r", encoding="utf-8") as f:
                self.assertEqual(f.read(), "be terse")
        finally:
            drv.shutdown(grace_seconds=1.0)

    def test_argv_no_system_prompt_omits_override(self) -> None:
        cfg = codex_driver.CodexConfig(model="gpt-5.5", cwd="/work")
        drv = codex_driver.CodexDriver(
            cfg,
            emit_event=lambda *_a, **_kw: None,
            emit_session_id=lambda *_a, **_kw: None,
        )
        argv = drv._build_argv("hi")
        # Without a system prompt we don't add a `-c` config override —
        # the codex CLI's defaults (AGENTS.md, etc.) take over.
        self.assertNotIn("-c", argv)


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
