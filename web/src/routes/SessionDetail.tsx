import { useCallback, useEffect, useMemo, useReducer, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson } from "../api";
import { attach } from "../ws";
import type { SessionStatus, SnapshotData, WireEvent } from "../types";
import { ConversationView, type TranscriptFilter } from "../components/ConversationView";
import { MessageInput } from "../components/MessageInput";
import { McpPanel } from "../components/McpPanel";
import { StopButton } from "../components/StopButton";
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
    apiJson<{ session: { name?: string; status?: SessionStatus } }>(
      `/v1/sessions/${encodeURIComponent(id)}`,
    )
      .then((r) => {
        if (cancelled) return;
        if (r?.session) {
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
          <StopButton sessionId={id} inFlight={state.inFlight} />
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
