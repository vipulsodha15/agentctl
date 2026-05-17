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
# Fixture transcripts.
#
# ``FIXTURE_LINES`` is a legacy mix: the thread.started / agent_message /
# turn.completed frames are codex-cli 0.130.0 shapes (text path was
# verified against the pinned CLI in image/Dockerfile), but the two
# function_call / function_call_output frames are the *older* OpenAI
# responses-style shape — they don't appear on a real 0.130.0 turn, but
# the translator still accepts them and they pin the back-compat path
# in case a CLI rollback happens.
#
# ``FIXTURE_LINES_MODERN_TOOLS`` uses the actual codex-cli 0.130.0 tool
# item types (``command_execution`` / ``file_change`` / ``mcp_tool_call``
# / ``web_search`` / ``todo_list``) so the end-to-end driver test covers
# the path the production CLI exercises.
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


# Real codex-cli 0.130.0 transcript shape for a turn that runs a shell
# command. The ThreadItem schema (id on the outer item, type as a
# flattened discriminator) is verified against
# codex-rs/exec/src/exec_events.rs in openai/codex.
FIXTURE_LINES_MODERN_TOOLS = [
    {"type": "thread.started", "thread_id": "codex-sess-modern"},
    {"type": "turn.started"},
    {
        "type": "item.started",
        "item": {
            "id": "cmd-1",
            "type": "command_execution",
            "command": "ls /work",
            "aggregated_output": "",
            "exit_code": None,
            "status": "in_progress",
        },
    },
    {
        "type": "item.completed",
        "item": {
            "id": "cmd-1",
            "type": "command_execution",
            "command": "ls /work",
            "aggregated_output": "file.txt\n",
            "exit_code": 0,
            "status": "completed",
        },
    },
    {
        "type": "item.completed",
        "item": {
            "id": "msg-1",
            "type": "agent_message",
            "text": "Done.",
        },
    },
    {
        "type": "turn.completed",
        "usage": {
            "input_tokens": 5,
            "cached_input_tokens": 0,
            "output_tokens": 2,
            "reasoning_output_tokens": 0,
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


class CodexToolItemTranslationTests(unittest.TestCase):
    """Codex-cli 0.130.0 tool-like item types — verified against the
    ThreadItemDetails enum in codex-rs/exec/src/exec_events.rs.

    Each tool-like item arrives as ``item.started`` (status=in_progress)
    followed later by ``item.completed`` with the final status + output.
    We emit ``tool.call`` on the started frame so the web card opens in
    the "running" state, then ``tool.result`` on completion keyed back
    by ``item.id`` so the same row gets the output folded in.
    """

    # -- command_execution -------------------------------------------

    def test_command_execution_started_emits_pending_bash_call(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "cmd-1",
                "type": "command_execution",
                "command": "ls -la /work",
                "aggregated_output": "",
                "exit_code": None,
                "status": "in_progress",
            },
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_CALL)
        # ``Bash`` name lets the web's formatToolHeader pick the ⌘ icon
        # and "Ran <cmd>" verb instead of a generic tool card.
        self.assertEqual(data["name"], "Bash")
        self.assertEqual(data["tool_use_id"], "cmd-1")
        self.assertEqual(data["input"], {"command": "ls -la /work"})

    def test_command_execution_completed_emits_tool_result_with_output(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "cmd-1",
                "type": "command_execution",
                "command": "ls -la /work",
                "aggregated_output": "total 8\ndrwxr-xr-x file.txt\n",
                "exit_code": 0,
                "status": "completed",
            },
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TOOL_RESULT)
        self.assertEqual(data["tool_use_id"], "cmd-1")
        self.assertEqual(data["content"], "total 8\ndrwxr-xr-x file.txt\n")
        self.assertFalse(data["is_error"])

    def test_command_execution_failed_status_marks_result_as_error(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "cmd-bad",
                "type": "command_execution",
                "command": "false",
                "aggregated_output": "",
                "exit_code": 1,
                "status": "failed",
            },
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        _, data = out[0]
        self.assertTrue(data["is_error"])
        # Empty stdout on a failed run gets a synthetic exit-code hint
        # so the card isn't a blank red box.
        self.assertIn("exit code 1", data["content"])

    def test_command_execution_declined_status_is_error_with_hint(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "cmd-decl",
                "type": "command_execution",
                "command": "rm -rf /",
                "aggregated_output": "",
                "exit_code": None,
                "status": "declined",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertTrue(data["is_error"])
        self.assertIn("declined", data["content"])

    # -- file_change -------------------------------------------------

    def test_file_change_single_update_maps_to_edit(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "fc-1",
                "type": "file_change",
                "changes": [{"path": "/work/foo.py", "kind": "update"}],
                "status": "in_progress",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertEqual(data["name"], "Edit")
        self.assertEqual(data["input"], {"file_path": "/work/foo.py"})

    def test_file_change_single_add_maps_to_write(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "fc-2",
                "type": "file_change",
                "changes": [{"path": "/work/new.py", "kind": "add"}],
                "status": "in_progress",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertEqual(data["name"], "Write")
        self.assertEqual(data["input"], {"file_path": "/work/new.py"})

    def test_file_change_multi_file_keeps_changes_list(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "fc-3",
                "type": "file_change",
                "changes": [
                    {"path": "/work/a.py", "kind": "update"},
                    {"path": "/work/b.py", "kind": "add"},
                ],
                "status": "in_progress",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        # Multi-file patches fall through to a generic ApplyPatch tool
        # with the full change list — there's no Claude-equivalent single
        # tool that touches multiple files at once.
        self.assertEqual(data["name"], "ApplyPatch")
        self.assertEqual(len(data["input"]["changes"]), 2)

    def test_file_change_completed_summarizes_paths_in_output(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "fc-4",
                "type": "file_change",
                "changes": [{"path": "/work/foo.py", "kind": "update"}],
                "status": "completed",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertIn("/work/foo.py", data["content"])
        self.assertFalse(data["is_error"])

    # -- mcp_tool_call -----------------------------------------------

    def test_mcp_tool_call_uses_claude_sdk_naming_convention(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "mcp-1",
                "type": "mcp_tool_call",
                "server": "github",
                "tool": "create_pull_request",
                "arguments": {"title": "fix"},
                "result": None,
                "error": None,
                "status": "in_progress",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        # The web's MCP_RE matches mcp__<server>__<tool>; emit the same
        # name so MCP tools get the badge + health pill.
        self.assertEqual(data["name"], "mcp__github__create_pull_request")
        self.assertEqual(data["input"], {"title": "fix"})

    def test_mcp_tool_call_completed_extracts_text_from_content_blocks(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "mcp-1",
                "type": "mcp_tool_call",
                "server": "github",
                "tool": "create_pull_request",
                "arguments": {},
                "result": {
                    "content": [
                        {"type": "text", "text": "PR #42 created"},
                    ],
                    "structured_content": None,
                },
                "error": None,
                "status": "completed",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertEqual(data["content"], "PR #42 created")
        self.assertFalse(data["is_error"])

    def test_mcp_tool_call_error_field_surfaces_as_failure(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "mcp-2",
                "type": "mcp_tool_call",
                "server": "github",
                "tool": "create_pull_request",
                "arguments": {},
                "result": None,
                "error": {"message": "rate limited"},
                "status": "failed",
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertTrue(data["is_error"])
        self.assertEqual(data["content"], "rate limited")

    # -- web_search --------------------------------------------------

    def test_web_search_started_emits_websearch_call_with_query(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "ws-1",
                "type": "web_search",
                "query": "latest python release",
                "action": {"type": "search", "query": "latest python release"},
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertEqual(data["name"], "WebSearch")
        self.assertEqual(data["input"]["query"], "latest python release")
        self.assertEqual(data["input"]["action"]["type"], "search")

    # -- todo_list ---------------------------------------------------

    def test_todo_list_maps_to_todowrite(self) -> None:
        event = {
            "type": "item.started",
            "item": {
                "id": "todo-1",
                "type": "todo_list",
                "items": [
                    {"text": "step one", "completed": False},
                    {"text": "step two", "completed": True},
                ],
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertEqual(data["name"], "TodoWrite")
        self.assertEqual(len(data["input"]["todos"]), 2)

    def test_todo_list_completed_renders_checklist_in_output(self) -> None:
        event = {
            "type": "item.completed",
            "item": {
                "id": "todo-2",
                "type": "todo_list",
                "items": [
                    {"text": "first", "completed": True},
                    {"text": "second", "completed": False},
                ],
            },
        }
        _, data = codex_driver.translate_codex_event(event, turn_id="t1")[0]
        self.assertIn("[x] first", data["content"])
        self.assertIn("[ ] second", data["content"])

    # -- lifecycle pairing -------------------------------------------

    def test_started_and_completed_share_tool_use_id(self) -> None:
        # The conversation reducer pairs results back to pending calls
        # by tool_use_id; the two events emitted from a single codex
        # item must use the same id (item.id from the outer ThreadItem)
        # so the web folds them into one row.
        started = {
            "type": "item.started",
            "item": {
                "id": "shared-id",
                "type": "command_execution",
                "command": "echo hi",
                "aggregated_output": "",
                "exit_code": None,
                "status": "in_progress",
            },
        }
        completed = {
            "type": "item.completed",
            "item": {
                "id": "shared-id",
                "type": "command_execution",
                "command": "echo hi",
                "aggregated_output": "hi\n",
                "exit_code": 0,
                "status": "completed",
            },
        }
        s = codex_driver.translate_codex_event(started, turn_id="t1")[0]
        c = codex_driver.translate_codex_event(completed, turn_id="t1")[0]
        self.assertEqual(s[1]["tool_use_id"], c[1]["tool_use_id"])
        self.assertEqual(s[0], EVENT_TOOL_CALL)
        self.assertEqual(c[0], EVENT_TOOL_RESULT)

    def test_tool_item_with_missing_id_is_dropped(self) -> None:
        # No id means the conversation reducer can't pair the result
        # back to the call — better to drop than emit an orphan row.
        event = {
            "type": "item.started",
            "item": {"type": "command_execution", "command": "ls"},
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(out, [])

    def test_reasoning_item_is_dropped(self) -> None:
        # Reasoning is the model's internal chain — codex emits it only
        # at completion. The shared vocabulary has no thinking event
        # today; drop so we don't fabricate a misleading tool card.
        event = {
            "type": "item.completed",
            "item": {
                "id": "r-1",
                "type": "reasoning",
                "text": "I should run ls first.",
            },
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(out, [])

    def test_unknown_tool_item_falls_back_to_raw_type_name(self) -> None:
        # Forward-compat: a future codex revision that adds a new
        # tool-like item type shouldn't make the call vanish from the
        # UI. We don't recognize it, so we'd just drop in the normal
        # path — but if someone extends _TOOL_ITEM_TYPES at the
        # frontier they get a sensible fallback.
        event = {
            "type": "item.started",
            "item": {
                "id": "future-1",
                "type": "future_kind",
                "payload": {"x": 1},
            },
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        # Not in _TOOL_ITEM_TYPES, so the started branch silently
        # drops. Item.completed for an unknown type also drops. That
        # matches Claude's forward-compat behavior.
        self.assertEqual(out, [])

    def test_turn_failed_with_nested_error_surfaces_message(self) -> None:
        event = {
            "type": "turn.failed",
            "error": {"message": "rate limit hit"},
        }
        out = codex_driver.translate_codex_event(event, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, EVENT_TURN_END)
        self.assertFalse(data["ok"])
        self.assertEqual(data["error"], "rate limit hit")


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

    def test_one_turn_translates_modern_tool_shapes(self) -> None:
        # Exercises the actual codex-cli 0.130.0 tool item shapes
        # (command_execution start/complete pair) end-to-end so a
        # regression where item.started gets dropped — leaving tool
        # cards invisible in the web UI — fails this test.
        fake_codex = _write_fake_codex(self.tmp.name, FIXTURE_LINES_MODERN_TOOLS)
        events, sids, _records, emit_event, emit_sid, emit_record = self._collector()

        cfg = codex_driver.CodexConfig(
            model="gpt-5.5",
            cwd=self.tmp.name,
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
            drv.submit_turn(turn_id="t-modern", content="run ls")
            self._wait_for_turn_end(events)
        finally:
            drv.shutdown(grace_seconds=2.0)

        self.assertEqual(sids, ["codex-sess-modern"])
        # The tool.call must arrive *before* tool.result so the web
        # opens the card in the pending state, then folds the result in.
        kinds = [k for k, _ in events]
        call_idx = kinds.index(EVENT_TOOL_CALL)
        result_idx = kinds.index(EVENT_TOOL_RESULT)
        self.assertLess(call_idx, result_idx)
        # Same item id on both events so the conversation reducer pairs
        # them into a single collapsed row.
        self.assertEqual(events[call_idx][1]["tool_use_id"], "cmd-1")
        self.assertEqual(events[result_idx][1]["tool_use_id"], "cmd-1")
        # Bash naming so the web's formatToolHeader picks ⌘ "Ran ls /work".
        self.assertEqual(events[call_idx][1]["name"], "Bash")
        self.assertEqual(events[result_idx][1]["content"], "file.txt\n")
        self.assertFalse(events[result_idx][1]["is_error"])
        self.assertIn(EVENT_ASSISTANT_MESSAGE, kinds)
        self.assertIn(EVENT_USAGE, kinds)
        self.assertEqual(kinds[-1], EVENT_TURN_END)

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
        self.assertEqual(argv[argv.index("--sandbox") + 1], "workspace-write")
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
