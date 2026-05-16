"""Driver package — one driver per provider, dispatched by :func:`get_driver`.

Layout (ADR 0020 §7):

* :mod:`shim.runtime.translate` — shared event-vocabulary constants
  every driver emits.
* :mod:`shim.runtime.claude_driver` — wraps ``claude-agent-sdk``.
* :mod:`shim.runtime.codex_driver` — shells out to ``codex exec --json``.

The factory below is what ``__main__.py`` calls after parsing the
``agentd.greet`` frame. The Driver protocol is structural — any object
with ``start`` / ``submit_turn`` / ``interrupt`` / ``shutdown`` /
``set_model`` satisfies it; we don't enforce an ABC because the tests
already monkey-patch the concrete classes and we want that to keep
working without rewrapping fakes.

Backward-compat re-exports
--------------------------

Pre-refactor callers imported names directly from ``shim.runtime`` (the
old single-file module). Most prominently the existing unit tests do::

    from shim import runtime
    runtime.ClaudeAgentOptions = Fake          # test_runtime_options
    runtime.EVENT_ASSISTANT_DELTA              # test_runtime_translate
    shim_main.rt.RuntimeDriver = factory       # test_shim_main
    shim_main.rt.read_snapshot_jsonl(...)      # test_shim_main

To keep the refactor strictly additive on the test surface, we re-export
the Claude-driver names and event constants at package scope. New code
should import from the explicit submodule (``shim.runtime.claude_driver``,
``shim.runtime.translate``); the re-exports exist so the existing tests
and any out-of-tree callers do not break in this commit.
"""

from __future__ import annotations

from typing import Any, Optional, Protocol, runtime_checkable

from . import claude_driver as _claude_driver
from . import codex_driver as _codex_driver
from .translate import (
    EVENT_ASSISTANT_DELTA,
    EVENT_ASSISTANT_MESSAGE,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    EVENT_TURN_CANCELLED,
    EVENT_TURN_END,
    EVENT_TURN_START,
    EVENT_USAGE,
)

# --- Backward-compat re-exports (see module docstring) ---------------------
# Claude driver — formerly ``shim.runtime.RuntimeDriver`` /
# ``shim.runtime.RuntimeConfig`` / ``shim.runtime.read_snapshot_jsonl``.
RuntimeDriver = _claude_driver.RuntimeDriver
RuntimeConfig = _claude_driver.RuntimeConfig
translate_message = _claude_driver.translate_message
read_snapshot_jsonl = _claude_driver.read_snapshot_jsonl
ClaudeAgentOptions = _claude_driver.ClaudeAgentOptions
ClaudeSDKClient = _claude_driver.ClaudeSDKClient

# Codex driver — new in this commit; re-exported for symmetry and so
# tests can monkey-patch via the package facade if they choose.
CodexDriver = _codex_driver.CodexDriver
CodexConfig = _codex_driver.CodexConfig
translate_codex_event = _codex_driver.translate_codex_event


PROVIDER_ANTHROPIC = "anthropic"
PROVIDER_OPENAI = "openai"
DEFAULT_PROVIDER = PROVIDER_ANTHROPIC


@runtime_checkable
class Driver(Protocol):
    """Structural protocol every provider driver satisfies.

    Used purely for type hints / documentation; the factory returns
    whichever concrete driver matches the provider. The shim's call
    sites are duck-typed.
    """

    def start(self) -> None: ...
    def submit_turn(self, *, turn_id: str, content: str) -> None: ...
    def interrupt(self) -> None: ...
    def shutdown(self, grace_seconds: float = 30.0) -> None: ...
    def set_model(self, model: str) -> None: ...


def get_driver(
    provider: Optional[str],
    options: dict,
) -> Any:
    """Build and return the driver for ``provider``.

    ``options`` is a flat dict carrying everything the chosen driver
    needs to construct its config plus the three emit callbacks the
    shim wires through:

      * ``emit_event``: callable ``(kind, payload)`` for runtime.event
      * ``emit_session_id``: callable ``(sid)`` for runtime.session_id
      * ``emit_message_record``: optional callable ``(record)`` for
        runtime.message_record

    Per-provider config keys are passed through to the driver's config
    dataclass; unknown keys are ignored (so the same call site works as
    we grow per-provider knobs).

    The default provider is ``"anthropic"`` for backward compat — old
    agentd builds that don't yet send ``provider`` in ``agentd.greet``
    still get the Claude driver they always did. The default lives here
    (one source of truth) so we can drop it once every agentd build is
    new enough.
    """

    name = (provider or DEFAULT_PROVIDER).strip().lower() or DEFAULT_PROVIDER

    emit_event = options["emit_event"]
    emit_session_id = options["emit_session_id"]
    emit_message_record = options.get("emit_message_record")

    if name in (PROVIDER_ANTHROPIC, "claude"):
        # Resolve via globals() so tests that override the package facade
        # (``shim_main.rt.RuntimeDriver = fake``) still see their fake
        # here. Keeping the indirection on a single line means new
        # callsites don't have to know about the legacy patch shape.
        config_cls = globals().get("RuntimeConfig") or _claude_driver.RuntimeConfig
        driver_cls = globals().get("RuntimeDriver") or _claude_driver.RuntimeDriver
        cfg = config_cls(
            model=options.get("model", "") or "",
            cwd=options.get("cwd", "/work") or "/work",
            resume=options.get("resume") or None,
            mcp_servers=options.get("mcp_servers") or None,
            system_prompt=options.get("system_prompt") or None,
        )
        return driver_cls(
            cfg,
            emit_event=emit_event,
            emit_session_id=emit_session_id,
            emit_message_record=emit_message_record,
        )

    if name in (PROVIDER_OPENAI, "codex"):
        config_cls = globals().get("CodexConfig") or _codex_driver.CodexConfig
        driver_cls = globals().get("CodexDriver") or _codex_driver.CodexDriver
        cfg = config_cls(
            model=options.get("model", "") or "",
            cwd=options.get("cwd", "/work") or "/work",
            resume=options.get("resume") or None,
            system_prompt=options.get("system_prompt") or None,
        )
        return driver_cls(
            cfg,
            emit_event=emit_event,
            emit_session_id=emit_session_id,
            emit_message_record=emit_message_record,
        )

    raise ValueError(f"unknown provider {provider!r} (expected anthropic|openai)")


__all__ = [
    # Factory + protocol
    "get_driver",
    "Driver",
    "DEFAULT_PROVIDER",
    "PROVIDER_ANTHROPIC",
    "PROVIDER_OPENAI",
    # Shared vocabulary
    "EVENT_ASSISTANT_DELTA",
    "EVENT_ASSISTANT_MESSAGE",
    "EVENT_TOOL_CALL",
    "EVENT_TOOL_RESULT",
    "EVENT_USAGE",
    "EVENT_TURN_START",
    "EVENT_TURN_END",
    "EVENT_TURN_CANCELLED",
    # Backward-compat re-exports
    "RuntimeDriver",
    "RuntimeConfig",
    "translate_message",
    "read_snapshot_jsonl",
    "ClaudeAgentOptions",
    "ClaudeSDKClient",
    "CodexDriver",
    "CodexConfig",
    "translate_codex_event",
]
