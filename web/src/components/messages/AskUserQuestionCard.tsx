import { useEffect, useMemo, useState } from "react";
import { ApiError, apiJson, jsonBody } from "../../api";
import type { SendMessageRequest, SendMessageResponse } from "../../types";

interface OptionSpec {
  label: string;
  description?: string;
}

interface QuestionSpec {
  question: string;
  header?: string;
  multiSelect?: boolean;
  options: OptionSpec[];
}

interface Props {
  sessionId: string | undefined;
  toolUseId: string | undefined;
  input: unknown;
}

const STORAGE_PREFIX = "agentctl.askq.";

function parseQuestions(input: unknown): QuestionSpec[] | null {
  let obj: unknown = input;
  if (typeof obj === "string") {
    try {
      obj = JSON.parse(obj);
    } catch {
      return null;
    }
  }
  if (!obj || typeof obj !== "object") return null;
  const qs = (obj as Record<string, unknown>).questions;
  if (!Array.isArray(qs)) return null;
  const out: QuestionSpec[] = [];
  for (const raw of qs) {
    if (!raw || typeof raw !== "object") continue;
    const r = raw as Record<string, unknown>;
    const question = typeof r.question === "string" ? r.question : "";
    if (!question) continue;
    const opts = Array.isArray(r.options) ? r.options : [];
    const options: OptionSpec[] = [];
    for (const o of opts) {
      if (!o || typeof o !== "object") continue;
      const label = (o as Record<string, unknown>).label;
      if (typeof label !== "string" || !label) continue;
      const desc = (o as Record<string, unknown>).description;
      options.push({
        label,
        description: typeof desc === "string" ? desc : undefined,
      });
    }
    if (options.length === 0) continue;
    out.push({
      question,
      header: typeof r.header === "string" ? r.header : undefined,
      multiSelect: r.multiSelect === true,
      options,
    });
  }
  return out.length > 0 ? out : null;
}

function quote(s: string): string {
  return `"${s.replace(/\\/g, "\\\\").replace(/"/g, '\\"')}"`;
}

function formatAnswerMessage(
  questions: QuestionSpec[],
  selections: string[][],
  notes: string[],
): string {
  const parts: string[] = [];
  for (let i = 0; i < questions.length; i++) {
    const q = questions[i];
    const picks = selections[i] ?? [];
    const value = picks.length > 0 ? picks.map(quote).join(", ") : "(skipped)";
    parts.push(`${quote(q.question)}=${value}`);
  }
  let msg = `Your questions have been answered: ${parts.join(", ")}.`;
  const trimmed = notes
    .map((n, i) => (n.trim() ? `${quote(questions[i].question)}: ${n.trim()}` : ""))
    .filter(Boolean);
  if (trimmed.length > 0) {
    msg += ` Notes: ${trimmed.join(" | ")}.`;
  }
  msg += " You can now continue with these answers in mind.";
  return msg;
}

export function AskUserQuestionCard({ sessionId, toolUseId, input }: Props) {
  const questions = useMemo(() => parseQuestions(input), [input]);

  const storageKey = toolUseId ? STORAGE_PREFIX + toolUseId : null;
  const [submitted, setSubmitted] = useState<string | null>(() => {
    if (!storageKey) return null;
    try {
      return window.localStorage.getItem(storageKey);
    } catch {
      return null;
    }
  });
  const [sending, setSending] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [selections, setSelections] = useState<string[][]>(() =>
    (questions ?? []).map(() => []),
  );
  const [notes, setNotes] = useState<string[]>(() =>
    (questions ?? []).map(() => ""),
  );

  useEffect(() => {
    if (!storageKey) return;
    try {
      const cached = window.localStorage.getItem(storageKey);
      if (cached) setSubmitted(cached);
    } catch {
      // ignore
    }
  }, [storageKey]);

  if (!questions) {
    return null;
  }

  if (submitted) {
    return (
      <div className="askq-card askq-answered">
        <div className="askq-answered-label">Answered</div>
        <div className="askq-answered-text">{submitted}</div>
      </div>
    );
  }

  const allAnswered = questions.every((_q, i) => {
    const picks = selections[i] ?? [];
    return picks.length > 0 || notes[i]?.trim();
  });

  function togglePick(qIdx: number, label: string, multi: boolean) {
    setSelections((prev) => {
      const next = prev.map((row) => row.slice());
      const row = next[qIdx] ?? [];
      const has = row.includes(label);
      if (multi) {
        next[qIdx] = has ? row.filter((l) => l !== label) : [...row, label];
      } else {
        next[qIdx] = has ? [] : [label];
      }
      return next;
    });
  }

  async function submit() {
    if (!sessionId) {
      setError("This question card isn't attached to a session.");
      return;
    }
    if (sending) return;
    setSending(true);
    setError(null);
    const content = formatAnswerMessage(questions!, selections, notes);
    try {
      const req: SendMessageRequest = {
        content,
        client_id: "askq-" + (toolUseId ?? Math.random().toString(36).slice(2)),
        idempotency_key:
          "askq-" + (toolUseId ?? "") + "-" + Math.random().toString(36).slice(2),
      };
      await apiJson<SendMessageResponse>(
        `/v1/sessions/${encodeURIComponent(sessionId)}/messages`,
        { method: "POST", ...jsonBody(req) },
      );
      if (storageKey) {
        try {
          window.localStorage.setItem(storageKey, content);
        } catch {
          // storage may be disabled; we still mark submitted in-memory.
        }
      }
      setSubmitted(content);
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

  return (
    <div className="askq-card">
      <div className="askq-intro">
        The agent is asking for your input. Pick an answer for each question
        and submit to continue the turn.
      </div>
      {questions.map((q, qIdx) => {
        const multi = !!q.multiSelect;
        const inputName = `askq-${toolUseId ?? "x"}-${qIdx}`;
        return (
          <div key={qIdx} className="askq-question">
            <div className="askq-question-head">
              {q.header && <span className="askq-question-tag">{q.header}</span>}
              <span className="askq-question-text">{q.question}</span>
              {multi && (
                <span className="askq-multi-hint">(choose any)</span>
              )}
            </div>
            <div className="askq-options">
              {q.options.map((opt) => {
                const checked = (selections[qIdx] ?? []).includes(opt.label);
                return (
                  <label
                    key={opt.label}
                    className={`askq-option ${checked ? "checked" : ""}`}
                  >
                    <input
                      type={multi ? "checkbox" : "radio"}
                      name={inputName}
                      checked={checked}
                      onChange={() => togglePick(qIdx, opt.label, multi)}
                    />
                    <span className="askq-option-body">
                      <span className="askq-option-label">{opt.label}</span>
                      {opt.description && (
                        <span className="askq-option-desc">{opt.description}</span>
                      )}
                    </span>
                  </label>
                );
              })}
            </div>
            <textarea
              className="askq-note"
              placeholder="Other / notes (optional)"
              value={notes[qIdx] ?? ""}
              rows={2}
              onChange={(e) =>
                setNotes((prev) => {
                  const next = prev.slice();
                  next[qIdx] = e.target.value;
                  return next;
                })
              }
            />
          </div>
        );
      })}
      {error && <div className="askq-error">{error}</div>}
      <div className="askq-actions">
        <button
          type="button"
          className="askq-submit"
          onClick={submit}
          disabled={!allAnswered || sending || !sessionId}
        >
          {sending ? "Sending…" : "Submit answers"}
        </button>
        {!sessionId && (
          <span className="askq-disabled-hint">
            Open the session detail to answer.
          </span>
        )}
      </div>
    </div>
  );
}
