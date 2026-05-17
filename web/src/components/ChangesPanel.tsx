import { useCallback, useEffect, useMemo, useState } from "react";
import { ApiError, api, apiJson } from "../api";
import type { RepoInfo } from "../types";
import { parseUnifiedDiff, type FileDiff } from "../lib/diff";
import { FileTree } from "./FileTree";
import { FileDiffView, type DiffViewMode } from "./FileDiffView";

interface Props {
  sessionId: string;
}

interface PushOutcome {
  success: boolean;
  branch?: string;
  output?: string;
  error?: string;
}

interface RepoDiff {
  repo: string;
  state: "loading" | "ready" | "error";
  files: FileDiff[];
  error?: string;
}

export function ChangesPanel({ sessionId }: Props) {
  const [repos, setRepos] = useState<RepoInfo[] | null>(null);
  const [diffs, setDiffs] = useState<Record<string, RepoDiff>>({});
  const [activeRepo, setActiveRepo] = useState<string | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | null>(null);
  const [mode, setMode] = useState<DiffViewMode>("unified");
  const [refreshing, setRefreshing] = useState(false);
  const [pushFor, setPushFor] = useState<string | null>(null);
  const [pushBranch, setPushBranch] = useState("");
  const [pushMessage, setPushMessage] = useState("");
  const [pushOutcome, setPushOutcome] = useState<PushOutcome | null>(null);
  const [pushBusy, setPushBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Load repos once per session.
  useEffect(() => {
    let cancelled = false;
    apiJson<{ repos: RepoInfo[] }>(
      `/v1/sessions/${encodeURIComponent(sessionId)}/repos`,
    )
      .then((r) => {
        if (cancelled) return;
        const list = r?.repos ?? [];
        setRepos(list);
        if (list.length > 0 && !activeRepo) setActiveRepo(list[0].name);
      })
      .catch((e) => {
        if (cancelled) return;
        setError(e instanceof ApiError ? e.message : String(e));
        setRepos([]);
      });
    return () => {
      cancelled = true;
    };
    // activeRepo intentionally excluded — we set it once on first load.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [sessionId]);

  const fetchRepoDiff = useCallback(
    async (repo: string) => {
      setDiffs((prev) => ({
        ...prev,
        [repo]: { repo, state: "loading", files: [] },
      }));
      try {
        const res = await api(
          `/v1/sessions/${encodeURIComponent(sessionId)}/diff?repo=${encodeURIComponent(repo)}`,
        );
        if (!res.ok) {
          const txt = await res.text();
          setDiffs((prev) => ({
            ...prev,
            [repo]: {
              repo,
              state: "error",
              files: [],
              error: `diff failed (${res.status}): ${txt}`,
            },
          }));
          return;
        }
        const text = await res.text();
        const files = parseUnifiedDiff(text);
        setDiffs((prev) => ({
          ...prev,
          [repo]: { repo, state: "ready", files },
        }));
      } catch (e) {
        const msg = e instanceof ApiError ? e.message : String(e);
        setDiffs((prev) => ({
          ...prev,
          [repo]: { repo, state: "error", files: [], error: msg },
        }));
      }
    },
    [sessionId],
  );

  // Fetch the active repo's diff lazily on first selection and whenever the
  // active repo changes (a different repo's tab was clicked). Other repos
  // load on demand when the user switches tabs — keeps initial paint cheap.
  useEffect(() => {
    if (!activeRepo) return;
    if (diffs[activeRepo]) return;
    void fetchRepoDiff(activeRepo);
  }, [activeRepo, diffs, fetchRepoDiff]);

  // When the active repo's diff finishes loading, auto-select the first
  // file so the right pane isn't blank.
  const activeDiff = activeRepo ? diffs[activeRepo] : undefined;
  useEffect(() => {
    if (!activeDiff || activeDiff.state !== "ready") return;
    if (selectedPath && activeDiff.files.some((f) => f.path === selectedPath)) {
      return;
    }
    setSelectedPath(activeDiff.files[0]?.path ?? null);
  }, [activeDiff, selectedPath]);

  const refresh = useCallback(async () => {
    if (!activeRepo) return;
    setRefreshing(true);
    try {
      await fetchRepoDiff(activeRepo);
    } finally {
      setRefreshing(false);
    }
  }, [activeRepo, fetchRepoDiff]);

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

  const selectedFile = useMemo(() => {
    if (!activeDiff || activeDiff.state !== "ready" || !selectedPath) {
      return null;
    }
    return activeDiff.files.find((f) => f.path === selectedPath) ?? null;
  }, [activeDiff, selectedPath]);

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

      {repos.length > 1 && (
        <div className="diff-repo-tabs" role="tablist">
          {repos.map((r) => {
            const d = diffs[r.name];
            const total = d?.files.length;
            return (
              <button
                key={r.name}
                type="button"
                role="tab"
                aria-selected={activeRepo === r.name}
                className={`diff-repo-tab ${activeRepo === r.name ? "active" : ""}`}
                onClick={() => {
                  setActiveRepo(r.name);
                  setSelectedPath(null);
                }}
              >
                <span className="diff-repo-name">{r.name}</span>
                {typeof total === "number" && total > 0 && (
                  <span className="diff-repo-badge">{total}</span>
                )}
                {d?.state === "loading" && (
                  <span className="diff-repo-spinner" aria-hidden>
                    ⋯
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}

      {activeRepo && (
        <RepoDiffView
          repo={activeRepo}
          state={activeDiff}
          selectedPath={selectedPath}
          onSelect={setSelectedPath}
          mode={mode}
          onModeChange={setMode}
          refreshing={refreshing}
          onRefresh={refresh}
          selectedFile={selectedFile}
        />
      )}

      <section className="changes-actions">
        <h4>Export</h4>
        <table className="repos-table">
          <thead>
            <tr>
              <th>repo</th>
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

function RepoDiffView({
  repo,
  state,
  selectedPath,
  onSelect,
  mode,
  onModeChange,
  refreshing,
  onRefresh,
  selectedFile,
}: {
  repo: string;
  state: RepoDiff | undefined;
  selectedPath: string | null;
  onSelect: (p: string) => void;
  mode: DiffViewMode;
  onModeChange: (m: DiffViewMode) => void;
  refreshing: boolean;
  onRefresh: () => void;
  selectedFile: FileDiff | null;
}) {
  const totals = useMemo(() => {
    if (!state || state.state !== "ready") return null;
    let added = 0;
    let removed = 0;
    for (const f of state.files) {
      added += f.added;
      removed += f.removed;
    }
    return { files: state.files.length, added, removed };
  }, [state]);

  return (
    <section className="diff-section">
      <div className="diff-toolbar">
        <div className="diff-toolbar-left">
          <strong className="diff-toolbar-repo" title={`Repo: ${repo}`}>
            {repo}
          </strong>
          {totals && totals.files > 0 && (
            <span className="diff-toolbar-totals">
              <span>
                {totals.files} file{totals.files === 1 ? "" : "s"}
              </span>
              <span className="diff-add">+{totals.added}</span>
              <span className="diff-rem">−{totals.removed}</span>
            </span>
          )}
        </div>
        <div className="diff-toolbar-right">
          <div className="diff-mode-toggle" role="radiogroup" aria-label="View mode">
            <button
              type="button"
              role="radio"
              aria-checked={mode === "unified"}
              className={mode === "unified" ? "active" : ""}
              onClick={() => onModeChange("unified")}
            >
              Unified
            </button>
            <button
              type="button"
              role="radio"
              aria-checked={mode === "split"}
              className={mode === "split" ? "active" : ""}
              onClick={() => onModeChange("split")}
            >
              Split
            </button>
          </div>
          <button
            type="button"
            onClick={onRefresh}
            disabled={refreshing || state?.state === "loading"}
            title="Refresh"
          >
            {refreshing || state?.state === "loading" ? "Refreshing…" : "Refresh"}
          </button>
        </div>
      </div>

      {state?.state === "loading" || !state ? (
        <div className="diff-empty">Loading diff…</div>
      ) : state.state === "error" ? (
        <div className="warning">{state.error}</div>
      ) : state.files.length === 0 ? (
        <div className="diff-empty">No changes against the base ref.</div>
      ) : (
        <div className="diff-layout">
          <aside className="diff-tree-pane">
            <FileTree
              files={state.files}
              selectedPath={selectedPath}
              onSelect={onSelect}
            />
          </aside>
          <main className="diff-view-pane">
            {selectedFile ? (
              <FileDiffView file={selectedFile} mode={mode} />
            ) : (
              <div className="diff-empty">Select a file from the tree.</div>
            )}
          </main>
        </div>
      )}
    </section>
  );
}
