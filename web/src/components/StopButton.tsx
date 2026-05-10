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
      <button className="danger" onClick={onStop} disabled={busy}>
        {busy ? "Stopping…" : "Stop turn"}
      </button>
      {error && <span className="error-text">{error}</span>}
    </>
  );
}
