"""Shared event vocabulary for the runtime drivers.

Every provider driver (Claude, Codex, …) emits the same internal event
kinds defined here; agentd unmarshals them by tag and the CLI/web
renderers never inspect provider. Keep this file driver-agnostic — no
Anthropic-specific or Codex-specific helpers belong here, only the
constants both drivers translate *into*.

The wire keys are fixed by architecture/api.md §5; renaming a field
will silently break the Go renderers (they unmarshal by tag and drop
unknown keys).
"""

from __future__ import annotations


EVENT_ASSISTANT_DELTA = "assistant.delta"
EVENT_ASSISTANT_MESSAGE = "assistant.message"
EVENT_TOOL_CALL = "tool.call"
EVENT_TOOL_RESULT = "tool.result"
EVENT_USAGE = "usage"
EVENT_TURN_START = "turn.start"
EVENT_TURN_END = "turn.end"
EVENT_TURN_CANCELLED = "turn.cancelled"


__all__ = [
    "EVENT_ASSISTANT_DELTA",
    "EVENT_ASSISTANT_MESSAGE",
    "EVENT_TOOL_CALL",
    "EVENT_TOOL_RESULT",
    "EVENT_USAGE",
    "EVENT_TURN_START",
    "EVENT_TURN_END",
    "EVENT_TURN_CANCELLED",
]
