"""Tests for runtime.translate_message wire-format compliance.

The Go-side renderers (CLI + web) unmarshal assistant frames by JSON tag,
so the wire keys are strict: assistant.delta carries ``delta``,
assistant.message carries ``content``. A typo silently leaves the
rendered text blank.
"""

from __future__ import annotations

import os
import sys
import unittest
from dataclasses import dataclass
from typing import Any

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.dirname(os.path.dirname(HERE)))

from shim import runtime  # noqa: E402


@dataclass
class _StreamEventFake:
    event: dict


# Mimic the SDK 0.1.x StreamEvent so `type(message).__name__ == "StreamEvent"`.
_StreamEventFake.__name__ = "StreamEvent"


@dataclass
class _TextBlock:
    text: str
    type: str = "text"


_TextBlock.__name__ = "TextBlock"


@dataclass
class _ToolUseBlock:
    id: str = "tu_1"
    name: str = "Read"
    input: dict = None
    type: str = "tool_use"


_ToolUseBlock.__name__ = "ToolUseBlock"


@dataclass
class _ToolResultBlock:
    tool_use_id: str = "tu_1"
    content: Any = "ok"
    is_error: bool = False
    type: str = "tool_result"


_ToolResultBlock.__name__ = "ToolResultBlock"


@dataclass
class _AssistantMessageFake:
    content: list
    model: str = "claude-opus-4-7"


_AssistantMessageFake.__name__ = "AssistantMessage"


@dataclass
class _UserMessageFake:
    content: list


_UserMessageFake.__name__ = "UserMessage"


class TranslateMessageTest(unittest.TestCase):
    def test_stream_event_text_delta_maps_to_delta_key(self) -> None:
        ev = _StreamEventFake(
            event={
                "type": "content_block_delta",
                "index": 0,
                "delta": {"type": "text_delta", "text": "pong"},
            }
        )
        out = runtime.translate_message(ev, turn_id="t1")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, runtime.EVENT_ASSISTANT_DELTA)
        self.assertEqual(data["turn_id"], "t1")
        self.assertEqual(data["delta"], "pong")
        # The Go renderer ignores anything not tagged `delta`; reject a stray
        # `text` key to catch regressions.
        self.assertNotIn("text", data)

    def test_stream_event_non_text_delta_drops_silently(self) -> None:
        ev = _StreamEventFake(
            event={"type": "content_block_delta", "delta": {"type": "input_json_delta"}}
        )
        self.assertEqual(runtime.translate_message(ev, turn_id="t1"), [])

    def test_assistant_message_emits_content_not_text(self) -> None:
        msg = _AssistantMessageFake(content=[_TextBlock(text="hello")])
        out = runtime.translate_message(msg, turn_id="t2")
        kinds = [k for k, _ in out]
        self.assertIn(runtime.EVENT_ASSISTANT_MESSAGE, kinds)
        data = next(d for k, d in out if k == runtime.EVENT_ASSISTANT_MESSAGE)
        self.assertEqual(data["content"], "hello")
        self.assertNotIn("text", data)

    def test_assistant_message_with_only_tool_use_emits_tool_call(self) -> None:
        # When Claude follows up a tool result with another batch of tool
        # calls and no text, the SDK delivers an AssistantMessage whose
        # content is purely tool_use blocks. The collapsed text is "" — we
        # must not emit an assistant.message for that (no empty bubble), but
        # we MUST emit a tool.call so the web shows the collapsed tool
        # widget live during the turn instead of after the JSONL flush.
        msg = _AssistantMessageFake(
            content=[_ToolUseBlock(id="tu_42", name="Read", input={"path": "a.go"})]
        )
        out = runtime.translate_message(msg, turn_id="t3")
        kinds = [k for k, _ in out]
        self.assertNotIn(runtime.EVENT_ASSISTANT_MESSAGE, kinds)
        self.assertIn(runtime.EVENT_TOOL_CALL, kinds)
        data = next(d for k, d in out if k == runtime.EVENT_TOOL_CALL)
        self.assertEqual(data["turn_id"], "t3")
        self.assertEqual(data["tool_use_id"], "tu_42")
        self.assertEqual(data["name"], "Read")
        self.assertEqual(data["input"], {"path": "a.go"})

    def test_assistant_message_with_text_and_tool_use_emits_both(self) -> None:
        # Text-then-tool is the common interleaved shape: the agent narrates
        # a step, then fires the tool. Both must appear on the wire so the
        # bubble seals and the tool widget renders directly below.
        msg = _AssistantMessageFake(
            content=[
                _TextBlock(text="Reading the file."),
                _ToolUseBlock(id="tu_99", name="Read", input={"path": "b.go"}),
            ]
        )
        out = runtime.translate_message(msg, turn_id="t4")
        kinds = [k for k, _ in out]
        self.assertEqual(
            kinds[:2],
            [runtime.EVENT_ASSISTANT_MESSAGE, runtime.EVENT_TOOL_CALL],
        )
        text_data = next(d for k, d in out if k == runtime.EVENT_ASSISTANT_MESSAGE)
        self.assertEqual(text_data["content"], "Reading the file.")
        tool_data = next(d for k, d in out if k == runtime.EVENT_TOOL_CALL)
        self.assertEqual(tool_data["tool_use_id"], "tu_99")

    def test_user_message_with_tool_result_emits_tool_result(self) -> None:
        # The SDK packs tool results inside a follow-up UserMessage whose
        # content array carries ToolResultBlock items. Without surfacing
        # these as tool.result events, the pending tool widget on the web
        # spins forever until the end-of-turn JSONL flush.
        msg = _UserMessageFake(
            content=[_ToolResultBlock(tool_use_id="tu_99", content="line1\nline2")]
        )
        out = runtime.translate_message(msg, turn_id="t5")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, runtime.EVENT_TOOL_RESULT)
        self.assertEqual(data["turn_id"], "t5")
        self.assertEqual(data["tool_use_id"], "tu_99")
        self.assertEqual(data["content"], "line1\nline2")
        self.assertFalse(data["is_error"])

    def test_user_message_with_dict_tool_result_emits_event(self) -> None:
        # Some SDK versions deliver content blocks as plain dicts — make sure
        # we handle that shape too.
        msg = _UserMessageFake(
            content=[
                {
                    "type": "tool_result",
                    "tool_use_id": "tu_77",
                    "content": "boom",
                    "is_error": True,
                }
            ]
        )
        out = runtime.translate_message(msg, turn_id="t6")
        self.assertEqual(len(out), 1)
        kind, data = out[0]
        self.assertEqual(kind, runtime.EVENT_TOOL_RESULT)
        self.assertEqual(data["tool_use_id"], "tu_77")
        self.assertTrue(data["is_error"])


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
