"""claude-agent-sdk integration.

Drives one-or-more turns against the SDK's ``ClaudeSDKClient``, translating
the typed event stream into ``runtime.event`` frames per
container-and-image.md §2.6 step 7. SDK persistence lives at
``/work/.claude/projects/-work/<sdk_session_id>.jsonl`` (the ``cwd=/work``
directory encoded as ``-work``); the entrypoint sets up the symlink.

Resume happens via ``ClaudeAgentOptions(resume=<sdk_session_id>)`` on every
container start after the very first; the session id is captured from the
first SDK message of the initial turn and persisted by agentd through the
``runtime.session_id`` frame.
"""

from __future__ import annotations

import asyncio
import os
import threading
from dataclasses import dataclass
from typing import Any, Callable, Optional

try:
    from claude_agent_sdk import ClaudeAgentOptions, ClaudeSDKClient
except Exception:  # noqa: BLE001
    ClaudeAgentOptions = None  # type: ignore[assignment]
    ClaudeSDKClient = None  # type: ignore[assignment]


EVENT_ASSISTANT_DELTA = "assistant.delta"
EVENT_ASSISTANT_MESSAGE = "assistant.message"
EVENT_TOOL_CALL = "tool.call"
EVENT_TOOL_RESULT = "tool.result"
EVENT_USAGE = "usage"
EVENT_TURN_START = "turn.start"
EVENT_TURN_END = "turn.end"
EVENT_TURN_CANCELLED = "turn.cancelled"


EmitEvent = Callable[[str, dict], None]


@dataclass
class RuntimeConfig:
    model: str
    cwd: str = "/work"
    permission_mode: str = "bypassPermissions"
    resume: Optional[str] = None
    mcp_servers: Optional[dict] = None


class RuntimeDriver:
    """Wrap the async ClaudeSDKClient in a thread-friendly facade.

    The shim's main loop is sync (blocking control-sock reads). Turns run on
    a dedicated asyncio thread; control commands (interrupt, snapshot) are
    posted to it via ``asyncio.run_coroutine_threadsafe``.
    """

    def __init__(self, cfg: RuntimeConfig, emit_event: EmitEvent, emit_session_id: Callable[[str], None]) -> None:
        self._cfg = cfg
        self._emit = emit_event
        self._emit_session_id = emit_session_id
        self._loop: Optional[asyncio.AbstractEventLoop] = None
        self._loop_ready = threading.Event()
        self._loop_thread: Optional[threading.Thread] = None
        self._client: Any = None
        self._client_lock = threading.Lock()
        self._sdk_session_id: Optional[str] = cfg.resume
        self._in_flight_lock = threading.Lock()
        self._in_flight: Optional[asyncio.Task] = None

    def start(self) -> None:
        self._loop_thread = threading.Thread(target=self._run_loop, name="shim-runtime", daemon=True)
        self._loop_thread.start()
        self._loop_ready.wait()

    def _run_loop(self) -> None:
        self._loop = asyncio.new_event_loop()
        asyncio.set_event_loop(self._loop)
        self._loop_ready.set()
        try:
            self._loop.run_forever()
        finally:
            self._loop.close()

    def _options(self) -> Any:
        if ClaudeAgentOptions is None:
            raise RuntimeError("claude_agent_sdk not installed")
        kwargs: dict[str, Any] = dict(
            model=self._cfg.model,
            permission_mode=self._cfg.permission_mode,
            cwd=self._cfg.cwd,
            include_partial_messages=True,
        )
        if self._cfg.mcp_servers:
            kwargs["mcp_servers"] = self._cfg.mcp_servers
        if self._sdk_session_id:
            kwargs["resume"] = self._sdk_session_id
        return ClaudeAgentOptions(**kwargs)

    async def _ensure_client(self) -> Any:
        if self._client is not None:
            return self._client
        if ClaudeSDKClient is None:
            raise RuntimeError("claude_agent_sdk not installed")
        client = ClaudeSDKClient(options=self._options())
        await client.connect()
        self._client = client
        return client

    def submit_turn(self, *, turn_id: str, content: str) -> None:
        if self._loop is None:
            raise RuntimeError("driver not started")
        coro = self._run_turn(turn_id, content)
        fut = asyncio.run_coroutine_threadsafe(coro, self._loop)
        # Track the underlying task for interrupt; the future itself wraps it.
        with self._in_flight_lock:
            self._in_flight = fut

    def interrupt(self) -> None:
        with self._in_flight_lock:
            fut = self._in_flight
            self._in_flight = None
        if fut is None or self._loop is None:
            return
        try:
            asyncio.run_coroutine_threadsafe(self._cancel_client(), self._loop)
        except Exception:  # noqa: BLE001
            pass
        try:
            fut.cancel()
        except Exception:  # noqa: BLE001
            pass

    async def _cancel_client(self) -> None:
        client = self._client
        if client is None:
            return
        for name in ("interrupt", "cancel", "abort"):
            method = getattr(client, name, None)
            if method is None:
                continue
            try:
                result = method()
                if asyncio.iscoroutine(result):
                    await result
                return
            except Exception:  # noqa: BLE001
                continue

    def shutdown(self, grace_seconds: float = 30.0) -> None:
        with self._in_flight_lock:
            fut = self._in_flight
            self._in_flight = None
        if fut is not None:
            try:
                fut.result(timeout=grace_seconds)
            except Exception:  # noqa: BLE001
                pass
        if self._loop is None:
            return
        try:
            close_fut = asyncio.run_coroutine_threadsafe(self._close_client(), self._loop)
            close_fut.result(timeout=grace_seconds)
        except Exception:  # noqa: BLE001
            pass
        self._loop.call_soon_threadsafe(self._loop.stop)
        if self._loop_thread is not None:
            self._loop_thread.join(timeout=grace_seconds)

    async def _close_client(self) -> None:
        client = self._client
        if client is None:
            return
        for name in ("disconnect", "close", "aclose"):
            method = getattr(client, name, None)
            if method is None:
                continue
            try:
                result = method()
                if asyncio.iscoroutine(result):
                    await result
                self._client = None
                return
            except Exception:  # noqa: BLE001
                continue
        self._client = None

    async def _run_turn(self, turn_id: str, content: str) -> None:
        try:
            client = await self._ensure_client()
        except Exception as exc:  # noqa: BLE001
            self._emit(EVENT_TURN_END, {"turn_id": turn_id, "ok": False, "error": str(exc)})
            return

        # agentd's actor broadcasts turn.start the moment it hands the message
        # to the shim; re-emitting here produced a duplicate frame that the CLI
        # renders as a second blank `assistant: ` line. Keep turn.end as our
        # responsibility — the actor uses it to settle in-flight state.

        try:
            await client.query(content)
        except Exception as exc:  # noqa: BLE001
            self._emit(EVENT_TURN_END, {"turn_id": turn_id, "ok": False, "error": str(exc)})
            return

        cancelled = False
        try:
            async for message in client.receive_response():
                if self._sdk_session_id is None:
                    sid = _extract_session_id(message)
                    if sid:
                        self._sdk_session_id = sid
                        self._emit_session_id(sid)
                for kind, data in translate_message(message, turn_id=turn_id):
                    self._emit(kind, data)
        except asyncio.CancelledError:
            cancelled = True
        except Exception as exc:  # noqa: BLE001
            self._emit(EVENT_TURN_END, {"turn_id": turn_id, "ok": False, "error": str(exc)})
            return

        if cancelled:
            self._emit(EVENT_TURN_CANCELLED, {"turn_id": turn_id})
        else:
            self._emit(EVENT_TURN_END, {"turn_id": turn_id, "ok": True})


def _extract_session_id(message: Any) -> Optional[str]:
    for attr in ("session_id", "sdk_session_id"):
        sid = getattr(message, attr, None)
        if isinstance(sid, str) and sid:
            return sid
    return None


def translate_message(message: Any, *, turn_id: str) -> list[tuple[str, dict]]:
    """Map a single SDK message into ``runtime.event`` payload tuples.

    The SDK's message types are versioned and may grow; we recognize the
    documented shapes (assistant text/delta/message, tool call/result,
    per-turn usage block) and pass through anything else as raw event
    metadata. Each tuple is ``(event_kind, data)`` ready for the wire.
    """

    out: list[tuple[str, dict]] = []
    cls = type(message).__name__
    # claude-agent-sdk 0.1.x delivers token-level partials as `StreamEvent`s
    # whose `.event` mirrors the raw Anthropic streaming wire format. Older
    # names (AssistantDelta / PartialAssistantMessage / TextDelta) are kept
    # for forward compatibility with future SDKs.
    if cls == "StreamEvent":
        text = _stream_event_text(getattr(message, "event", None))
        if text:
            out.append((EVENT_ASSISTANT_DELTA, {"turn_id": turn_id, "delta": text}))
        return out
    if cls in ("AssistantDelta", "PartialAssistantMessage", "TextDelta"):
        text = getattr(message, "text", None) or getattr(message, "delta", "") or ""
        # Wire keys are fixed by architecture/api.md §5: assistant.delta carries
        # `delta`, assistant.message carries `content`. Don't rename to `text` —
        # the Go CLI/web renderers unmarshal these by tag and silently drop a
        # mis-named field, leaving the assistant lines blank.
        out.append((EVENT_ASSISTANT_DELTA, {"turn_id": turn_id, "delta": text}))
        return out
    if cls in ("AssistantMessage", "AssistantFinal"):
        text = getattr(message, "text", None)
        if text is None:
            text = _coerce_assistant_text(getattr(message, "content", None))
        out.append((EVENT_ASSISTANT_MESSAGE, {"turn_id": turn_id, "content": text or ""}))
        usage = getattr(message, "usage", None)
        if usage is not None:
            out.append((EVENT_USAGE, {"turn_id": turn_id, "model": getattr(message, "model", ""), **_usage_dict(usage)}))
        return out
    if cls in ("ToolUseBlock", "ToolCall", "ToolUse"):
        out.append((EVENT_TOOL_CALL, {
            "turn_id": turn_id,
            "tool_use_id": getattr(message, "id", None) or getattr(message, "tool_use_id", ""),
            "name": getattr(message, "name", ""),
            "input": _to_jsonable(getattr(message, "input", {})),
        }))
        return out
    if cls in ("ToolResultBlock", "ToolResult"):
        out.append((EVENT_TOOL_RESULT, {
            "turn_id": turn_id,
            "tool_use_id": getattr(message, "tool_use_id", ""),
            "content": _to_jsonable(getattr(message, "content", "")),
            "is_error": bool(getattr(message, "is_error", False)),
        }))
        return out
    if cls in ("ResultMessage", "TurnEnd"):
        usage = getattr(message, "usage", None)
        if usage is not None:
            out.append((EVENT_USAGE, {"turn_id": turn_id, "model": getattr(message, "model", ""), **_usage_dict(usage)}))
        return out
    if cls == "SystemMessage":
        return out
    return out


def _usage_dict(usage: Any) -> dict:
    def _g(name: str) -> int:
        # claude-agent-sdk emits usage as a plain dict (not a typed object).
        if isinstance(usage, dict):
            v = usage.get(name, 0)
        else:
            v = getattr(usage, name, 0)
        if v is None:
            return 0
        try:
            return int(v)
        except (TypeError, ValueError):
            return 0

    return {
        "input_tokens": _g("input_tokens"),
        "output_tokens": _g("output_tokens"),
        "cache_read_tokens": _g("cache_read_input_tokens") or _g("cache_read_tokens"),
        "cache_write_tokens": _g("cache_creation_input_tokens") or _g("cache_write_tokens"),
    }


def _stream_event_text(event: Any) -> str:
    """Pull the text fragment out of an Anthropic streaming event.

    The shape mirrors the Anthropic Messages streaming spec. We only forward
    text from ``content_block_delta`` (type ``text_delta``) and the initial
    ``content_block_start`` (when the block already carries seed text).
    Tool-use blocks, thinking, and message-level deltas have no user-visible
    text and are skipped.
    """

    if not isinstance(event, dict):
        return ""
    etype = event.get("type")
    if etype == "content_block_delta":
        delta = event.get("delta") or {}
        if isinstance(delta, dict) and delta.get("type") == "text_delta":
            text = delta.get("text")
            return text if isinstance(text, str) else ""
        return ""
    if etype == "content_block_start":
        block = event.get("content_block") or {}
        if isinstance(block, dict) and block.get("type") == "text":
            text = block.get("text")
            return text if isinstance(text, str) else ""
    return ""


def _coerce_assistant_text(content: Any) -> str:
    if content is None:
        return ""
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for block in content:
            if isinstance(block, dict):
                if block.get("type") == "text" and isinstance(block.get("text"), str):
                    parts.append(block["text"])
            else:
                text = getattr(block, "text", None)
                if isinstance(text, str):
                    parts.append(text)
        return "".join(parts)
    return ""


def _to_jsonable(value: Any) -> Any:
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    if isinstance(value, dict):
        return {str(k): _to_jsonable(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_to_jsonable(v) for v in value]
    return repr(value)


def read_snapshot_jsonl(volume_root: str, sdk_session_id: Optional[str]) -> list:
    """Read the SDK-owned conversation history for ``runtime.snapshot``.

    Returns the raw JSONL records (one dict per line). agentd treats this
    payload as opaque; the browser/CLI render it.
    """

    if not sdk_session_id:
        return []
    path = os.path.join(volume_root, ".claude", "projects", "-work", f"{sdk_session_id}.jsonl")
    if not os.path.exists(path):
        return []
    out: list = []
    with open(path, "r", encoding="utf-8") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(__import__("json").loads(line))
            except ValueError:
                continue
    return out
