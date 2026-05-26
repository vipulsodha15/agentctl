import { useEffect, useRef, useState } from "react";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  InterruptResponse,
  SendMessageRequest,
  SendMessageResponse,
} from "../types";
import { SkillAutocomplete, getSkillNav } from "./SkillAutocomplete";

interface Props {
  sessionId: string;
  inFlight: boolean;
  queueDepth: number;
  // ADR 0020 §2 / §UX principles — `/model <name>` is the keyboard-driven
  // secondary surface for mid-session model switch. Owner of the actual
  // PATCH lives one component up (SessionDetail) so the dropdown and the
  // slash command share the same code path; this component only handles
  // detection + fuzzy resolution.
  providerModels?: string[];
  currentModel?: string;
  onModelSwitch?: (next: string) => Promise<void> | void;
  // Optimistic local cancellation hook. Called synchronously the moment
  // Stop is pressed, before the /interrupt POST round-trips, so the
  // reducer can drop late streaming frames and clear the "Responding"
  // pill immediately rather than waiting for turn.cancelled to land.
  onStopRequested?: () => void;
}

export function MessageInput({
  sessionId,
  inFlight,
  queueDepth,
  providerModels,
  currentModel,
  onModelSwitch,
  onStopRequested,
}: Props) {
  const [value, setValue] = useState("");
  const [sending, setSending] = useState(false);
  const [stopping, setStopping] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);

  useEffect(() => {
    taRef.current?.focus();
  }, [sessionId]);

  async function send() {
    const content = value.trim();
    if (!content) return;
    if (sending) return;
    // `/model <name>` interceptor (ADR 0020 §2 secondary UX). Resolved
    // via fuzzy match against the provider's catalog: `/model opus`
    // → `claude-opus-4-7` if exactly one model contains "opus". On
    // ambiguity or no match we fall through to sending the literal
    // text so the user gets a chat reply explaining what happened
    // — better than silently swallowing the command.
    const modelArg = parseSlashModel(content);
    if (modelArg !== null && onModelSwitch) {
      const resolved = resolveModelName(modelArg, providerModels || []);
      if (resolved === MODEL_AMBIGUOUS) {
        setError(`/model: "${modelArg}" matches multiple models — be more specific`);
        return;
      }
      if (resolved === MODEL_UNKNOWN) {
        setError(`/model: no model matches "${modelArg}"`);
        return;
      }
      if (resolved === currentModel) {
        setError(`/model: already on ${resolved}`);
        return;
      }
      setSending(true);
      setError(null);
      try {
        await onModelSwitch(resolved);
        setValue("");
      } catch (err) {
        setError(
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err),
        );
      } finally {
        setSending(false);
        queueMicrotask(() => taRef.current?.focus());
      }
      return;
    }
    setSending(true);
    setError(null);
    try {
      const req: SendMessageRequest = {
        content,
        client_id: clientId(),
        idempotency_key: cryptoRandomId(),
      };
      await apiJson<SendMessageResponse>(
        `/v1/sessions/${encodeURIComponent(sessionId)}/messages`,
        { method: "POST", ...jsonBody(req) },
      );
      setValue("");
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSending(false);
      queueMicrotask(() => taRef.current?.focus());
    }
  }

  async function stopTurn() {
    if (stopping) return;
    setStopping(true);
    setError(null);
    onStopRequested?.();
    try {
      await apiJson<InterruptResponse>(
        `/v1/sessions/${encodeURIComponent(sessionId)}/interrupt`,
        { method: "POST", ...jsonBody({ clear_queue: false }) },
      );
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setStopping(false);
    }
  }

  function onPickSkill(name: string) {
    const rest = value.includes(" ") ? value.slice(value.indexOf(" ")) : "";
    const next = `/${name} ${rest.trimStart()}`.replace(/\s+$/, " ");
    setValue(next);
    queueMicrotask(() => taRef.current?.focus());
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    const nav = getSkillNav();
    if (nav && nav.hasItems) {
      if (e.key === "ArrowDown") {
        e.preventDefault();
        nav.next();
        return;
      }
      if (e.key === "ArrowUp") {
        e.preventDefault();
        nav.prev();
        return;
      }
      if (e.key === "Tab") {
        e.preventDefault();
        nav.pick();
        return;
      }
      if (e.key === "Enter" && !e.shiftKey) {
        e.preventDefault();
        nav.pick();
        return;
      }
    }
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      void send();
    }
  }

  const sendDisabled = sending || inFlight || value.trim() === "";

  return (
    <div className="input-area">
      <SkillAutocomplete
        sessionId={sessionId}
        value={value}
        onPick={onPickSkill}
      />
      <div className="input-shell">
        <textarea
          ref={taRef}
          value={value}
          onChange={(e) => setValue(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={
            inFlight
              ? "Type to queue — will send when the current turn ends…"
              : "Message the agent. Enter to send, Shift+Enter for newline. Press / for skills."
          }
        />
        <div className="controls">
          <span className="hint">
            <kbd>⏎</kbd> send · <kbd>⇧ ⏎</kbd> newline · <kbd>/</kbd> skills
          </span>
          {queueDepth > 0 && (
            <span className="queue-indicator">{queueDepth} queued</span>
          )}
          {inFlight ? (
            <button
              className="stop-btn"
              onClick={() => void stopTurn()}
              disabled={stopping}
              title="Stop the current turn"
            >
              {stopping ? "Stopping…" : "Stop"}
              {!stopping && (
                <svg viewBox="0 0 24 24" fill="currentColor" aria-hidden>
                  <rect x="6" y="6" width="12" height="12" rx="1.5" />
                </svg>
              )}
            </button>
          ) : (
            <button
              className="send-btn"
              onClick={() => void send()}
              disabled={sendDisabled}
            >
              {sending ? "Sending…" : "Send"}
              {!sending && (
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2.5"
                  strokeLinecap="round"
                  strokeLinejoin="round"
                  aria-hidden
                >
                  <path d="M5 12h14M13 5l7 7-7 7" />
                </svg>
              )}
            </button>
          )}
        </div>
      </div>
      {error && <div className="error-text input-error">{error}</div>}
    </div>
  );
}

function clientId(): string {
  const w = window as unknown as { __agentctlClientId?: string };
  if (!w.__agentctlClientId) {
    w.__agentctlClientId = `web-${cryptoRandomId()}`;
  }
  return w.__agentctlClientId;
}

function cryptoRandomId(): string {
  const bytes = new Uint8Array(16);
  crypto.getRandomValues(bytes);
  let out = "";
  for (const b of bytes) out += b.toString(16).padStart(2, "0");
  return out;
}

// Sentinel returns from resolveModelName — distinguishable from any valid
// model id (those never start with a dot per Anthropic / OpenAI naming).
export const MODEL_AMBIGUOUS = ".ambiguous";
export const MODEL_UNKNOWN = ".unknown";

// parseSlashModel returns the argument to a `/model <name>` command, or
// null if the message isn't that command. Quoting / extra whitespace is
// tolerated; trailing extra args are ignored (a `/model opus please`
// resolves on the first token, matching how skill commands behave).
export function parseSlashModel(content: string): string | null {
  const trimmed = content.trim();
  if (!trimmed.startsWith("/model")) return null;
  const rest = trimmed.slice("/model".length);
  // Must be followed by whitespace or end-of-input to avoid matching
  // "/modelfoo" as if it were "/model foo".
  if (rest.length > 0 && !/^\s/.test(rest)) return null;
  const arg = rest.trim().split(/\s+/)[0] || "";
  return arg;
}

// resolveModelName takes the user's argument and the provider's catalog
// and returns either an exact catalog id or one of the sentinel
// constants. Priority: exact match → unique substring match. We treat
// case-insensitive substrings as "fuzzy enough" — the catalog is small
// (≤10 entries in practice) so we don't need a real Levenshtein.
export function resolveModelName(arg: string, models: string[]): string {
  if (!arg) return MODEL_UNKNOWN;
  if (models.includes(arg)) return arg;
  const lower = arg.toLowerCase();
  const matches = models.filter((m) => m.toLowerCase().includes(lower));
  if (matches.length === 1) return matches[0];
  if (matches.length === 0) return MODEL_UNKNOWN;
  // Multiple matches: if one is an exact case-insensitive match, prefer
  // it. Otherwise the user has to disambiguate.
  const exactCI = matches.find((m) => m.toLowerCase() === lower);
  if (exactCI) return exactCI;
  return MODEL_AMBIGUOUS;
}
