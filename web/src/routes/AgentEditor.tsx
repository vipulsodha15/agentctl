import { useEffect, useMemo, useRef, useState } from "react";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  Agent,
  ListAgentsResponse,
  ListSkillsResponse,
  McpEntry,
  SkillEntry,
} from "../types";

// Mirrors validateAgent / nameRe in internal/ttl/ttl.go.
const NAME_RE = /^[a-z][a-z0-9-]{0,62}$/;

const COLOURS: ReadonlyArray<Agent["colour"]> = [
  "blue",
  "purple",
  "green",
  "amber",
  "red",
  "slate",
];

const MODELS = [
  "claude-opus-4-7",
  "claude-sonnet-4-6",
  "claude-haiku-4-5",
];

type Mode = "new" | "edit";

export function AgentEditor() {
  const navigate = useNavigate();
  const params = useParams<{ name?: string }>();
  const location = useLocation();
  const mode: Mode = params.name ? "edit" : "new";

  const [agents, setAgents] = useState<Agent[]>([]);
  const [mcps, setMcps] = useState<McpEntry[]>([]);
  const [skills, setSkills] = useState<SkillEntry[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [colour, setColour] = useState<string>("slate");
  const [model, setModel] = useState<string>("");
  const [prompt, setPrompt] = useState("");
  const [mcpsAllowed, setMcpsAllowed] = useState<Set<string>>(new Set());
  const [skillsAllowed, setSkillsAllowed] = useState<Set<string>>(new Set());
  const [mcpsConstrained, setMcpsConstrained] = useState(false);
  const [skillsConstrained, setSkillsConstrained] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);

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
      apiJson<{ mcps: McpEntry[] }>("/v1/mcps"),
      apiJson<ListSkillsResponse>("/v1/skills"),
    ])
      .then(([a, m, s]) => {
        if (cancelled) return;
        setAgents(a.agents ?? []);
        setMcps(m.mcps ?? []);
        setSkills(s.skills ?? []);

        const seedFrom = new URLSearchParams(location.search).get("from");
        const seedName = mode === "edit" ? params.name : seedFrom;
        if (seedName) {
          const src = (a.agents ?? []).find((x) => x.name === seedName);
          if (!src) {
            if (mode === "edit") {
              setLoadError(`Agent "${seedName}" not found.`);
            }
          } else if (mode === "edit" && src.source === "builtin") {
            setLoadError(
              `"${src.name}" is a built-in agent and cannot be edited. Use Duplicate to make a copy.`,
            );
          } else {
            if (mode === "edit") setName(src.name);
            setDescription(src.description);
            setColour(src.colour || "slate");
            setModel(src.model || "");
            setPrompt(src.prompt);
            const am = src.mcps_allowed ?? [];
            const sa = src.skills_allowed ?? [];
            setMcpsAllowed(new Set(am));
            setSkillsAllowed(new Set(sa));
            setMcpsConstrained(am.length > 0);
            setSkillsConstrained(sa.length > 0);
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

  const existingNames = useMemo(() => {
    const s = new Set<string>();
    for (const a of agents) s.add(a.name);
    return s;
  }, [agents]);

  const nameError = useMemo(() => {
    if (mode === "edit") return null;
    if (!name) return null;
    if (!NAME_RE.test(name)) {
      return "Lowercase letters, digits, and dashes only (must start with a letter).";
    }
    if (existingNames.has(name)) {
      return `An agent named "${name}" already exists.`;
    }
    return null;
  }, [name, mode, existingNames]);

  const canSubmit =
    !submitting &&
    name.trim().length > 0 &&
    description.trim().length > 0 &&
    prompt.trim().length > 0 &&
    !nameError;

  function markDirty() {
    if (!touched) setTouched(true);
  }

  function toggleSet(
    set: Set<string>,
    setter: (next: Set<string>) => void,
    name: string,
  ) {
    const next = new Set(set);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    setter(next);
    markDirty();
  }

  async function save() {
    setSubmitError(null);
    if (!canSubmit) return;
    setSubmitting(true);
    try {
      const body: Agent = {
        name: name.trim(),
        description: description.trim(),
        colour,
        prompt,
      };
      if (model.trim()) body.model = model.trim();
      if (mcpsConstrained) {
        body.mcps_allowed = Array.from(mcpsAllowed);
      }
      if (skillsConstrained) {
        body.skills_allowed = Array.from(skillsAllowed);
      }
      const path =
        mode === "edit"
          ? `/v1/agents/${encodeURIComponent(body.name)}`
          : "/v1/agents";
      const method = mode === "edit" ? "PUT" : "POST";
      await apiJson<Agent>(path, { method, ...jsonBody(body) });
      cleanRef.current = true;
      navigate("/agents", { state: { selected: body.name } });
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
    navigate("/agents");
  }

  return (
    <section className="page workflow-editor-page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <button
            type="button"
            className="ghost back-link"
            onClick={cancel}
            aria-label="Back to agents"
          >
            <span aria-hidden>←</span>
            <span>Agents</span>
          </button>
          <h2 style={{ marginTop: 4 }}>
            {mode === "edit" ? `Edit "${params.name}"` : "New agent"}
          </h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Reusable session template: prompt, MCPs, skills, model, colour.
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
                Lowercase letters, digits, and dashes. Used as the agent
                identifier.
              </span>
              <input
                type="text"
                value={name}
                onChange={(e) => {
                  setName(e.target.value);
                  markDirty();
                }}
                placeholder="e.g. bug-investigator"
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
                One-line summary shown alongside the agent.
              </span>
              <input
                type="text"
                value={description}
                onChange={(e) => {
                  setDescription(e.target.value);
                  markDirty();
                }}
                placeholder="e.g. Investigates production bugs and proposes fixes."
              />
            </label>
          </div>

          <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
            <div className="field">
              <span className="field-label">Colour</span>
              <span className="field-hint">
                Identity badge in pickers and stage nodes.
              </span>
              <div className="agent-colour-row">
                {COLOURS.map((c) => (
                  <button
                    key={c}
                    type="button"
                    className={`agent-colour-swatch swatch-${c}${
                      colour === c ? " active" : ""
                    }`}
                    onClick={() => {
                      setColour(c);
                      markDirty();
                    }}
                    aria-label={c}
                    aria-pressed={colour === c}
                    title={c}
                  >
                    <span className={`agent-swatch swatch-${c}`} aria-hidden />
                  </button>
                ))}
              </div>
            </div>

            <div className="field" style={{ marginBottom: 0 }}>
              <span className="field-label">Model</span>
              <span className="field-hint">
                Leave as "Inherit" to fall back to the default session model.
              </span>
              <select
                value={model}
                onChange={(e) => {
                  setModel(e.target.value);
                  markDirty();
                }}
              >
                <option value="">Inherit (default session model)</option>
                {MODELS.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
            <div className="field" style={{ marginBottom: 0 }}>
              <span className="field-label">Prompt</span>
              <span className="field-hint">
                System instructions for this agent's role in a workflow stage.
                Markdown is allowed.
              </span>
              <textarea
                rows={12}
                value={prompt}
                onChange={(e) => {
                  setPrompt(e.target.value);
                  markDirty();
                }}
                placeholder="You are a careful reviewer. Read the synthesis from the previous stage and..."
                spellCheck={false}
              />
            </div>
          </div>

          <AllowlistPanel
            title="MCPs allowed"
            help="Pick which MCP servers this agent can use. Leave unconstrained to allow all registered MCPs."
            emptyHint="No MCPs registered. Add them under Settings."
            constrained={mcpsConstrained}
            setConstrained={(v) => {
              setMcpsConstrained(v);
              markDirty();
            }}
            items={mcps.map((m) => ({
              key: m.name,
              label: m.name,
              sub: `${m.transport} · ${m.kind}${m.description ? ` · ${m.description}` : ""}`,
            }))}
            selected={mcpsAllowed}
            onToggle={(n) => toggleSet(mcpsAllowed, setMcpsAllowed, n)}
          />

          <AllowlistPanel
            title="Skills allowed"
            help="Pick which skills this agent can use. Leave unconstrained to allow all installed skills."
            emptyHint="No skills installed."
            constrained={skillsConstrained}
            setConstrained={(v) => {
              setSkillsConstrained(v);
              markDirty();
            }}
            items={skills.map((s) => ({
              key: s.name,
              label: s.name,
              sub: s.description,
            }))}
            selected={skillsAllowed}
            onToggle={(n) => toggleSet(skillsAllowed, setSkillsAllowed, n)}
          />

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
                  : "Create agent"}
            </button>
          </div>
        </>
      )}
    </section>
  );
}

interface AllowlistItem {
  key: string;
  label: string;
  sub?: string;
}

interface AllowlistPanelProps {
  title: string;
  help: string;
  emptyHint: string;
  constrained: boolean;
  setConstrained: (v: boolean) => void;
  items: AllowlistItem[];
  selected: Set<string>;
  onToggle: (name: string) => void;
}

function AllowlistPanel({
  title,
  help,
  emptyHint,
  constrained,
  setConstrained,
  items,
  selected,
  onToggle,
}: AllowlistPanelProps) {
  return (
    <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
      <div className="field" style={{ marginBottom: 12 }}>
        <span className="field-label">{title}</span>
        <span className="field-hint">{help}</span>
      </div>
      <label className="checkbox-row" style={{ marginBottom: 12 }}>
        <input
          type="checkbox"
          checked={constrained}
          onChange={(e) => setConstrained(e.target.checked)}
        />
        <span>Restrict to a specific allowlist</span>
      </label>
      {!constrained ? (
        <div className="muted">
          All registered entries are allowed for this agent.
        </div>
      ) : items.length === 0 ? (
        <div className="empty">{emptyHint}</div>
      ) : (
        <div>
          {items.map((it) => (
            <label key={it.key} className="checkbox-row">
              <input
                type="checkbox"
                checked={selected.has(it.key)}
                onChange={() => onToggle(it.key)}
              />
              <span>
                <strong style={{ fontWeight: 600 }}>{it.label}</strong>
                {it.sub && (
                  <>
                    {" "}
                    <span style={{ color: "var(--c-fg-mute)", fontSize: 12 }}>
                      {it.sub}
                    </span>
                  </>
                )}
              </span>
            </label>
          ))}
        </div>
      )}
    </div>
  );
}

function toMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.code ?? err.status}: ${err.message}`;
  }
  return String(err);
}
