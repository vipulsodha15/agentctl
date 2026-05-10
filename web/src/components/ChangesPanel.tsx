import { useCallback, useEffect, useState } from "react";
import { ApiError, api, apiJson } from "../api";
import type { RepoInfo } from "../types";

interface Props {
  sessionId: string;
}

interface PushOutcome {
  success: boolean;
  branch?: string;
  output?: string;
  error?: string;
}

export function ChangesPanel({ sessionId }: Props) {
  const [repos, setRepos] = useState<RepoInfo[] | null>(null);
  const [diffOpen, setDiffOpen] = useState(false);
  const [diffText, setDiffText] = useState<string>("");
  const [diffLoading, setDiffLoading] = useState(false);
  const [pushFor, setPushFor] = useState<string | null>(null);
  const [pushBranch, setPushBranch] = useState("");
  const [pushMessage, setPushMessage] = useState("");
  const [pushOutcome, setPushOutcome] = useState<PushOutcome | null>(null);
  const [pushBusy, setPushBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    apiJson<{ repos: RepoInfo[] }>(
      `/v1/sessions/${encodeURIComponent(sessionId)}/repos`,
    )
      .then((r) => {
        if (!cancelled) setRepos(r?.repos ?? []);
      })
      .catch((e) => {
        if (!cancelled) {
          setError(e instanceof ApiError ? e.message : String(e));
          setRepos([]);
        }
      });
    return () => {
      cancelled = true;
    };
  }, [sessionId]);

  const loadDiff = useCallback(async () => {
    setDiffLoading(true);
    setError(null);
    try {
      const res = await api(
        `/v1/sessions/${encodeURIComponent(sessionId)}/diff`,
      );
      if (!res.ok) {
        const txt = await res.text();
        setError(`diff failed (${res.status}): ${txt}`);
        return;
      }
      setDiffText(await res.text());
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    } finally {
      setDiffLoading(false);
    }
  }, [sessionId]);

  const downloadPatch = useCallback(
    async (repo: string) => {
      setError(null);
      try {
        const res = await api(
          `/v1/sessions/${encodeURIComponent(sessionId)}/export/patch`,
          {
            method: "POST",
            body: JSON.stringify({ repo }),
            headers: { "Content-Type": "application/json" },
          },
        );
        if (!res.ok) {
          const txt = await res.text();
          setError(`patch failed (${res.status}): ${txt}`);
          return;
        }
        const blob = await res.blob();
        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = `${repo || "session"}.patch`;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
      } catch (e) {
        setError(e instanceof ApiError ? e.message : String(e));
      }
    },
    [sessionId],
  );

  const submitPush = useCallback(async () => {
    if (!pushFor || !pushBranch.trim()) return;
    setPushBusy(true);
    setPushOutcome(null);
    setError(null);
    try {
      const r = await apiJson<PushOutcome>(
        `/v1/sessions/${encodeURIComponent(sessionId)}/export/push`,
        {
          method: "POST",
          body: JSON.stringify({
            repo: pushFor,
            branch: pushBranch.trim(),
            message: pushMessage.trim() || undefined,
          }),
          headers: { "Content-Type": "application/json" },
        },
      );
      setPushOutcome(r);
      if (r.success) {
        setPushBranch("");
        setPushMessage("");
      }
    } catch (e) {
      const msg = e instanceof ApiError ? e.message : String(e);
      setPushOutcome({ success: false, error: msg });
    } finally {
      setPushBusy(false);
    }
  }, [sessionId, pushFor, pushBranch, pushMessage]);

  if (repos === null) {
    return <div className="empty">Loading repos…</div>;
  }
  if (repos.length === 0) {
    return (
      <div className="empty">
        No repos in this session.
        {error && <div className="warning">{error}</div>}
      </div>
    );
  }

  return (
    <div className="changes-panel">
      {error && <div className="warning">{error}</div>}

      <section>
        <div className="row">
          <button onClick={() => {
            const next = !diffOpen;
            setDiffOpen(next);
            if (next && !diffText && !diffLoading) {
              void loadDiff();
            }
          }}>
            {diffOpen ? "Hide diff" : "Show diff"}
          </button>
          {diffOpen && (
            <button onClick={() => void loadDiff()} disabled={diffLoading}>
              {diffLoading ? "Refreshing…" : "Refresh"}
            </button>
          )}
        </div>
        {diffOpen && (
          <pre className="diff-view">
            {diffLoading ? "Loading…" : diffText || "(no changes)"}
          </pre>
        )}
      </section>

      <section>
        <h4>Repos</h4>
        <table className="repos-table">
          <thead>
            <tr>
              <th>name</th>
              <th>branch</th>
              <th></th>
            </tr>
          </thead>
          <tbody>
            {repos.map((r) => (
              <tr key={r.name}>
                <td>
                  <code>{r.name}</code>
                </td>
                <td>{r.branch || "—"}</td>
                <td className="row">
                  <button onClick={() => void downloadPatch(r.name)}>
                    Download patch
                  </button>
                  <button
                    onClick={() => {
                      setPushFor(r.name);
                      setPushOutcome(null);
                    }}
                  >
                    Push to branch…
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </section>

      {pushFor && (
        <section className="push-modal">
          <h4>Push {pushFor}</h4>
          <label>
            Branch
            <input
              value={pushBranch}
              onChange={(e) => setPushBranch(e.target.value)}
              placeholder="feat/my-branch"
              autoFocus
            />
          </label>
          <label>
            Message <span className="empty">(optional)</span>
            <input
              value={pushMessage}
              onChange={(e) => setPushMessage(e.target.value)}
              placeholder="agentctl session changes"
            />
          </label>
          <div className="row">
            <button
              onClick={() => void submitPush()}
              disabled={pushBusy || !pushBranch.trim()}
            >
              {pushBusy ? "Pushing…" : "Push"}
            </button>
            <button onClick={() => setPushFor(null)} disabled={pushBusy}>
              Cancel
            </button>
          </div>
          {pushOutcome && (
            <div
              className={pushOutcome.success ? "" : "warning"}
              style={{ whiteSpace: "pre-wrap" }}
            >
              {pushOutcome.success
                ? `Pushed ${pushOutcome.branch}.`
                : `Push failed: ${pushOutcome.error || "unknown error"}`}
              {pushOutcome.output ? "\n\n" + pushOutcome.output : ""}
            </div>
          )}
        </section>
      )}
    </div>
  );
}
