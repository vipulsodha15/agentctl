import { useEffect, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { useLocation, useNavigate, useParams } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import { ConfirmModal } from "../components/ConfirmModal";
import type {
  Agent,
  ListAgentsResponse,
  ListSkillsResponse,
  McpEntry,
  ProvidersResponse,
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

type Mode = "new" | "edit";

// Built-in models prefixes can hint provider for legacy agent YAMLs that
// were written before the editor learned to set `provider:` explicitly.
function inferProvider(model: string): string {
  if (model.startsWith("claude-")) return "anthropic";
  if (model.startsWith("gpt-")) return "openai";
  return "";
}

// Display labels for the provider dropdown. The underlying IDs stored in
// agent YAML / API payloads stay as the canonical provider ids.
const PROVIDER_LABEL: Record<string, string> = {
  anthropic: "Claude Code",
  openai: "Codex",
};

function providerLabel(id: string): string {
  return PROVIDER_LABEL[id] ?? id;
}

// pickDefaultProvider chooses an initial provider when the agent doesn't
// already pin one: prefer Claude Code (anthropic) when enabled, otherwise
// fall back to whatever the first enabled provider is.
function pickDefaultProvider(enabled: string[]): string {
  if (enabled.includes("anthropic")) return "anthropic";
  return enabled[0] ?? "";
}

export function AgentEditor() {
  const navigate = useNavigate();
  const params = useParams<{ name?: string }>();
  const location = useLocation();
  const mode: Mode = params.name ? "edit" : "new";

  const [agents, setAgents] = useState<Agent[]>([]);
  const [mcps, setMcps] = useState<McpEntry[]>([]);
  const [skills, setSkills] = useState<SkillEntry[]>([]);
  const [providers, setProviders] = useState<ProvidersResponse>({});
  const [loaded, setLoaded] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);

  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [colour, setColour] = useState<string>("slate");
  const [provider, setProvider] = useState<string>("");
  const [model, setModel] = useState<string>("");
  const [prompt, setPrompt] = useState("");
  const [mcpsAllowed, setMcpsAllowed] = useState<Set<string>>(new Set());
  const [skillsAllowed, setSkillsAllowed] = useState<Set<string>>(new Set());
  const [mcpsConstrained, setMcpsConstrained] = useState(false);
  const [skillsConstrained, setSkillsConstrained] = useState(false);

  const [submitting, setSubmitting] = useState(false);
  const [submitError, setSubmitError] = useState<string | null>(null);
  const [touched, setTouched] = useState(false);
  const [confirmDiscard, setConfirmDiscard] = useState(false);

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
      apiJson<ProvidersResponse>("/v1/providers").catch(() => ({}) as ProvidersResponse),
    ])
      .then(([a, m, s, p]) => {
        if (cancelled) return;
        setAgents(a.agents ?? []);
        setMcps(m.mcps ?? []);
        setSkills(s.skills ?? []);
        const catalog = p ?? {};
        setProviders(catalog);

        const enabledIds = Object.entries(catalog)
          .filter(([, v]) => v?.enabled)
          .map(([k]) => k);
        const fallbackProvider = pickDefaultProvider(enabledIds);
        const fallbackModel = fallbackProvider
          ? catalog[fallbackProvider]?.default_model ?? ""
          : "";

        const seedFrom = new URLSearchParams(location.search).get("from");
        const seedName = mode === "edit" ? params.name : seedFrom;
        let seeded = false;
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
            seeded = true;
            if (mode === "edit") setName(src.name);
            setDescription(src.description);
            setColour(src.colour || "slate");
            const seededModel = src.model || "";
            const seededProvider =
              src.provider || inferProvider(seededModel) || fallbackProvider;
            setProvider(seededProvider);
            setModel(
              seededModel ||
                (seededProvider
                  ? catalog[seededProvider]?.default_model ?? ""
                  : ""),
            );
            setPrompt(src.prompt);
            const am = src.mcps_allowed ?? [];
            const sa = src.skills_allowed ?? [];
            setMcpsAllowed(new Set(am));
            setSkillsAllowed(new Set(sa));
            setMcpsConstrained(am.length > 0);
            setSkillsConstrained(sa.length > 0);
          }
        }
        if (!seeded && fallbackProvider) {
          setProvider(fallbackProvider);
          setModel(fallbackModel);
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

  // The provider dropdown is hidden when there's only one enabled
  // provider — there's nothing to choose. The form still carries a
  // resolved provider value either way (set in the load effect).
  const enabledProviderIds = useMemo(
    () =>
      Object.entries(providers)
        .filter(([, p]) => p?.enabled)
        .map(([k]) => k)
        .sort(),
    [providers],
  );
  const showProviderSelector = enabledProviderIds.length >= 2;
  const activeProvider = provider || enabledProviderIds[0] || "";
  const modelOptions = useMemo(() => {
    const base = activeProvider ? providers[activeProvider]?.models ?? [] : [];
    // Preserve the agent's currently-pinned model in the dropdown even if
    // it's no longer in the catalog (e.g. pricing-table churn) so we don't
    // silently mutate it on save.
    if (model && !base.includes(model)) return [model, ...base];
    return base;
  }, [providers, activeProvider, model]);

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
      if (provider.trim()) body.provider = provider.trim();
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
      setConfirmDiscard(true);
      return;
    }
    cleanRef.current = true;
    navigate("/agents");
  }

  function discardAndLeave() {
    cleanRef.current = true;
    setConfirmDiscard(false);
    navigate("/agents");
  }

  return (
    <section className="page assembly-line-editor-page">
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
          <div className="panel assembly-line-editor-meta">
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

          <div
            className="panel"
            style={{
              padding: "20px 22px",
              marginTop: 16,
              display: "flex",
              flexDirection: "column",
              gap: 18,
            }}
          >
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

            {showProviderSelector && (
              <div className="field">
                <span className="field-label">Provider</span>
                <span className="field-hint">
                  Which runtime this agent uses. Defaults to Claude Code when
                  available.
                </span>
                <select
                  value={provider}
                  onChange={(e) => {
                    const next = e.target.value;
                    setProvider(next);
                    // Switching providers: drop a model pin that doesn't
                    // belong to the new provider, then seed the new
                    // provider's default model so we never submit an
                    // empty model.
                    const catalogModels = providers[next]?.models ?? [];
                    if (!model || !catalogModels.includes(model)) {
                      setModel(providers[next]?.default_model ?? "");
                    }
                    markDirty();
                  }}
                >
                  {enabledProviderIds.map((p) => (
                    <option key={p} value={p}>
                      {providerLabel(p)}
                    </option>
                  ))}
                </select>
              </div>
            )}

            <div className="field">
              <span className="field-label">Model</span>
              <span className="field-hint">
                {activeProvider
                  ? `Model used by ${providerLabel(activeProvider)} for this agent.`
                  : "Pick a provider above to choose a model."}
              </span>
              <select
                value={model}
                onChange={(e) => {
                  setModel(e.target.value);
                  markDirty();
                }}
                disabled={!activeProvider}
              >
                <option value="">
                  {activeProvider
                    ? `Inherit (default for ${providerLabel(activeProvider)})`
                    : "Inherit (default session model)"}
                </option>
                {modelOptions.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            </div>
          </div>

          <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
            <div className="field">
              <span className="field-label">Prompt</span>
              <span className="field-hint">
                System instructions for this agent's role in an assembly line stage.
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
            pickButtonLabel="Pick MCPs…"
            modalTitle="Pick MCPs"
            modalHelp="Tick the MCP servers this agent can use."
            itemNoun="MCP"
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
            onChange={(next) => {
              setMcpsAllowed(next);
              markDirty();
            }}
          />

          <AllowlistPanel
            title="Skills allowed"
            help="Pick which skills this agent can use. Leave unconstrained to allow all installed skills."
            emptyHint="No skills installed."
            pickButtonLabel="Pick skills…"
            modalTitle="Pick skills"
            modalHelp="Tick the skills this agent can use."
            itemNoun="skill"
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
            onChange={(next) => {
              setSkillsAllowed(next);
              markDirty();
            }}
          />

          {submitError && <div className="error-text">{submitError}</div>}

          <div className="form-actions assembly-line-editor-actions">
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
      <ConfirmModal
        open={confirmDiscard}
        title="Discard unsaved changes?"
        message="Your edits will be lost."
        confirmLabel="Discard"
        cancelLabel="Keep editing"
        variant="danger"
        onConfirm={discardAndLeave}
        onCancel={() => setConfirmDiscard(false)}
      />
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
  pickButtonLabel: string;
  modalTitle: string;
  modalHelp: string;
  itemNoun: string;
  constrained: boolean;
  setConstrained: (v: boolean) => void;
  items: AllowlistItem[];
  selected: Set<string>;
  onChange: (next: Set<string>) => void;
}

function AllowlistPanel({
  title,
  help,
  emptyHint,
  pickButtonLabel,
  modalTitle,
  modalHelp,
  itemNoun,
  constrained,
  setConstrained,
  items,
  selected,
  onChange,
}: AllowlistPanelProps) {
  const [modalOpen, setModalOpen] = useState(false);
  const selectedItems = useMemo(
    () => items.filter((it) => selected.has(it.key)),
    [items, selected],
  );
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
        <div className="allowlist-summary">
          <div className="allowlist-chips">
            {selectedItems.length === 0 ? (
              <span className="allowlist-empty muted">
                Nothing selected — this agent will have no {itemNoun}s.
              </span>
            ) : (
              selectedItems.map((it) => (
                <span key={it.key} className="allowlist-chip">
                  <span>{it.label}</span>
                  <button
                    type="button"
                    className="allowlist-chip-remove"
                    onClick={() => {
                      const next = new Set(selected);
                      next.delete(it.key);
                      onChange(next);
                    }}
                    aria-label={`Remove ${it.label}`}
                    title={`Remove ${it.label}`}
                  >
                    ×
                  </button>
                </span>
              ))
            )}
          </div>
          <button
            type="button"
            className="ghost allowlist-edit-button"
            onClick={() => setModalOpen(true)}
          >
            {pickButtonLabel}
            {selectedItems.length > 0 && (
              <span className="allowlist-edit-count">
                {selectedItems.length}/{items.length}
              </span>
            )}
          </button>
        </div>
      )}
      {modalOpen && (
        <AllowlistModal
          title={modalTitle}
          help={modalHelp}
          itemNoun={itemNoun}
          items={items}
          initialSelected={selected}
          onSave={(next) => {
            onChange(next);
            setModalOpen(false);
          }}
          onClose={() => setModalOpen(false)}
        />
      )}
    </div>
  );
}

interface AllowlistModalProps {
  title: string;
  help: string;
  itemNoun: string;
  items: AllowlistItem[];
  initialSelected: Set<string>;
  onSave: (next: Set<string>) => void;
  onClose: () => void;
}

function AllowlistModal({
  title,
  help,
  itemNoun,
  items,
  initialSelected,
  onSave,
  onClose,
}: AllowlistModalProps) {
  const [query, setQuery] = useState("");
  const [draft, setDraft] = useState<Set<string>>(() => new Set(initialSelected));
  const searchRef = useRef<HTMLInputElement>(null);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return items;
    return items.filter(
      (it) =>
        it.label.toLowerCase().includes(q) ||
        (it.sub ? it.sub.toLowerCase().includes(q) : false),
    );
  }, [items, query]);

  useEffect(() => {
    searchRef.current?.focus();
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.stopPropagation();
        onClose();
      }
    }
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [onClose]);

  function toggle(key: string) {
    setDraft((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  function selectAll() {
    setDraft(new Set(items.map((it) => it.key)));
  }
  function clearAll() {
    setDraft(new Set());
  }

  const allSelected = draft.size === items.length && items.length > 0;

  return createPortal(
    <div className="modal-scrim" onClick={onClose}>
      <div
        className="modal allowlist-modal"
        onClick={(e) => e.stopPropagation()}
        role="dialog"
        aria-label={title}
      >
        <div className="allowlist-modal-head">
          <h3>{title}</h3>
          <p className="muted allowlist-modal-help">{help}</p>
        </div>
        <input
          ref={searchRef}
          className="allowlist-modal-search"
          type="text"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          placeholder={`Search ${itemNoun}s…`}
          spellCheck={false}
        />
        <div className="allowlist-modal-list">
          {filtered.length === 0 ? (
            <div className="allowlist-modal-empty muted">No matches.</div>
          ) : (
            filtered.map((it) => {
              const on = draft.has(it.key);
              return (
                <button
                  key={it.key}
                  type="button"
                  className={`allowlist-card${on ? " selected" : ""}`}
                  onClick={() => toggle(it.key)}
                  aria-pressed={on}
                >
                  <span className="allowlist-card-check" aria-hidden>
                    {on ? "✓" : ""}
                  </span>
                  <span className="allowlist-card-body">
                    <span className="allowlist-card-name">{it.label}</span>
                    {it.sub && (
                      <span className="allowlist-card-desc">{it.sub}</span>
                    )}
                  </span>
                </button>
              );
            })
          )}
        </div>
        <div className="allowlist-modal-foot">
          <div className="allowlist-modal-bulk">
            <span className="muted">
              {draft.size} of {items.length} selected
            </span>
            <button
              type="button"
              className="ghost allowlist-bulk-link"
              onClick={allSelected ? clearAll : selectAll}
            >
              {allSelected ? "Clear all" : "Select all"}
            </button>
          </div>
          <div className="form-actions" style={{ margin: 0 }}>
            <button type="button" onClick={onClose}>
              Cancel
            </button>
            <button
              type="button"
              className="primary"
              onClick={() => onSave(draft)}
            >
              Done
            </button>
          </div>
        </div>
      </div>
    </div>,
    document.body,
  );
}

function toMessage(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.code ?? err.status}: ${err.message}`;
  }
  return String(err);
}
