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


@dataclass
class _AssistantMessageFake:
    content: list
    model: str = "claude-opus-4-7"


_AssistantMessageFake.__name__ = "AssistantMessage"


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


if __name__ == "__main__":  # pragma: no cover
    unittest.main()
