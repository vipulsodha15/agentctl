import { useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useNavigate, useParams, useLocation } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  Agent,
  ListAgentsResponse,
  ListWorkflowsResponse,
  Workflow,
  WorkflowStageDef,
} from "../types";

// Mirrors validateWorkflow / nameRe in internal/ttl/ttl.go.
const NAME_RE = /^[a-z][a-z0-9-]{0,62}$/;
const MAX_STAGES = 16;

type Mode = "new" | "edit";

export function WorkflowEditor() {
  const navigate = useNavigate();
  const params = useParams<{ name?: string }>();
  const location = useLocation();
  const mode: Mode = params.name ? "edit" : "new";

  const [agents, setAgents] = useState<Agent[]>([]);
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [stages, setStages] = useState<WorkflowStageDef[]>([]);
  const [picker, setPicker] = useState<number | null>(null);

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);

  // Cancel-confirm guard when there are unsaved changes.
  const cleanRef = useRef(false);
  useEffect(() => {
    if (!touched) return;
    const handler = (e: BeforeUnloadEvent) => {
      if (cleanRef.current) return;
      e.preventDefault();
      e.returnValue = "";
    };
    window.addEventListener("beforeunload", handler);
    return () => window.removeEventListener("beforeunload", handler);
  }, [touched]);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiJson<ListAgentsResponse>("/v1/agents"),
      apiJson<ListWorkflowsResponse>("/v1/workflows"),
    ])
      .then(([a, w]) => {
        if (cancelled) return;
        setAgents(a.agents ?? []);
        setWorkflows(w.workflows ?? []);
        if (mode === "edit" && params.name) {
          const wf = (w.workflows ?? []).find((x) => x.name === params.name);
          if (!wf) {
            setLoadError(`Workflow "${params.name}" not found.`);
          } else if (wf.source === "builtin") {
            setLoadError(
              `"${wf.name}" is a built-in workflow and cannot be edited. Use Duplicate to make a copy.`,
            );
          } else {
            setName(wf.name);
            setDescription(wf.description);
            setStages(wf.stages.map((s) => ({ agent: s.agent })));
          }
        } else {
          // /workflows/new — optionally seeded by ?from=<workflow> for the
          // "Duplicate" flow from a built-in.
          const seedFrom = new URLSearchParams(location.search).get("from");
          if (seedFrom) {
            const src = (w.workflows ?? []).find((x) => x.name === seedFrom);
            if (src) {
              setDescription(src.description);
              setStages(src.stages.map((s) => ({ agent: s.agent })));
            }
          }
        }
        setLoaded(true);
      })
      .catch((err) => {
        if (cancelled) return;
        setLoadError(toMessage(err));
        setLoaded(true);
      });
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [mode, params.name]);

  const agentMap = useMemo(() => {
    const m: Record<string, Agent> = {};
    for (const a of agents) m[a.name] = a;
    return m;
  }, [agents]);

  const existingNames = useMemo(() => {
    const s = new Set<string>();
    for (const w of workflows) s.add(w.name);
    return s;
  }, [workflows]);

  const nameError = useMemo(() => {
    if (mode === "edit") return null;
    if (!name) return null;
    if (!NAME_RE.test(name)) {
      return "Lowercase letters, digits, and dashes only (must start with a letter).";
    }
    if (existingNames.has(name)) {
      return `A workflow named "${name}" already exists.`;
    }
    return null;
  }, [name, mode, existingNames]);

  const canSubmit =
    !submitting &&
    name.trim().length > 0 &&
    description.trim().length > 0 &&
    stages.length > 0 &&
    stages.length <= MAX_STAGES &&
    !nameError;

  function markDirty() {
    if (!touched) setTouched(true);
  }

  function insertAt(idx: number, agentName: string) {
    setStages((prev) => {
      const next = prev.slice();
      next.splice(idx, 0, { agent: agentName });
      return next;
    });
    setPicker(null);
    markDirty();
  }

  function removeAt(idx: number) {
    setStages((prev) => prev.filter((_, i) => i !== idx));
    markDirty();
  }

  function moveStage(idx: number, dir: -1 | 1) {
    setStages((prev) => {
      const j = idx + dir;
      if (j < 0 || j >= prev.length) return prev;
      const next = prev.slice();
      [next[idx], next[j]] = [next[j], next[idx]];
      return next;
    });
    markDirty();
  }

  async function save() {
    setSubmitError(null);
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const body: Workflow = {
        name: name.trim(),
        description: description.trim(),
        stages: stages.map((s) => ({ agent: s.agent })),
      };
      const path =
        mode === "edit"
          ? `/v1/workflows/${encodeURIComponent(body.name)}`
          : "/v1/workflows";
      const method = mode === "edit" ? "PUT" : "POST";
      await apiJson<Workflow>(path, { method, ...jsonBody(body) });
      cleanRef.current = true;
      navigate("/workflows", { state: { selected: body.name } });
    } catch (err) {
      setSubmitError(toMessage(err));
    } finally {
      setSubmitting(false);
    }
  }

  function cancel() {
    if (touched) {
      const ok = window.confirm("Discard unsaved changes?");
      if (!ok) return;
    }
    cleanRef.current = true;
    navigate("/workflows");
  }

  return (
    <section className="page workflow-editor-page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <button
            type="button"
            className="ghost back-link"
            onClick={cancel}
            aria-label="Back to workflows"
          >
            <span aria-hidden>←</span>
            <span>Workflows</span>
          </button>
          <h2 style={{ marginTop: 4 }}>
            {mode === "edit" ? `Edit "${params.name}"` : "New workflow"}
          </h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Chain role-distinct agents into an ordered pipeline. Each task runs
            one workflow.
          </div>
        </div>
      </div>

      {loadError && <div className="error-text">{loadError}</div>}

      {loaded && !loadError && (
        <>
          <div className="panel workflow-editor-meta">
            <label className="field">
              <span className="field-label">Name</span>
              <span className="field-hint">
                Lowercase letters, digits, and dashes. Used as the workflow
                identifier.
              </span>
              <input
                type="text"
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  markDirty();
                }}
                placeholder="e.g. plan-build-review"
                disabled={mode === "edit"}
                aria-invalid={!!nameError}
                spellCheck={false}
                autoCapitalize="off"
              />
              {nameError && <span className="field-error">{nameError}</span>}
            </label>
            <label className="field">
              <span className="field-label">Description</span>
              <span className="field-hint">
                One-line summary shown alongside the workflow.
              </span>
              <input
                type="text"
                value={description}
                onChange={(e) => {
                  setDescription(e.target.value);
                  markDirty();
                }}
                placeholder="e.g. Plan a change, build it, then review."
              />
            </label>
          </div>

          <div className="panel workflow-canvas-panel">
            <div className="workflow-canvas-head">
              <div>
                <div className="section-label">Stages</div>
                <div className="muted workflow-canvas-hint">
                  Click <span className="kbd-inline">+</span> to add an agent.
                  Stages run in order, left to right.
                </div>
              </div>
              <div className="workflow-canvas-count">
                {stages.length} / {MAX_STAGES}
              </div>
            </div>

            <WorkflowCanvas
              stages={stages}
              agents={agentMap}
              picker={picker}
              setPicker={(i) => setPicker(i)}
              onInsert={insertAt}
              onRemove={removeAt}
              onMove={moveStage}
              agentList={agents}
              maxReached={stages.length >= MAX_STAGES}
            />
          </div>

          {submitError && <div className="error-text">{submitError}</div>}

          <div className="form-actions workflow-editor-actions">
            <button type="button" onClick={cancel} disabled={submitting}>
              Cancel
            </button>
            <button
              type="button"
              className="primary"
              onClick={save}
              disabled={!canSubmit}
            >
              {submitting
                ? "Saving…"
                : mode === "edit"
                  ? "Save changes"
                  : "Create workflow"}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

interface CanvasProps {
  stages: WorkflowStageDef[];
  agents: Record<string, Agent>;
  agentList: Agent[];
  picker: number | null;
  setPicker: (i: number | null) => void;
  onInsert: (i: number, agent: string) => void;
  onRemove: (i: number) => void;
  onMove: (i: number, dir: -1 | 1) => void;
  maxReached: boolean;
}

function WorkflowCanvas({
  stages,
  agents,
  agentList,
  picker,
  setPicker,
  onInsert,
  onRemove,
  onMove,
  maxReached,
}: CanvasProps) {
  const canvasRef = useRef<HTMLDivElement>(null);
  const prevLen = useRef(stages.length);
  useEffect(() => {
    if (stages.length > prevLen.current && canvasRef.current) {
      canvasRef.current.scrollTo({
        left: canvasRef.current.scrollWidth,
        behavior: "smooth",
      });
    }
    prevLen.current = stages.length;
  }, [stages.length]);
  return (
    <div className="workflow-canvas" role="list" ref={canvasRef}>
      {stages.length === 0 ? (
        <AddSlot
          slotIndex={0}
          active={picker === 0}
          onOpen={() => setPicker(0)}
          onClose={() => setPicker(null)}
          onPick={(a) => onInsert(0, a)}
          agentList={agentList}
          variant="empty"
          disabled={maxReached}
        />
      ) : (
        <>
          {stages.map((s, idx) => (
            <div key={`${s.agent}-${idx}`} className="workflow-canvas-row">
              <AddSlot
                slotIndex={idx}
                active={picker === idx}
                onOpen={() => setPicker(idx)}
                onClose={() => setPicker(null)}
                onPick={(a) => onInsert(idx, a)}
                agentList={agentList}
                variant="between"
                disabled={maxReached}
              />
              <StageNode
                index={idx}
                stage={s}
                agent={agents[s.agent]}
                onRemove={() => onRemove(idx)}
                onMoveLeft={idx > 0 ? () => onMove(idx, -1) : undefined}
                onMoveRight={
                  idx < stages.length - 1 ? () => onMove(idx, 1) : undefined
                }
              />
            </div>
          ))}
          <AddSlot
            slotIndex={stages.length}
            active={picker === stages.length}
            onOpen={() => setPicker(stages.length)}
            onClose={() => setPicker(null)}
            onPick={(a) => onInsert(stages.length, a)}
            agentList={agentList}
            variant="trailing"
            disabled={maxReached}
          />
        </>
      )}
    </div>
  );
}

interface StageNodeProps {
  index: number;
  stage: WorkflowStageDef;
  agent: Agent | undefined;
  onRemove: () => void;
  onMoveLeft?: () => void;
  onMoveRight?: () => void;
}

function StageNode({
  index,
  stage,
  agent,
  onRemove,
  onMoveLeft,
  onMoveRight,
}: StageNodeProps) {
  const colour = agent?.colour ?? "slate";
  const missing = !agent;
  return (
    <div
      className={`workflow-node swatch-${colour}${missing ? " missing" : ""}`}
      role="listitem"
    >
      <div className="workflow-node-head">
        <span className="workflow-node-num">{index + 1}</span>
        <span className={`agent-swatch swatch-${colour}`} aria-hidden />
        <div className="workflow-node-name" title={stage.agent}>
          {stage.agent}
        </div>
        <button
          type="button"
          className="workflow-node-remove ghost"
          onClick={onRemove}
          aria-label={`Remove ${stage.agent}`}
          title="Remove"
        >
          <IconX />
        </button>
      </div>
      <div className="workflow-node-desc">
        {missing ? (
          <span className="workflow-node-missing">
            Agent no longer defined.
          </span>
        ) : (
          agent.description
        )}
      </div>
      <div className="workflow-node-foot">
        <button
          type="button"
          className="ghost workflow-node-move"
          onClick={onMoveLeft}
          disabled={!onMoveLeft}
          aria-label="Move stage left"
          title="Move left"
        >
          <IconArrow direction="left" />
        </button>
        <button
          type="button"
          className="ghost workflow-node-move"
          onClick={onMoveRight}
          disabled={!onMoveRight}
          aria-label="Move stage right"
          title="Move right"
        >
          <IconArrow direction="right" />
        </button>
      </div>
    </div>
  );
}

interface AddSlotProps {
  slotIndex: number;
  active: boolean;
  onOpen: () => void;
  onClose: () => void;
  onPick: (agent: string) => void;
  agentList: Agent[];
  variant: "empty" | "between" | "trailing";
  disabled: boolean;
}

function AddSlot({
  slotIndex,
  active,
  onOpen,
  onClose,
  onPick,
  agentList,
  variant,
  disabled,
}: AddSlotProps) {
  const buttonRef = useRef<HTMLButtonElement>(null);
  const label =
    variant === "between" ? `Insert stage at position ${slotIndex + 1}` : "Add stage";
  return (
    <div className={`workflow-slot workflow-slot-${variant}${active ? " active" : ""}`}>
      {variant !== "empty" && <span className="workflow-slot-line" aria-hidden />}
      <button
        ref={buttonRef}
        type="button"
        className={`workflow-slot-button${active ? " active" : ""}`}
        onClick={() => (active ? onClose() : onOpen())}
        aria-label={label}
        aria-expanded={active}
        title={disabled ? "Maximum stages reached" : label}
        disabled={disabled && !active}
      >
        <IconPlus />
      </button>
      {variant !== "empty" && <span className="workflow-slot-line" aria-hidden />}
      {active && (
        <AgentPicker
          agents={agentList}
          onPick={onPick}
          onClose={onClose}
          anchorRef={buttonRef}
        />
      )}
    </div>
  );
}

interface AgentPickerProps {
  agents: Agent[];
  onPick: (name: string) => void;
  onClose: () => void;
  anchorRef: React.RefObject<HTMLElement>;
}

const PICKER_WIDTH = 320;
const PICKER_MAX_HEIGHT = 340;
const PICKER_GAP = 8;
const PICKER_VIEWPORT_PAD = 12;

function AgentPicker({ agents, onPick, onClose, anchorRef }: AgentPickerProps) {
  const [query, setQuery] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const popoverRef = useRef<HTMLDivElement>(null);
  const [activeIdx, setActiveIdx] = useState(0);
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return agents;
    return agents.filter(
      (a) =>
        a.name.toLowerCase().includes(q) ||
        a.description.toLowerCase().includes(q),
    );
  }, [agents, query]);

  // Position the popover under the anchor button, clamped to the viewport.
  useLayoutEffect(() => {
    function recompute() {
      const el = anchorRef.current;
      if (!el) return;
      const rect = el.getBoundingClientRect();
      const vw = window.innerWidth;
      const vh = window.innerHeight;
      const anchorCenter = rect.left + rect.width / 2;
      let left = anchorCenter - PICKER_WIDTH / 2;
      left = Math.max(
        PICKER_VIEWPORT_PAD,
        Math.min(left, vw - PICKER_WIDTH - PICKER_VIEWPORT_PAD),
      );
      let top = rect.bottom + PICKER_GAP;
      if (top + PICKER_MAX_HEIGHT + PICKER_VIEWPORT_PAD > vh) {
        const above = rect.top - PICKER_GAP - PICKER_MAX_HEIGHT;
        if (above >= PICKER_VIEWPORT_PAD) {
          top = above;
        } else {
          top = Math.max(PICKER_VIEWPORT_PAD, vh - PICKER_MAX_HEIGHT - PICKER_VIEWPORT_PAD);
        }
      }
      setPos({ top, left });
    }
    recompute();
    window.addEventListener("resize", recompute);
    window.addEventListener("scroll", recompute, true);
    return () => {
      window.removeEventListener("resize", recompute);
      window.removeEventListener("scroll", recompute, true);
    };
  }, [anchorRef]);

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    setActiveIdx(0);
  }, [query]);

  // Click-outside / Esc close. We ignore clicks on the anchor button so its
  // own toggle handler stays in charge of close-on-second-click.
  useEffect(() => {
    function onDocClick(e: MouseEvent) {
      const target = e.target as Node;
      if (popoverRef.current?.contains(target)) return;
      if (anchorRef.current?.contains(target)) return;
      onClose();
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    }
    document.addEventListener("mousedown", onDocClick);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDocClick);
      document.removeEventListener("keydown", onKey);
    };
  }, [onClose, anchorRef]);

  function onKeyDown(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActiveIdx((i) => Math.min(i + 1, Math.max(filtered.length - 1, 0)));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActiveIdx((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const a = filtered[activeIdx];
      if (a) onPick(a.name);
    }
  }

  if (!pos) return null;

  const popover = (
    <div
      ref={popoverRef}
      className="workflow-picker"
      role="dialog"
      aria-label="Pick an agent"
      style={{
        top: pos.top,
        left: pos.left,
        width: PICKER_WIDTH,
        maxHeight: PICKER_MAX_HEIGHT,
      }}
    >
      <input
        ref={inputRef}
        className="workflow-picker-search"
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Search agents…"
        spellCheck={false}
      />
      <div className="workflow-picker-list">
        {agents.length === 0 ? (
          <div className="workflow-picker-empty muted">
            No agents defined. Add agents under <strong>Agents</strong> first.
          </div>
        ) : filtered.length === 0 ? (
          <div className="workflow-picker-empty muted">No matches.</div>
        ) : (
          filtered.map((a, i) => (
            <button
              key={a.name}
              type="button"
              className={`workflow-picker-item${i === activeIdx ? " active" : ""}`}
              onMouseEnter={() => setActiveIdx(i)}
              onClick={() => onPick(a.name)}
            >
              <span
                className={`agent-swatch swatch-${a.colour ?? "slate"}`}
                aria-hidden
              />
              <span className="workflow-picker-item-body">
                <span className="workflow-picker-item-name">{a.name}</span>
                <span className="workflow-picker-item-desc muted">
                  {a.description}
                </span>
              </span>
            </button>
          ))
        )}
      </div>
    </div>
  );

  return createPortal(popover, document.body);
}

function IconPlus() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="16"
      height="16"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

function IconX() {
  return (
    <svg
      viewBox="0 0 24 24"
      width="14"
      height="14"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M6 6l12 12M18 6L6 18" />
    </svg>
  );
}

function IconArrow({ direction }: { direction: "left" | "right" }) {
  return (
    <svg
      viewBox="0 0 24 24"
      width="14"
      height="14"
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

function toMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.code ?? err.status}: ${err.message}`;
  }
  return String(err);
}
