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
import os
import subprocess
import tempfile
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
    fields make sense for the Codex CLI. ``system_prompt``, when set, is
    written to a temp file at driver init and referenced via the
    ``model_instructions_file`` config override (the bare ``instructions``
    key is reserved per the 0.130.0 config reference).
    """

    model: str
    cwd: str = "/work"
    # The outer Docker profile (CapDrop ALL + no-new-privileges + ReadOnlyRootFS
    # in internal/sm/manager.go) blocks the unshare(CLONE_NEWUSER) that codex's
    # bwrap-based inner sandbox needs on Linux, so the inner layer runs wide-open
    # and the container is the trust boundary. Without this, every tool call
    # aborts with "bwrap: No permissions to create a new namespace" before the
    # wrapped command runs. ADR-0020 §"Items to verify" pre-authorized the
    # fallback.
    sandbox: str = "danger-full-access"
    approval_mode: str = "never"
    resume: Optional[str] = None
    system_prompt: Optional[str] = None
    codex_bin: str = CODEX_BIN_DEFAULT
    # Extra flags appended to every ``codex exec`` invocation. Empty in
    # production; tests use this to point at a fixture shim.
    extra_args: list[str] = field(default_factory=list)
    # MCP server descriptors in the agentd.greet wire shape — a list of
    # ``{name, url, transport, kind, headers?}`` dicts. The Codex CLI does
    # not accept the Claude SDK's name-keyed mcp_servers map, so we
    # translate to ``-c mcp_servers.<name>.<key>=<value>`` overrides at
    # argv-build time. HTTP/SSE entries additionally require codex's
    # ``experimental_use_rmcp_client`` flag.
    mcp_servers: list = field(default_factory=list)


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
        # silently-broken resume doesn't go unnoticed. Subsequent turns
        # stay quiet to avoid log flooding when the upstream JSONL schema
        # genuinely doesn't expose one.
        self._sid_warned = False
        # System prompt file: codex 0.130.0's `instructions` config key is
        # reserved; `model_instructions_file` is the documented path. Write
        # once at init and reference the stable file on every turn.
        self._instructions_file: Optional[str] = None
        if cfg.system_prompt:
            try:
                fd, path = tempfile.mkstemp(
                    prefix="agentctl-codex-instr-", suffix=".md",
                )
                with os.fdopen(fd, "w", encoding="utf-8") as f:
                    f.write(cfg.system_prompt)
                self._instructions_file = path
            except OSError as exc:
                _log.warning(
                    "codex_driver: failed to materialize system_prompt file "
                    "(running without instructions): %s", exc,
                )

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
        if self._instructions_file:
            try:
                os.unlink(self._instructions_file)
            except OSError:
                pass
            self._instructions_file = None

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
        # codex-cli 0.130.0 argv shape (verified against the pinned CLI
        # and developers.openai.com/codex/cli/reference):
        #   * `--ask-for-approval` is a *global* flag — must precede the
        #     subcommand. Putting it after `exec` errors with
        #     "unexpected argument".
        #   * Resume uses the `exec resume <SID>` subcommand; there is no
        #     top-level `--resume` flag. `exec resume` inherits the
        #     recorded session's `--sandbox` / `--cd` so we don't repeat
        #     them on resumed turns.
        #   * The bare `instructions` config key is reserved; the
        #     documented path for a caller-supplied system prompt is
        #     `model_instructions_file` pointing at a file on disk.
        argv: list[str] = [
            self._cfg.codex_bin,
            "--ask-for-approval",
            self._cfg.approval_mode,
            "exec",
        ]
        if sdk_session_id:
            argv.extend(["resume", sdk_session_id])
        argv.extend([
            "--json",
            "--skip-git-repo-check",
            "--model",
            model,
        ])
        if not sdk_session_id:
            argv.extend([
                "--sandbox",
                self._cfg.sandbox,
                "--cd",
                self._cfg.cwd,
            ])
        if self._instructions_file:
            argv.extend([
                "-c",
                f'model_instructions_file="{self._instructions_file}"',
            ])
        for override in _render_codex_mcp_overrides(self._cfg.mcp_servers):
            argv.extend(["-c", override])
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
# codex-cli 0.130.0 JSONL schema (verified against
# codex-rs/exec/src/exec_events.rs in the openai/codex repo). The
# ThreadEvent enum is tagged on ``type``; ThreadItem carries an outer
# ``id`` plus a flattened ``type`` discriminator for the inner item kind.
#
#   {"type": "thread.started", "thread_id": "<uuid>"}
#   {"type": "turn.started"}
#   {"type": "item.started", "item": {"id": "...", "type": "<kind>", ...}}
#   {"type": "item.updated", "item": {...}}
#   {"type": "item.completed", "item": {...}}
#   {"type": "turn.completed", "usage": {
#       "input_tokens": N, "cached_input_tokens": K,
#       "output_tokens": M, "reasoning_output_tokens": R
#   }}
#   {"type": "turn.failed", "error": {"message": "..."}}
#   {"type": "error", "message": "..."}
#
# Tool-like items emit started → completed; AgentMessage / Reasoning /
# Error emit completed only. The tool-like item shapes are:
#
#   agent_message:    {"text": "..."}
#   reasoning:        {"text": "..."}
#   command_execution:{"command": "ls", "aggregated_output": "...",
#                      "exit_code": 0, "status": "completed"}
#   file_change:      {"changes": [{"path": "foo", "kind": "add"}],
#                      "status": "completed"}
#   mcp_tool_call:    {"server": "...", "tool": "...", "arguments": {...},
#                      "result": {"content": [...]}|null,
#                      "error": {"message": "..."}|null,
#                      "status": "completed"}
#   web_search:       {"query": "...", "action": {"type": "search", ...}}
#   todo_list:        {"items": [{"text": "...", "completed": false}, ...]}
#
# Legacy shapes still accepted for back-compat with older CLI revisions
# that emitted OpenAI-responses-style frames:
#
#   {"type": "session.created", "session_id": "..."}
#   {"type": "item.delta", "delta": {"text": "..."}}
#   {"type": "item.completed", "item": {"type": "function_call",
#                                       "name": "...", "arguments": "..."}}
#   {"type": "item.completed", "item": {"type": "function_call_output",
#                                       "call_id": "...", "output": "..."}}


def _extract_codex_session_id(event: dict) -> Optional[str]:
    """Pull the Codex session id out of any frame that carries it.

    codex-cli 0.130.0 emits the id once at turn start as
    ``{"type":"thread.started","thread_id":"<uuid>"}``. Older / future
    revisions have used ``session.created`` / ``session_id`` shapes;
    accept all of them so a CLI revision flip doesn't silently break
    resume.
    """

    if event.get("type") == "thread.started":
        tid = event.get("thread_id")
        if isinstance(tid, str) and tid:
            return tid
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


# Item types that map to the shared tool.call / tool.result vocabulary.
# Anything in this set gets a tool.call on item.started and a tool.result
# on item.completed so the web/CLI renders a collapsed tool card. Items
# outside the set (agent_message, reasoning) take their own paths.
_TOOL_ITEM_TYPES = frozenset({
    "command_execution",
    "file_change",
    "mcp_tool_call",
    "web_search",
    "todo_list",
    "collab_tool_call",
})


def translate_codex_event(event: dict, *, turn_id: str) -> list[tuple[str, dict]]:
    """Map a single Codex JSONL event into shared-vocabulary tuples.

    Unknown event types are silently dropped (per the same forward-compat
    rule the Claude translator follows). Each tuple is
    ``(event_kind, data)`` ready for the wire.
    """

    out: list[tuple[str, dict]] = []
    etype = event.get("type") or ""

    # Legacy responses-API streaming shape — codex-cli 0.130.0 doesn't
    # emit this on plain turns, but older revisions did and a future one
    # may bring it back. Forward whatever text we can extract.
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

    if etype == "item.started":
        item = event.get("item") or {}
        if not isinstance(item, dict):
            return out
        itype = item.get("type") or ""
        # Tool-like items (command_execution, file_change, …) get a
        # tool.call here so the web card opens in the "running" state;
        # item.completed below fills it in with status + output.
        if itype in _TOOL_ITEM_TYPES:
            call = _tool_call_from_codex_item(item, itype, turn_id)
            if call is not None:
                out.append(call)
        # Legacy back-compat: older codex revisions emitted function_call
        # at item.completed time only, so the started branch here is a
        # no-op for them. Nothing else to do.
        return out

    if etype == "item.completed":
        item = event.get("item") or {}
        if not isinstance(item, dict):
            return out
        itype = item.get("type") or ""

        # Assistant message shape: codex-cli 0.130.0 uses item type
        # ``agent_message`` with text on a top-level ``text`` field
        # (``_coerce_codex_text`` already prefers that). Older shapes
        # (``message`` / ``assistant_message`` / ``output_message`` with
        # a ``content[]`` block list) are kept for forward/back compat.
        if itype in ("agent_message", "message", "assistant_message", "output_message"):
            text = _coerce_codex_text(item)
            if text:
                out.append((EVENT_ASSISTANT_MESSAGE, {
                    "turn_id": turn_id,
                    "content": text,
                }))
            return out

        # Reasoning items are the model's internal chain — codex emits
        # them only on completion. The shared vocabulary has no
        # thinking-block event today, so drop them for now (the JSONL
        # mirror still preserves the raw record for replay).
        if itype == "reasoning":
            return out

        # Tool-like items (codex-cli 0.130.0): emit tool.result paired
        # back to the tool.call we emitted at item.started time. The
        # web's conversation reducer keys on tool_use_id == item.id and
        # folds the result into the pending row.
        if itype in _TOOL_ITEM_TYPES:
            result = _tool_result_from_codex_item(item, itype, turn_id)
            if result is not None:
                out.append(result)
            return out

        # Legacy responses-API tool shape: older codex revisions packed
        # tool calls as ``function_call`` items with a string-encoded
        # ``arguments`` blob, and results as separate
        # ``function_call_output`` items keyed by ``call_id``. Keep
        # accepting these so a CLI rollback doesn't blank out tool cards.
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

    if etype in ("error", "turn.failed"):
        # codex-cli 0.130.0 emits turn.failed with {"error": {"message":
        # "..."}} on a model/CLI error; older revisions used a top-level
        # ``error`` frame. Surface both as a failed turn.end so agentd
        # settles the in-flight state. The driver caller logs the
        # underlying stderr separately.
        err = event.get("error")
        if isinstance(err, dict):
            msg = err.get("message") or err.get("error") or ""
        else:
            msg = err or event.get("message") or ""
        out.append((EVENT_TURN_END, {
            "turn_id": turn_id,
            "ok": False,
            "error": str(msg or "codex error"),
        }))
        return out

    return out


def _tool_call_from_codex_item(
    item: dict, itype: str, turn_id: str,
) -> Optional[tuple[str, dict]]:
    """Build a ``tool.call`` event from a codex-cli 0.130.0 tool-like item.

    Returns ``None`` if the item has no id (the conversation reducer
    keys results back to calls by ``tool_use_id``, so a missing id would
    leave the row orphaned). Tool names match the Claude SDK's vocabulary
    where the input shape lines up (``Bash`` for shells, ``Edit``/``Write``
    for file patches, ``WebSearch``/``TodoWrite`` for the obvious ones)
    so the existing web ``formatToolHeader`` renders nice verbs/icons
    instead of falling back to the raw item type.
    """

    item_id = str(item.get("id") or "")
    if not item_id:
        return None
    name, tinput = _codex_tool_name_and_input(item, itype)
    return (EVENT_TOOL_CALL, {
        "turn_id": turn_id,
        "tool_use_id": item_id,
        "name": name,
        "input": tinput,
    })


def _tool_result_from_codex_item(
    item: dict, itype: str, turn_id: str,
) -> Optional[tuple[str, dict]]:
    """Build a ``tool.result`` event paired with the call's item id."""

    item_id = str(item.get("id") or "")
    if not item_id:
        return None
    content, is_error = _codex_tool_output(item, itype)
    return (EVENT_TOOL_RESULT, {
        "turn_id": turn_id,
        "tool_use_id": item_id,
        "content": content,
        "is_error": is_error,
    })


def _codex_tool_name_and_input(item: dict, itype: str) -> tuple[str, dict]:
    """Map a codex item to a (tool_name, input_dict) pair the web renders.

    The names mirror Claude SDK tool names where the shape matches so
    ``formatToolHeader`` picks the right verb/icon (``Bash`` → ⌘ "Ran …",
    ``Edit`` → ✎ "Edited …"). Items without a Claude analog fall back to
    a readable label like ``ApplyPatch`` and use the default renderer.
    """

    if itype == "command_execution":
        cmd = item.get("command")
        return ("Bash", {
            "command": cmd if isinstance(cmd, str) else "",
        })

    if itype == "file_change":
        changes = item.get("changes")
        if not isinstance(changes, list):
            changes = []
        # Single-file patches map onto the Edit/Write shape so the web
        # shows "Edited foo.py" / "Wrote new.py" instead of a generic
        # tool card. Multi-file patches keep the changes list intact
        # under a generic ``ApplyPatch`` name (no special formatter).
        if len(changes) == 1 and isinstance(changes[0], dict):
            path = str(changes[0].get("path") or "")
            kind = str(changes[0].get("kind") or "").lower()
            if kind == "add":
                return ("Write", {"file_path": path})
            if kind == "delete":
                return ("Delete", {"file_path": path})
            # Default (update / unknown): Edit.
            return ("Edit", {"file_path": path})
        # Multi-file: surface the full list under input.changes so a
        # power user can inspect the raw patch in the "Input" panel.
        return ("ApplyPatch", {"changes": [c for c in changes if isinstance(c, dict)]})

    if itype == "mcp_tool_call":
        server = str(item.get("server") or "")
        tool = str(item.get("tool") or "")
        args = item.get("arguments")
        if not isinstance(args, dict):
            args = {} if args is None else {"_raw": args}
        # The web's ``MCP_RE`` matches the Claude SDK convention
        # ``mcp__<server>__<tool>``; emit the same shape so MCP tools
        # render with the MCP badge and the server's health pill.
        full_name = f"mcp__{server}__{tool}" if server and tool else (tool or "mcp_tool")
        return (full_name, args)

    if itype == "web_search":
        query = item.get("query")
        action = item.get("action")
        tinput: dict = {}
        if isinstance(query, str) and query:
            tinput["query"] = query
        if isinstance(action, dict):
            tinput["action"] = action
        return ("WebSearch", tinput)

    if itype == "todo_list":
        items = item.get("items")
        if not isinstance(items, list):
            items = []
        return ("TodoWrite", {"todos": items})

    if itype == "collab_tool_call":
        # Multi-agent coordination — keep the raw shape under a clear
        # name so the web shows it as a tool card without trying to
        # format it.
        return ("CollabTool", {k: v for k, v in item.items() if k not in ("id", "type")})

    # Defensive fallback for any new tool-like item type we forgot to
    # special-case: keep the original item type as the tool name and
    # forward all non-meta fields so the user still sees the call.
    return (itype or "tool", {
        k: v for k, v in item.items() if k not in ("id", "type", "status")
    })


def _codex_tool_output(item: dict, itype: str) -> tuple[str, bool]:
    """Build the (output_text, is_error) pair for a completed item.

    ``status`` is the cross-item-type signal codex uses for success vs
    failure ("completed" / "failed" / "declined"); we surface the body
    in a per-type-aware way (aggregated stdout for shells, change list
    for patches, structured/text content for MCP).
    """

    status = str(item.get("status") or "").lower()
    is_error = status in ("failed", "declined")

    if itype == "command_execution":
        out_str = item.get("aggregated_output")
        text = out_str if isinstance(out_str, str) else ""
        exit_code = item.get("exit_code")
        # On declined/failed runs with no stdout, give the user a hint
        # rather than an empty card.
        if not text and is_error:
            if status == "declined":
                text = "(command declined by sandbox)"
            elif isinstance(exit_code, int) and exit_code != 0:
                text = f"(exit code {exit_code})"
            else:
                text = "(command failed)"
        return (text, is_error)

    if itype == "file_change":
        changes = item.get("changes")
        if not isinstance(changes, list):
            changes = []
        # Human-readable summary; the structured input still carries the
        # full change list for any consumer that wants it.
        parts: list[str] = []
        for c in changes:
            if not isinstance(c, dict):
                continue
            path = str(c.get("path") or "")
            kind = str(c.get("kind") or "")
            if path or kind:
                parts.append(f"{kind}: {path}".strip(": ").strip())
        body = "\n".join(parts)
        if not body and is_error:
            body = f"(patch {status})"
        return (body, is_error)

    if itype == "mcp_tool_call":
        # mcp errors come on a dedicated ``error`` field; result has a
        # ``content`` array of JSON values plus optional
        # ``structured_content``. Prefer the text body, fall through to
        # JSON-stringified blocks when the content isn't plain text.
        err = item.get("error")
        if isinstance(err, dict) and err.get("message"):
            return (str(err.get("message")), True)
        result = item.get("result")
        if isinstance(result, dict):
            content = result.get("content")
            if isinstance(content, list):
                text_parts: list[str] = []
                for block in content:
                    if isinstance(block, dict) and isinstance(block.get("text"), str):
                        text_parts.append(block["text"])
                    else:
                        text_parts.append(json.dumps(_to_jsonable(block), sort_keys=True))
                joined = "\n".join(p for p in text_parts if p)
                if joined:
                    return (joined, is_error)
            structured = result.get("structured_content")
            if structured is not None:
                return (json.dumps(_to_jsonable(structured), sort_keys=True), is_error)
        return ("", is_error)

    if itype == "web_search":
        # Codex doesn't ship search hits in the item itself (the model
        # consumes them internally); close the card cleanly with an
        # empty body unless the run was declined/failed.
        if is_error:
            return (f"(web_search {status})", True)
        return ("", False)

    if itype == "todo_list":
        items = item.get("items")
        if not isinstance(items, list):
            return ("", is_error)
        lines: list[str] = []
        for t in items:
            if not isinstance(t, dict):
                continue
            mark = "x" if t.get("completed") else " "
            text = str(t.get("text") or "")
            lines.append(f"- [{mark}] {text}")
        return ("\n".join(lines), is_error)

    # Fallback: anything else the JSONL surfaces — emit an empty body
    # and trust ``status`` for the error chip.
    return ("", is_error)


def _to_jsonable(value: Any) -> Any:
    if isinstance(value, (str, int, float, bool)) or value is None:
        return value
    if isinstance(value, dict):
        return {str(k): _to_jsonable(v) for k, v in value.items()}
    if isinstance(value, (list, tuple)):
        return [_to_jsonable(v) for v in value]
    return repr(value)


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


_TOML_KEY_OK = set(
    "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-"
)


def _is_bare_toml_key(name: str) -> bool:
    return bool(name) and all(ch in _TOML_KEY_OK for ch in name)


def _toml_quote(s: str) -> str:
    """Quote ``s`` as a TOML basic string for use inside a ``-c key="..."``
    override. Escapes backslashes and double quotes; rejects control chars
    by stripping them (codex parses each ``-c`` value as a TOML expression,
    so we keep the value safe for that parser).
    """

    cleaned = "".join(ch for ch in s if ch >= " " or ch == "\t")
    return '"' + cleaned.replace("\\", "\\\\").replace('"', '\\"') + '"'


def _render_codex_mcp_overrides(mcps: Any) -> list[str]:
    """Translate the agentd.greet ``mcps`` list into Codex CLI ``-c`` overrides.

    Each HTTP/SSE entry becomes a pair of ``mcp_servers.<name>.url`` and
    (optionally) ``mcp_servers.<name>.bearer_token`` overrides; when any
    are emitted, codex's ``experimental_use_rmcp_client`` flag is set so
    the rmcp client handles the URL-based transport (the legacy mcp client
    only knows stdio). Entries with an unsafe TOML key, unsupported
    transport, or no URL are silently skipped — the registry-side render
    already emitted any user-facing skip events.
    """

    if not mcps or not isinstance(mcps, list):
        return []
    overrides: list[str] = []
    saw_http = False
    for m in mcps:
        if not isinstance(m, dict):
            continue
        name = m.get("name")
        if not isinstance(name, str) or not _is_bare_toml_key(name):
            continue
        transport = (m.get("transport") or "http").lower()
        if transport not in ("http", "sse"):
            continue
        url = m.get("url") or ""
        if not isinstance(url, str) or not url:
            continue
        overrides.append(f"mcp_servers.{name}.url={_toml_quote(url)}")
        headers = m.get("headers")
        bearer = ""
        if isinstance(headers, dict):
            auth = headers.get("Authorization") or headers.get("authorization") or ""
            if isinstance(auth, str) and auth.lower().startswith("bearer "):
                bearer = auth[len("bearer "):].strip()
        if bearer:
            overrides.append(
                f"mcp_servers.{name}.bearer_token={_toml_quote(bearer)}"
            )
        saw_http = True
    if saw_http:
        # rmcp_client is required for url-based MCP servers in codex
        # 0.130.0; without it, only stdio entries are honored.
        overrides.insert(0, "experimental_use_rmcp_client=true")
    return overrides


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
