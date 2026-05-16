"""OpenAI Codex CLI integration.

Spawns one ``codex exec --json`` subprocess per turn (the ADR-0020 §7
approach — the experimental ``openai-codex`` Python SDK is deliberately
avoided in phase 1) and translates the JSONL stream into the shared
``runtime.event`` vocabulary defined in :mod:`shim.runtime.translate`.

Resume uses the Codex session id captured from the first turn's JSONL,
passed back to subsequent ``codex exec`` invocations as ``--resume <sid>``
and persisted by agentd in the provider-agnostic ``sessions.sdk_session_id``
column.

The Codex JSONL schema is the per-ADR "verify at impl" item — every
mapping below that depends on a guessed event shape is tagged
``TODO(verify-codex-jsonl)`` so they're grep-able once we can run the
pinned CLI version against a real turn.
"""

from __future__ import annotations

import json
import logging
import subprocess
import threading
from collections import deque
from dataclasses import dataclass, field
from typing import Any, Callable, IO, Optional

_log = logging.getLogger(__name__)

# Cap the stderr ring buffer at ~64 KiB worth of lines so a chatty Codex CLI
# (e.g. debug logging on a long-running turn) can't grow the process heap
# unboundedly. The buffer is only surfaced on a non-zero exit, so older
# lines are the safe ones to drop.
_STDERR_MAX_LINES = 1000

from .translate import (
    EVENT_ASSISTANT_DELTA,
    EVENT_ASSISTANT_MESSAGE,
    EVENT_TOOL_CALL,
    EVENT_TOOL_RESULT,
    EVENT_TURN_CANCELLED,
    EVENT_TURN_END,
    EVENT_USAGE,
)


EmitEvent = Callable[[str, dict], None]
EmitRecord = Callable[[dict], None]


# Default binary name. Overridable for tests via CodexConfig.codex_bin so a
# fixture can substitute a fake exec script.
CODEX_BIN_DEFAULT = "codex"


@dataclass
class CodexConfig:
    """Per-session config for the Codex driver.

    Mirrors :class:`shim.runtime.claude_driver.RuntimeConfig` where the
    fields make sense for the Codex CLI. ``system_prompt`` is forwarded
    via ``--instructions`` when set (TODO(verify-codex-jsonl): the exact
    flag name on the pinned CLI version — current docs reference
    ``--instructions`` / ``-i``, double-check before phase 1 ships).
    """

    model: str
    cwd: str = "/work"
    sandbox: str = "workspace-write"
    approval_mode: str = "never"
    resume: Optional[str] = None
    system_prompt: Optional[str] = None
    codex_bin: str = CODEX_BIN_DEFAULT
    # Extra flags appended to every ``codex exec`` invocation. Empty in
    # production; tests use this to point at a fixture shim.
    extra_args: list[str] = field(default_factory=list)


class CodexDriver:
    """Drive a Codex session by spawning ``codex exec --json`` per turn.

    Each turn:
      1. Build the command (``codex exec --json --model … --sandbox … …``).
      2. Spawn the subprocess, ``cwd=self._cfg.cwd``.
      3. Read stdout line-by-line, parse JSON, dispatch through
         :func:`translate_codex_event` into shared-vocabulary events.
      4. On first turn, capture the Codex session id and ship it back via
         ``emit_session_id`` so agentd persists it for ``--resume`` on
         later turns.
      5. ``turn.end`` on clean exit, ``turn.cancelled`` if interrupted.

    Concurrent ``submit_turn`` calls are serialized by ``_proc_lock`` — the
    shim's actor only sends one message at a time today, but the lock
    keeps the contract crisp.
    """

    def __init__(
        self,
        cfg: CodexConfig,
        emit_event: EmitEvent,
        emit_session_id: Callable[[str], None],
        emit_message_record: Optional[EmitRecord] = None,
    ) -> None:
        self._cfg = cfg
        self._emit = emit_event
        self._emit_session_id = emit_session_id
        self._emit_record = emit_message_record
        self._sdk_session_id: Optional[str] = cfg.resume
        # _state_lock guards _sdk_session_id (read in _build_argv, written
        # in _consume_stdout) and _cfg.model (read in _build_argv, written
        # in set_model). _proc_lock guards the subprocess handle; keeping
        # them separate avoids hold-while-spawn when the next turn races
        # with a still-finalizing interrupt.
        self._state_lock = threading.Lock()
        self._proc_lock = threading.Lock()
        self._proc: Optional[subprocess.Popen] = None
        self._turn_thread: Optional[threading.Thread] = None
        self._stopping = threading.Event()
        # First-encounter flag for the session-id extractor — drops one
        # warn-log if a turn completes with no recognizable session id, so
        # silently-broken --resume doesn't go unnoticed. Subsequent turns
        # stay quiet to avoid log flooding when the upstream JSONL schema
        # genuinely doesn't expose one.
        self._sid_warned = False

    # -- lifecycle ------------------------------------------------------

    def start(self) -> None:
        """No persistent client — the driver runs ``codex`` per turn.

        Provided so the dispatcher in ``__main__.py`` can treat every
        driver the same way (start → submit_turn* → shutdown).
        """

        # Nothing to do; intentional. Kept as a method so the shim's
        # driver-protocol call site stays uniform.
        return None

    def shutdown(self, grace_seconds: float = 30.0) -> None:
        self._stopping.set()
        self._kill_proc()
        t = self._turn_thread
        if t is not None:
            t.join(timeout=grace_seconds)

    # -- per-turn entry points -----------------------------------------

    def submit_turn(self, *, turn_id: str, content: str) -> None:
        """Run one turn in a background thread.

        The shim's main loop blocks on the control socket; we mirror the
        Claude driver's contract by returning immediately and letting the
        thread emit events asynchronously.
        """

        t = threading.Thread(
            target=self._run_turn,
            args=(turn_id, content),
            name=f"codex-turn-{turn_id or 'noid'}",
            daemon=True,
        )
        with self._proc_lock:
            self._turn_thread = t
        t.start()

    def interrupt(self) -> None:
        """Kill the in-flight ``codex exec`` subprocess.

        The waiting ``_run_turn`` will observe the non-zero exit and emit
        ``turn.cancelled`` instead of ``turn.end``.
        """

        self._kill_proc()

    def set_model(self, model: str) -> None:
        """Update the model the next ``codex exec`` will run under.

        Codex re-spawns per turn, so the switch is just a config bump —
        no client tear-down required (contrast with Claude's
        :meth:`RuntimeDriver.set_model`, which has to reconnect the SDK
        client to apply a new model). Empty / unchanged is a no-op so a
        repeated ``/model`` doesn't churn anything. ADR 0020 §4.3.
        """

        if not model:
            return
        with self._state_lock:
            if model == self._cfg.model:
                return
            self._cfg.model = model

    # -- internals ------------------------------------------------------

    def _kill_proc(self) -> None:
        with self._proc_lock:
            proc = self._proc
        if proc is None:
            return
        try:
            proc.terminate()
        except Exception:  # noqa: BLE001
            pass

    def _build_argv(self, prompt: str) -> list[str]:
        # Snapshot the mutable state once under the lock so the argv is
        # internally consistent even if set_model() or _consume_stdout()
        # fires while this turn is being assembled.
        with self._state_lock:
            model = self._cfg.model
            sdk_session_id = self._sdk_session_id
        argv: list[str] = [
            self._cfg.codex_bin,
            "exec",
            "--json",
            "--model",
            model,
            "--sandbox",
            self._cfg.sandbox,
            "--ask-for-approval",
            self._cfg.approval_mode,
            "--cd",
            self._cfg.cwd,
        ]
        if sdk_session_id:
            # TODO(verify-codex-jsonl): confirm ``--resume <id>`` is the
            # right flag on the pinned CLI version. The ADR specifies it
            # but the current upstream docs sometimes call it
            # ``--continue`` or ``--session``; pin once verified.
            argv.extend(["--resume", sdk_session_id])
        if self._cfg.system_prompt:
            # TODO(verify-codex-jsonl): the exact CLI flag for a
            # caller-supplied system prompt — current docs reference
            # ``--instructions`` / ``-i`` but this may change. The Claude
            # driver forwards ``RuntimeConfig.system_prompt`` for the
            # task-chat per-stage prompt; mirror that here.
            argv.extend(["--instructions", self._cfg.system_prompt])
        argv.extend(self._cfg.extra_args)
        argv.append(prompt)
        return argv

    def _spawn(self, argv: list[str]) -> subprocess.Popen:
        # Use line-buffered text mode so the JSONL stream surfaces as it
        # arrives instead of waiting for the child's stdio buffer to flush
        # at exit. ``encoding="utf-8"`` matches what the CLI emits.
        return subprocess.Popen(
            argv,
            cwd=self._cfg.cwd,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
            encoding="utf-8",
            bufsize=1,
        )

    def _run_turn(self, turn_id: str, content: str) -> None:
        argv = self._build_argv(content)
        try:
            proc = self._spawn(argv)
        except FileNotFoundError as exc:
            self._emit(EVENT_TURN_END, {
                "turn_id": turn_id,
                "ok": False,
                "error": f"codex binary not found: {exc}",
            })
            return
        except OSError as exc:
            self._emit(EVENT_TURN_END, {
                "turn_id": turn_id,
                "ok": False,
                "error": f"codex spawn failed: {exc}",
            })
            return

        with self._proc_lock:
            self._proc = proc

        # Bounded ring buffer — a noisy Codex CLI can fill stderr fast and
        # we only surface it on a non-zero exit, so dropping oldest is the
        # right policy. Cap matches _STDERR_MAX_LINES so the resident set
        # for stderr stays small even for long-running turns.
        stderr_buf: deque[str] = deque(maxlen=_STDERR_MAX_LINES)
        stderr_thread = threading.Thread(
            target=_drain,
            args=(proc.stderr, stderr_buf),
            name=f"codex-stderr-{turn_id or 'noid'}",
            daemon=True,
        )
        stderr_thread.start()

        try:
            self._consume_stdout(proc.stdout, turn_id=turn_id)
        finally:
            try:
                rc = proc.wait(timeout=5.0)
            except subprocess.TimeoutExpired:
                proc.kill()
                rc = proc.wait()
            stderr_thread.join(timeout=1.0)
            # Close the pipes explicitly so the test runner doesn't
            # surface ResourceWarnings on the unbuffered text wrappers
            # we opened above.
            for stream in (proc.stdout, proc.stderr):
                if stream is not None:
                    try:
                        stream.close()
                    except Exception:  # noqa: BLE001
                        pass
            with self._proc_lock:
                self._proc = None

        cancelled = self._stopping.is_set() or rc < 0 or (
            # POSIX convention: termination by signal SIGTERM (15) maps to
            # exit code 143 when the shell reports it; cover both shapes.
            rc in (143, 137)
        )
        if cancelled:
            self._emit(EVENT_TURN_CANCELLED, {"turn_id": turn_id})
            return

        if rc != 0:
            err = "".join(stderr_buf).strip() or f"codex exec exited {rc}"
            self._emit(EVENT_TURN_END, {
                "turn_id": turn_id,
                "ok": False,
                "error": err,
            })
            return

        self._emit(EVENT_TURN_END, {"turn_id": turn_id, "ok": True})

    def _consume_stdout(self, stdout: Optional[IO[str]], *, turn_id: str) -> None:
        if stdout is None:
            return
        # Track whether we ever saw a session id in this turn so we can
        # warn once if the JSONL shape doesn't surface one — silent
        # --resume breakage is the failure mode the warning catches.
        saw_session_id = False
        events_processed = 0
        for line in stdout:
            line = line.strip()
            if not line:
                continue
            try:
                event = json.loads(line)
            except ValueError:
                # Non-JSON lines (banner, debug, etc.) — skip; the Codex
                # CLI is allowed to interleave free-form text.
                continue
            if not isinstance(event, dict):
                continue

            events_processed += 1
            sid = _extract_codex_session_id(event)
            if sid:
                saw_session_id = True
                with self._state_lock:
                    changed = sid != self._sdk_session_id
                    if changed:
                        self._sdk_session_id = sid
                if changed:
                    self._emit_session_id(sid)

            for kind, data in translate_codex_event(event, turn_id=turn_id):
                self._emit(kind, data)

            if self._emit_record is not None:
                # agentd's history mirror is provider-agnostic: it
                # stores the raw record verbatim, keyed by whatever the
                # provider considers unique. For Codex we hand it the
                # JSON object as-is. Swallowing the exception keeps a
                # single bad frame from killing the turn, but log it so
                # repeated failures don't go silent.
                try:
                    self._emit_record(event)
                except Exception as exc:  # noqa: BLE001
                    _log.warning(
                        "codex_driver: emit_record failed (turn_id=%s): %s",
                        turn_id, exc,
                    )

        # End-of-turn session-id sanity check: if events flowed but none
        # carried a session id, _build_argv won't pass --resume on the
        # next turn and the conversation forks silently. Warn once per
        # driver instance — the JSONL schema may legitimately omit the
        # field in some configurations, but the user deserves to know.
        if events_processed > 0 and not saw_session_id and not self._sid_warned:
            with self._state_lock:
                already_have_sid = self._sdk_session_id is not None
            if not already_have_sid:
                self._sid_warned = True
                _log.warning(
                    "codex_driver: turn %s emitted %d events but no recognizable "
                    "session id — --resume will not carry over (verify "
                    "TODO(verify-codex-jsonl) mappings against the pinned CLI)",
                    turn_id, events_processed,
                )


# ---------------------------------------------------------------------------
# Codex JSONL → internal-vocabulary translation
# ---------------------------------------------------------------------------
#
# TODO(verify-codex-jsonl): every shape below is the project's best-effort
# read of ``codex exec --json`` as of the ADR. The CLI's JSONL is
# documented at developers.openai.com/codex/cli/reference, but the schema
# has shifted between versions; pin the CLI version (ADR-0020 §6) and
# re-verify these mappings against a live run before phase 1 ships.
#
# The shapes assumed here:
#
#   {"type": "session.created", "session_id": "..."}
#   {"type": "item.started",   "item": {"id": "...", "type": "message", ...}}
#   {"type": "item.delta",     "item_id": "...", "delta": {"text": "..."}}
#   {"type": "item.completed", "item": {
#       "id": "...", "type": "message", "content":
#           [{"type": "output_text", "text": "..."}]
#   }}
#   {"type": "item.completed", "item": {
#       "id": "...", "type": "function_call",
#       "name": "shell", "arguments": "{\"cmd\":\"ls\"}"
#   }}
#   {"type": "item.completed", "item": {
#       "id": "...", "type": "function_call_output",
#       "call_id": "...", "output": "...", "is_error": false
#   }}
#   {"type": "turn.completed", "usage": {
#       "input_tokens": N, "output_tokens": M,
#       "cached_input_tokens": K
#   }, "model": "gpt-5.5"}
#   {"type": "error", "message": "..."}


def _extract_codex_session_id(event: dict) -> Optional[str]:
    """Pull the Codex session id out of any frame that carries it.

    Multiple frame shapes are known to surface this — the explicit
    ``session.created`` event on the first turn, and an inline
    ``session_id`` field on later events. Try both so the caller doesn't
    have to special-case.
    """

    # TODO(verify-codex-jsonl): confirm the canonical field name. Past
    # versions have used ``session_id``, ``id``, and ``conversation_id``
    # in different frames.
    sid = event.get("session_id")
    if isinstance(sid, str) and sid:
        return sid
    if event.get("type") == "session.created":
        inner = event.get("session") or {}
        if isinstance(inner, dict):
            sid = inner.get("id") or inner.get("session_id")
            if isinstance(sid, str) and sid:
                return sid
    return None


def translate_codex_event(event: dict, *, turn_id: str) -> list[tuple[str, dict]]:
    """Map a single Codex JSONL event into shared-vocabulary tuples.

    Unknown event types are silently dropped (per the same forward-compat
    rule the Claude translator follows). Each tuple is
    ``(event_kind, data)`` ready for the wire.
    """

    out: list[tuple[str, dict]] = []
    etype = event.get("type") or ""

    # TODO(verify-codex-jsonl): the ``item.delta`` shape with a nested
    # ``delta.text`` mirrors the OpenAI responses-API streaming format,
    # which the Codex CLI is reported to forward; verify on the pinned
    # version.
    if etype == "item.delta":
        delta = event.get("delta") or {}
        text = ""
        if isinstance(delta, dict):
            text = (delta.get("text") or delta.get("output_text") or "")
        elif isinstance(delta, str):
            text = delta
        if text:
            out.append((EVENT_ASSISTANT_DELTA, {"turn_id": turn_id, "delta": text}))
        return out

    if etype == "item.completed":
        item = event.get("item") or {}
        if not isinstance(item, dict):
            return out
        itype = item.get("type") or ""

        # TODO(verify-codex-jsonl): assistant message shape. We expect
        # either ``content`` (list of typed blocks) or a flat ``text``
        # field; handle both defensively.
        if itype in ("message", "assistant_message", "output_message"):
            text = _coerce_codex_text(item)
            if text:
                out.append((EVENT_ASSISTANT_MESSAGE, {
                    "turn_id": turn_id,
                    "content": text,
                }))
            return out

        # TODO(verify-codex-jsonl): tool call shape. Codex uses
        # OpenAI-function-style ``function_call`` items with the arg
        # blob serialized as a string under ``arguments``; decode it
        # before forwarding so the renderer can introspect it.
        if itype in ("function_call", "tool_call"):
            args_raw = item.get("arguments")
            if isinstance(args_raw, str):
                try:
                    args = json.loads(args_raw)
                except ValueError:
                    args = {"_raw": args_raw}
            elif isinstance(args_raw, dict):
                args = args_raw
            else:
                args = {}
            out.append((EVENT_TOOL_CALL, {
                "turn_id": turn_id,
                "tool_use_id": str(item.get("id") or item.get("call_id") or ""),
                "name": str(item.get("name") or ""),
                "input": args,
            }))
            return out

        # TODO(verify-codex-jsonl): tool-result shape. Codex pairs
        # ``function_call_output`` items with the originating call's
        # ``call_id``; the result text is under ``output``.
        if itype in ("function_call_output", "tool_call_output", "tool_result"):
            content = item.get("output")
            if content is None:
                content = item.get("content") or ""
            out.append((EVENT_TOOL_RESULT, {
                "turn_id": turn_id,
                "tool_use_id": str(item.get("call_id") or item.get("tool_use_id") or ""),
                "content": content,
                "is_error": bool(item.get("is_error", False)),
            }))
            return out

        # Unknown item types — let the JSONL mirror still pick them up
        # but don't fabricate vocabulary events for them.
        return out

    if etype == "turn.completed":
        usage = event.get("usage")
        if isinstance(usage, dict):
            out.append((EVENT_USAGE, {
                "turn_id": turn_id,
                "model": str(event.get("model") or ""),
                **_codex_usage_dict(usage),
            }))
        return out

    if etype == "error":
        # TODO(verify-codex-jsonl): error frames don't have a documented
        # canonical shape yet; surface the message as a failed turn.end
        # so agentd settles the in-flight state. The driver caller logs
        # the underlying stderr separately.
        out.append((EVENT_TURN_END, {
            "turn_id": turn_id,
            "ok": False,
            "error": str(event.get("message") or event.get("error") or "codex error"),
        }))
        return out

    return out


def _coerce_codex_text(item: dict) -> str:
    """Pull assistant text out of a completed Codex message item."""

    text = item.get("text")
    if isinstance(text, str) and text:
        return text
    content = item.get("content")
    if isinstance(content, str):
        return content
    if isinstance(content, list):
        parts: list[str] = []
        for block in content:
            if not isinstance(block, dict):
                continue
            # TODO(verify-codex-jsonl): block type names. OpenAI's
            # responses API uses ``output_text``; older Codex revisions
            # used ``text``. Accept either.
            btype = block.get("type") or ""
            if btype in ("output_text", "text"):
                t = block.get("text")
                if isinstance(t, str):
                    parts.append(t)
        return "".join(parts)
    return ""


def _codex_usage_dict(usage: dict) -> dict:
    """Normalize a Codex usage block into the shared usage event fields.

    TODO(verify-codex-jsonl): exact field names. OpenAI's responses API
    uses ``input_tokens`` / ``output_tokens`` / ``cached_input_tokens``;
    older Codex builds used ``prompt_tokens`` / ``completion_tokens``.
    Map both so a CLI revision flip doesn't zero our cost rows.
    """

    def _g(*names: str) -> int:
        for name in names:
            v = usage.get(name)
            if v is None:
                continue
            try:
                return int(v)
            except (TypeError, ValueError):
                continue
        return 0

    return {
        "input_tokens": _g("input_tokens", "prompt_tokens"),
        "output_tokens": _g("output_tokens", "completion_tokens"),
        "cache_read_tokens": _g("cached_input_tokens", "cache_read_input_tokens"),
        # Codex doesn't expose a cache-write counter today; leave at 0 so
        # the cost row arithmetic still lines up.
        "cache_write_tokens": 0,
    }


def _drain(stream: Optional[IO[str]], sink: "deque[str]") -> None:
    """Read lines off ``stream`` into ``sink``. ``sink`` is a bounded
    deque (maxlen=_STDERR_MAX_LINES) so a long-running noisy turn drops
    oldest lines instead of growing memory unboundedly. Only surfaced on
    a non-zero exit, so dropping the head is the right policy.
    """

    if stream is None:
        return
    try:
        for line in stream:
            sink.append(line)
    except Exception:  # noqa: BLE001
        pass
