import { useState } from "react";
import { ApiError, apiJson, jsonBody } from "../api";
import type { InterruptResponse } from "../types";

interface Props {
  sessionId: string;
  inFlight: boolean;
}

export function StopButton({ sessionId, inFlight }: Props) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onStop() {
    if (busy) return;
    setBusy(true);
    setError(null);
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
      setBusy(false);
    }
  }

  if (!inFlight) return null;
  return (
    <>
      <button className="danger" onClick={onStop} disabled={busy} title="Stop the current turn">
        {!busy && (
          <svg
            width="11"
            height="11"
            viewBox="0 0 24 24"
            fill="currentColor"
            aria-hidden
            style={{ marginRight: 6, marginLeft: -2 }}
          >
            <rect x="6" y="6" width="12" height="12" rx="1.5" />
          </svg>
        )}
        {busy ? "Stopping…" : "Stop turn"}
      </button>
      {error && <span className="error-text">{error}</span>}
    </>
  );
}
