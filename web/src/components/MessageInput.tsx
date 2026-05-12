import { useRef, useState } from "react";
import { ApiError, apiJson, jsonBody } from "../api";
import type { SendMessageRequest, SendMessageResponse } from "../types";
import { SkillAutocomplete, getSkillNav } from "./SkillAutocomplete";

interface Props {
  sessionId: string;
  inFlight: boolean;
  queueDepth: number;
}

export function MessageInput({ sessionId, inFlight, queueDepth }: Props) {
  const [value, setValue] = useState("");
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const taRef = useRef<HTMLTextAreaElement>(null);

  async function send() {
    const content = value.trim();
    if (!content) return;
    if (sending) return;
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

  const disabled = sending || inFlight || value.trim() === "";

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
            <kbd
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: 10.5,
                background: "var(--c-surface-2)",
                padding: "1px 5px",
                borderRadius: 4,
                border: "1px solid var(--c-border)",
              }}
            >
              ⏎
            </kbd>{" "}
            send ·{" "}
            <kbd
              style={{
                fontFamily: "var(--font-mono)",
                fontSize: 10.5,
                background: "var(--c-surface-2)",
                padding: "1px 5px",
                borderRadius: 4,
                border: "1px solid var(--c-border)",
              }}
            >
              ⇧⏎
            </kbd>{" "}
            newline
          </span>
          {queueDepth > 0 && (
            <span className="queue-indicator">{queueDepth} queued</span>
          )}
          <button
            className="send-btn"
            onClick={() => void send()}
            disabled={disabled}
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
