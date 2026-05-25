import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import { attach } from "../ws";
import type {
  ProvidersResponse,
  SessionStatus,
  SnapshotData,
  WireEvent,
} from "../types";
import { ConversationView, type TranscriptFilter } from "../components/ConversationView";
import { MessageInput } from "../components/MessageInput";
import { McpPanel } from "../components/McpPanel";
import { CostPanel } from "../components/CostPanel";
import { ChangesPanel } from "../components/ChangesPanel";
import {
  INITIAL_CONVERSATION_STATE,
  conversationReducer,
} from "../lib/conversation";
import { ConfirmModal } from "../components/ConfirmModal";


const SIDE_PANEL_COLLAPSED_KEY = "agentctl.sidePanel.collapsed";

export function SessionDetail() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const [state, dispatch] = useReducer(
    conversationReducer,
    INITIAL_CONVERSATION_STATE,
  );
  const [tab, setTab] = useState<"conversation" | "changes">("conversation");
  const [endBusy, setEndBusy] = useState(false);
  const [confirmEnd, setConfirmEnd] = useState(false);
  const [costRefreshKey, setCostRefreshKey] = useState(0);
  const [filter, setFilter] = useState<TranscriptFilter>("all");
  // Mid-session model swap (ADR 0020 §2 / Phase 4). The dropdown is the
  // primary surface per the ADR's UX principles — keyboard users get
  // `/model` in the input box, scripters get `agentctl session set-model`.
  // We track the current model separately from the conversation reducer
  // because the snapshot doesn't carry it (the SDK's JSONL records are
  // model-tagged per turn, not session-wide).
  const [currentModel, setCurrentModel] = useState<string>("");
  const [providerModels, setProviderModels] = useState<string[]>([]);
  const [modelSwapping, setModelSwapping] = useState(false);
  const [modelError, setModelError] = useState<string | null>(null);
  const [sideCollapsed, setSideCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SIDE_PANEL_COLLAPSED_KEY) === "1";
    } catch {
      return false;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem(SIDE_PANEL_COLLAPSED_KEY, sideCollapsed ? "1" : "0");
    } catch {
      // ignore — storage may be disabled
    }
  }, [sideCollapsed]);

  // Initial fetch for session metadata (name, status before snapshot lands).
  useEffect(() => {
    if (!id) return;
    let cancelled = false;
    apiJson<{ session: { name?: string; status?: SessionStatus; model?: string } }>(
      `/v1/sessions/${encodeURIComponent(id)}`,
    )
      .then((r) => {
        if (cancelled) return;
        if (r?.session) {
          if (typeof r.session.model === "string" && r.session.model) {
            setCurrentModel(r.session.model);
          }
          dispatch({
            type: "event",
            e: {
              kind: "session." + (r.session.status ?? "running"),
              data: {},
            } as WireEvent,
          });
        }
      })
      .catch(() => {
        // non-fatal; the WS snapshot will populate state.
      });
    return () => {
      cancelled = true;
    };
  }, [id]);

  // Provider catalog drives the model dropdown. Single-provider installs
  // (every install before ADR 0020 Phase 1 lands) get a flat models[]; the
  // dropdown silently hides itself when the catalog is empty so installs
  // without a populated pricing table keep the pre-Phase-4 display.
  useEffect(() => {
    let cancelled = false;
    apiJson<ProvidersResponse>("/v1/providers")
      .then((r) => {
        if (cancelled) return;
        const models = collectAllModels(r);
        setProviderModels(models);
      })
      .catch(() => {
        if (!cancelled) setProviderModels([]);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const switchModel = useCallback(
    async (next: string) => {
      if (!id) return;
      if (!next || next === currentModel) return;
      setModelSwapping(true);
      setModelError(null);
      const prev = currentModel;
      // Optimistic: flip the local state so the dropdown shows the new
      // selection immediately. Roll back on error.
      setCurrentModel(next);
      try {
        await apiJson(`/v1/sessions/${encodeURIComponent(id)}`, {
          method: "PATCH",
          ...jsonBody({ model: next }),
        });
      } catch (err) {
        setCurrentModel(prev);
        const msg =
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err);
        setModelError(msg);
      } finally {
        setModelSwapping(false);
      }
    },
    [id, currentModel],
  );

  // Escape cancels the active turn — mirrors the MessageInput Stop button.
  // The actor leaves the queue intact, so the next queued message starts
  // automatically once the cancellation lands. Skip when a modal is open
  // so Modal/ConfirmModal's own Escape-to-close keeps working.
  useEffect(() => {
    if (!id) return;
    if (!state.inFlight) return;
    let stopping = false;
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape") return;
      if (document.querySelector('[role="dialog"]')) return;
      if (stopping) return;
      stopping = true;
      apiJson(`/v1/sessions/${encodeURIComponent(id)}/interrupt`, {
        method: "POST",
        ...jsonBody({ clear_queue: false }),
      })
        .catch(() => {
          // Errors surface via the existing event stream / status panel.
        })
        .finally(() => {
          stopping = false;
        });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [id, state.inFlight]);

  useEffect(() => {
    if (!id) return;
    dispatch({ type: "reset" });
    const handle = attach(id, {
      onOpen: () => dispatch({ type: "ws_open" }),
      onDisconnect: (reason) => dispatch({ type: "ws_close", reason }),
      onEvent: (e) => {
        if (e.kind === "session.snapshot") {
          dispatch({ type: "snapshot", data: e.data as SnapshotData });
          return;
        }
        // Frame envelopes from the unix surface (kind=stream_end / error)
        // can also arrive here on the WS in some implementations; ignore.
        if (
          e.kind === "stream_end" ||
          (e as { kind: string }).kind === "error"
        ) {
          return;
        }
        if (
          e.kind === "usage" ||
          e.kind === "turn.end" ||
          e.kind === "turn.cancelled"
        ) {
          setCostRefreshKey((k) => k + 1);
        }
        dispatch({ type: "event", e });
      },
    });
    return () => handle.close();
  }, [id]);

  const onEnd = useCallback(() => {
    if (!id) return;
    setConfirmEnd(true);
  }, [id]);

  const doEnd = useCallback(async () => {
    if (!id) return;
    setEndBusy(true);
    try {
      await apiJson(`/v1/sessions/${encodeURIComponent(id)}`, {
        method: "DELETE",
      });
      setConfirmEnd(false);
      navigate("/sessions");
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err);
      alert(`Failed to end session: ${msg}`);
      setEndBusy(false);
      setConfirmEnd(false);
    }
  }, [id, navigate]);

  const visibleMessages = useMemo(() => state.messages, [state.messages]);

  if (!id) return null;

  return (
    <section
      className={`session-detail${sideCollapsed ? " side-collapsed" : ""}`}
    >
      <div className="session-header">
        <span className="breadcrumb">
          <Link to="/sessions">Sessions</Link>
          <span aria-hidden>/</span>
        </span>
        <span className="session-title" title={state.name || id}>
          {state.name || id}
        </span>
        <span className={`status-badge ${state.status}`}>{state.status}</span>
        <ModelControl
          current={currentModel}
          models={providerModels}
          swapping={modelSwapping}
          error={modelError}
          onChange={switchModel}
        />
        {state.inFlight && (
          <span className="responding-pill" aria-live="polite">
            <span className="bdot" />
            <span className="bdot" />
            <span className="bdot" />
            <span className="label">Responding</span>
          </span>
        )}
        {state.queueDepth > 0 && (
          <span className="queue-pill">{state.queueDepth} queued</span>
        )}
        {!state.connected && state.disconnectReason && (
          <span className="reconnect-pill">
            <span className="streaming-dot" aria-hidden />
            reconnecting…
          </span>
        )}
        <span className="actions">
          <button className="danger" onClick={onEnd} disabled={endBusy}>
            {endBusy ? "Ending…" : "End session"}
          </button>
        </span>
      </div>

      <div
        className={`conv-card${
          state.inFlight && tab === "conversation" ? " in-flight" : ""
        }`}
      >
        <div className="tabs">
          <button
            className={tab === "conversation" ? "active" : ""}
            onClick={() => setTab("conversation")}
          >
            Conversation
          </button>
          <button
            className={tab === "changes" ? "active" : ""}
            onClick={() => setTab("changes")}
          >
            Changes
          </button>
          {tab === "conversation" && (
            <div className="filter-bar">
              <FilterButton current={filter} value="all" onSet={setFilter}>
                All
              </FilterButton>
              <FilterButton current={filter} value="text" onSet={setFilter}>
                Text
              </FilterButton>
              <FilterButton current={filter} value="tools" onSet={setFilter}>
                Tools
              </FilterButton>
              <FilterButton current={filter} value="errors" onSet={setFilter}>
                Errors
              </FilterButton>
            </div>
          )}
        </div>
        {tab === "conversation" ? (
          <ConversationView
            messages={visibleMessages}
            warnings={state.warnings}
            inFlight={state.inFlight}
            mcps={state.mcps}
            usageByTurn={state.usageByTurn}
            filter={filter}
            sessionId={id}
          />
        ) : (
          <div className="conversation">
            <ChangesPanel sessionId={id} />
          </div>
        )}
        {tab === "conversation" && (
          <div className="input-area-wrap">
            <MessageInput
              sessionId={id}
              inFlight={state.inFlight}
              queueDepth={state.queueDepth}
              providerModels={providerModels}
              currentModel={currentModel}
              onModelSwitch={switchModel}
            />
          </div>
        )}
      </div>

      <aside className={`side${sideCollapsed ? " collapsed" : ""}`}>
        <button
          type="button"
          className="side-toggle"
          onClick={() => setSideCollapsed((v) => !v)}
          aria-label={sideCollapsed ? "Expand side panel" : "Collapse side panel"}
          aria-expanded={!sideCollapsed}
          title={sideCollapsed ? "Expand side panel" : "Collapse side panel"}
        >
          <SideChevron direction={sideCollapsed ? "left" : "right"} />
        </button>
        {!sideCollapsed && (
          <>
            <McpPanel mcps={state.mcps} />
            <CostPanel sessionId={id} refreshKey={costRefreshKey} />
          </>
        )}
      </aside>
      <ConfirmModal
        open={confirmEnd}
        title="End session?"
        message="Container, volume, and history will be removed."
        confirmLabel={endBusy ? "Ending…" : "End session"}
        variant="danger"
        busy={endBusy}
        onConfirm={doEnd}
        onCancel={() => setConfirmEnd(false)}
      />
    </section>
  );
}

function SideChevron({ direction }: { direction: "left" | "right" }) {
  return (
    <svg
      className="chevron-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {direction === "left" ? (
        <polyline points="15 6 9 12 15 18" />
      ) : (
        <polyline points="9 6 15 12 9 18" />
      )}
    </svg>
  );
}

// collectAllModels flattens the per-provider catalog into a deduped,
// stable-ordered list. The dropdown filters to a single provider once the
// Codex split (ADR 0020 Phase 1) lands and the session detail learns its
// session.provider; until then every model is reachable from every
// session, which preserves single-provider behavior.
function collectAllModels(catalog: ProvidersResponse | null | undefined): string[] {
  if (!catalog) return [];
  const seen = new Set<string>();
  const out: string[] = [];
  for (const key of Object.keys(catalog).sort()) {
    const entry = catalog[key];
    if (!entry || !entry.enabled) continue;
    for (const m of entry.models || []) {
      if (!seen.has(m)) {
        seen.add(m);
        out.push(m);
      }
    }
  }
  return out;
}

interface ModelControlProps {
  current: string;
  models: string[];
  swapping: boolean;
  error: string | null;
  onChange: (next: string) => void;
}

// ModelControl renders the per-session model picker that lives in the
// session header (ADR 0020 §UX principles — "the in-session model
// dropdown is the affordance users will reach for"). Three layout cases:
//
//   - No catalog: the daemon's /v1/providers returned an empty map (a
//     fresh install with no pricing-table models). We render the current
//     model as plain text, matching pre-Phase-4 behavior — no UI noise
//     in setups that aren't using the catalog yet.
//   - Catalog without the current model: the runtime is reporting a model
//     not in the user's pricing table. Render the current value alongside
//     the catalog entries so the picker is honest about what's running.
//   - Normal: a real <select> bound to the catalog.
function ModelControl({ current, models, swapping, error, onChange }: ModelControlProps) {
  if (models.length === 0) {
    if (!current) return null;
    return (
      <span className="model-display" title={current}>
        {current}
      </span>
    );
  }
  const options = models.includes(current) || !current ? models : [current, ...models];
  return (
    <span className={`model-control${swapping ? " swapping" : ""}`}>
      <select
        className="model-select"
        value={current}
        disabled={swapping}
        onChange={(e) => onChange(e.target.value)}
        title={error ? `Last switch failed: ${error}` : "Switch model for this session"}
      >
        {options.map((m) => (
          <option key={m} value={m}>
            {m}
          </option>
        ))}
      </select>
    </span>
  );
}

function FilterButton({
  current,
  value,
  onSet,
  children,
}: {
  current: TranscriptFilter;
  value: TranscriptFilter;
  onSet: (v: TranscriptFilter) => void;
  children: React.ReactNode;
}) {
  return (
    <button
      type="button"
      className={`filter-btn ${current === value ? "active" : ""}`}
      onClick={() => onSet(value)}
    >
      {children}
    </button>
  );
}
