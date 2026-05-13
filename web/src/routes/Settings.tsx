import { useEffect, useState } from "react";
import { ApiError, apiJson, jsonBody } from "../api";
import { ConfirmModal } from "../components/ConfirmModal";
import type {
  AddMcpRequest,
  AddSkillRequest,
  McpEntry,
  SkillEntry,
  UpdateMcpRequest,
} from "../types";

export function Settings() {
  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Settings</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            MCP servers and custom skills available to your sessions
          </div>
        </div>
      </div>
      <div className="warning" style={{ marginBottom: 24 }}>
        <svg
          width="15"
          height="15"
          viewBox="0 0 24 24"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.8"
          strokeLinecap="round"
          strokeLinejoin="round"
          style={{ flex: "0 0 auto" }}
          aria-hidden
        >
          <circle cx="12" cy="12" r="9" />
          <path d="M12 8v5M12 16.5h.01" />
        </svg>
        <span>
          Changes apply only to future sessions; running sessions are unaffected.
        </span>
      </div>
      <McpSection />
      <SkillsSection />
    </section>
  );
}

interface McpFormState {
  mode: "add" | "edit";
  name: string;
  url: string;
  transport: string;
  kind: string;
  default_enabled: boolean;
  description: string;
  auth_config_json: string;
}

const EMPTY_MCP_FORM: McpFormState = {
  mode: "add",
  name: "",
  url: "",
  transport: "http",
  kind: "none",
  default_enabled: true,
  description: "",
  auth_config_json: "",
};

function McpSection() {
  const [mcps, setMcps] = useState<McpEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<McpFormState | null>(null);
  const [busy, setBusy] = useState(false);
  const [pendingRemove, setPendingRemove] = useState<McpEntry | null>(null);

  async function load() {
    try {
      const r = await apiJson<{ mcps: McpEntry[] }>("/v1/mcps");
      setMcps(r.mcps ?? []);
      setError(null);
    } catch (err) {
      setError(formatErr(err));
    }
  }

  useEffect(() => {
    void load();
  }, []);

  function startAdd() {
    setForm({ ...EMPTY_MCP_FORM });
  }

  function startEdit(entry: McpEntry) {
    setForm({
      mode: "edit",
      name: entry.name,
      url: entry.url,
      transport: entry.transport,
      kind: entry.kind,
      default_enabled: entry.default_enabled,
      description: entry.description ?? "",
      auth_config_json:
        entry.auth_config != null ? JSON.stringify(entry.auth_config) : "",
    });
  }

  async function confirmRemove() {
    const entry = pendingRemove;
    if (!entry) return;
    setBusy(true);
    try {
      await apiJson(`/v1/mcps/${encodeURIComponent(entry.name)}`, {
        method: "DELETE",
      });
      await load();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
      setPendingRemove(null);
    }
  }

  async function onSubmitForm(e: React.FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setError(null);
    try {
      let auth_config: unknown = null;
      if (form.auth_config_json.trim() !== "") {
        try {
          auth_config = JSON.parse(form.auth_config_json);
        } catch {
          throw new Error("auth_config must be valid JSON");
        }
      }
      if (form.mode === "add") {
        const req: AddMcpRequest = {
          name: form.name.trim(),
          url: form.url.trim(),
          transport: form.transport.trim() || "http",
          kind: form.kind.trim() || "none",
          auth_config,
          default_enabled: form.default_enabled,
          description: form.description.trim(),
        };
        if (!req.name) throw new Error("name is required");
        if (!req.url) throw new Error("url is required");
        await apiJson("/v1/mcps", { method: "POST", ...jsonBody(req) });
      } else {
        const req: UpdateMcpRequest = {
          url: form.url.trim(),
          transport: form.transport.trim(),
          kind: form.kind.trim(),
          auth_config,
          default_enabled: form.default_enabled,
          description: form.description.trim(),
        };
        await apiJson(`/v1/mcps/${encodeURIComponent(form.name)}`, {
          method: "PATCH",
          ...jsonBody(req),
        });
      }
      setForm(null);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="settings-section">
      <div className="page-header">
        <h3>MCPs</h3>
        <button className="primary" onClick={startAdd} disabled={busy}>
          + Add MCP
        </button>
      </div>
      {error && <div className="error-text">{error}</div>}
      {mcps === null ? (
        <div className="empty">Loading…</div>
      ) : mcps.length === 0 ? (
        <div className="empty">No MCPs registered.</div>
      ) : (
        <table className="session-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>URL</th>
              <th>Transport</th>
              <th>Kind</th>
              <th>Default</th>
              <th>Description</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {mcps.map((m) => (
              <tr key={m.name}>
                <td>
                  <strong>{m.name}</strong>
                </td>
                <td className="id-cell">{m.url}</td>
                <td>{m.transport}</td>
                <td>{m.kind}</td>
                <td>{m.default_enabled ? "yes" : "no"}</td>
                <td>{m.description ?? ""}</td>
                <td style={{ whiteSpace: "nowrap" }}>
                  <button onClick={() => startEdit(m)} disabled={busy}>
                    Edit
                  </button>{" "}
                  <button
                    className="danger"
                    onClick={() => setPendingRemove(m)}
                    disabled={busy}
                  >
                    Remove
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {form && (
        <form
          onSubmit={onSubmitForm}
          className="form-grid"
          style={{ marginTop: 16 }}
        >
          <h3>{form.mode === "add" ? "Add MCP" : `Edit ${form.name}`}</h3>
          <div className="field">
            <label>Name</label>
            <input
              type="text"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              disabled={form.mode === "edit"}
            />
          </div>
          <div className="field">
            <label>URL</label>
            <input
              type="text"
              value={form.url}
              onChange={(e) => setForm({ ...form, url: e.target.value })}
            />
          </div>
          <div className="field">
            <label>Transport</label>
            <input
              type="text"
              list="transport-options"
              value={form.transport}
              onChange={(e) =>
                setForm({ ...form, transport: e.target.value })
              }
            />
            <datalist id="transport-options">
              <option value="http" />
              <option value="sse" />
            </datalist>
          </div>
          <div className="field">
            <label>Kind</label>
            <input
              type="text"
              list="kind-options"
              value={form.kind}
              onChange={(e) => setForm({ ...form, kind: e.target.value })}
            />
            <datalist id="kind-options">
              <option value="none" />
              <option value="github_pat" />
            </datalist>
          </div>
          <div className="field">
            <label>auth_config (JSON, optional)</label>
            <textarea
              rows={3}
              value={form.auth_config_json}
              onChange={(e) =>
                setForm({ ...form, auth_config_json: e.target.value })
              }
              placeholder="{}"
            />
          </div>
          <div className="field">
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={form.default_enabled}
                onChange={(e) =>
                  setForm({ ...form, default_enabled: e.target.checked })
                }
              />
              <span>Default enabled</span>
            </label>
          </div>
          <div className="field">
            <label>Description</label>
            <input
              type="text"
              value={form.description}
              onChange={(e) =>
                setForm({ ...form, description: e.target.value })
              }
            />
          </div>
          <div className="toolbar">
            <button type="submit" className="primary" disabled={busy}>
              {busy ? "Saving…" : form.mode === "add" ? "Add" : "Save"}
            </button>
            <button
              type="button"
              onClick={() => setForm(null)}
              disabled={busy}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
      <ConfirmModal
        open={pendingRemove !== null}
        title={`Remove MCP "${pendingRemove?.name ?? ""}"?`}
        message="Running sessions are unaffected."
        confirmLabel="Remove"
        variant="danger"
        busy={busy}
        onConfirm={confirmRemove}
        onCancel={() => setPendingRemove(null)}
      />
    </div>
  );
}

interface SkillFormState {
  name: string;
  description: string;
  skill_md: string;
  force: boolean;
}

const SKILL_TEMPLATE = `---
name: my-skill
description: What this skill does and when Claude should use it.
---

## Instructions

Step-by-step guidance Claude follows when this skill runs.
`;

const EMPTY_SKILL_FORM: SkillFormState = {
  name: "",
  description: "",
  skill_md: SKILL_TEMPLATE,
  force: false,
};

function SkillsSection() {
  const [skills, setSkills] = useState<SkillEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<SkillFormState | null>(null);
  const [busy, setBusy] = useState(false);
  const [pendingRemove, setPendingRemove] = useState<SkillEntry | null>(null);

  async function load() {
    try {
      const r = await apiJson<{ skills: SkillEntry[] }>("/v1/skills");
      setSkills(r.skills ?? []);
      setError(null);
    } catch (err) {
      setError(formatErr(err));
    }
  }

  useEffect(() => {
    void load();
  }, []);

  function startAdd() {
    setForm({ ...EMPTY_SKILL_FORM });
  }

  async function confirmRemove() {
    const entry = pendingRemove;
    if (!entry) return;
    setBusy(true);
    try {
      await apiJson(`/v1/skills/${encodeURIComponent(entry.name)}`, {
        method: "DELETE",
      });
      await load();
    } catch (err) {
      setError(formatErr(err));
    } finally {
      setBusy(false);
      setPendingRemove(null);
    }
  }

  async function onSubmitForm(e: React.FormEvent) {
    e.preventDefault();
    if (!form) return;
    setBusy(true);
    setError(null);
    try {
      const name = form.name.trim();
      if (!name) throw new Error("name is required");
      const skill_md = form.skill_md.trim();
      const description = form.description.trim();
      if (!skill_md && !description) {
        throw new Error("provide SKILL.md content or a description");
      }
      const req: AddSkillRequest = {
        name,
        description: description || undefined,
        skill_md: skill_md || undefined,
        force: form.force,
      };
      await apiJson("/v1/skills", { method: "POST", ...jsonBody(req) });
      setForm(null);
      await load();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="settings-section" style={{ marginTop: 32 }}>
      <div className="page-header">
        <h3>Skills</h3>
        <button className="primary" onClick={startAdd} disabled={busy}>
          + Add skill
        </button>
      </div>
      {error && <div className="error-text">{error}</div>}
      {skills === null ? (
        <div className="empty">Loading…</div>
      ) : skills.length === 0 ? (
        <div className="empty">No skills installed.</div>
      ) : (
        <table className="session-table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Source</th>
              <th>Description</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {skills.map((s) => (
              <tr key={s.name}>
                <td>
                  <strong>{s.name}</strong>
                  {s.overrides && (
                    <span className="badge" style={{ marginLeft: 6 }}>
                      overrides built-in
                    </span>
                  )}
                </td>
                <td>{s.source ?? "—"}</td>
                <td>{s.description}</td>
                <td style={{ whiteSpace: "nowrap" }}>
                  {s.source === "custom" ? (
                    <button
                      className="danger"
                      onClick={() => setPendingRemove(s)}
                      disabled={busy}
                    >
                      Remove
                    </button>
                  ) : (
                    <span className="muted">read-only</span>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {form && (
        <form
          onSubmit={onSubmitForm}
          className="form-grid"
          style={{ marginTop: 16 }}
        >
          <h3>Add skill</h3>
          <div className="field">
            <label>Name</label>
            <input
              type="text"
              value={form.name}
              onChange={(e) => setForm({ ...form, name: e.target.value })}
              placeholder="my-skill"
            />
          </div>
          <div className="field">
            <label>Description (used only if SKILL.md is empty)</label>
            <input
              type="text"
              value={form.description}
              onChange={(e) =>
                setForm({ ...form, description: e.target.value })
              }
              placeholder="What this skill does and when to use it."
            />
          </div>
          <div className="field">
            <label>
              SKILL.md (full file, with <code>---</code> YAML front matter)
            </label>
            <textarea
              rows={16}
              value={form.skill_md}
              onChange={(e) =>
                setForm({ ...form, skill_md: e.target.value })
              }
              spellCheck={false}
              style={{ fontFamily: "monospace" }}
            />
          </div>
          <div className="field">
            <label className="checkbox-row">
              <input
                type="checkbox"
                checked={form.force}
                onChange={(e) =>
                  setForm({ ...form, force: e.target.checked })
                }
              />
              <span>Overwrite if a skill with this name already exists</span>
            </label>
          </div>
          <div className="toolbar">
            <button type="submit" className="primary" disabled={busy}>
              {busy ? "Saving…" : "Add"}
            </button>
            <button
              type="button"
              onClick={() => setForm(null)}
              disabled={busy}
            >
              Cancel
            </button>
          </div>
        </form>
      )}
      <ConfirmModal
        open={pendingRemove !== null}
        title={`Remove custom skill "${pendingRemove?.name ?? ""}"?`}
        message="Running sessions are unaffected."
        confirmLabel="Remove"
        variant="danger"
        busy={busy}
        onConfirm={confirmRemove}
        onCancel={() => setPendingRemove(null)}
      />
    </div>
  );
}

function formatErr(err: unknown): string {
  if (err instanceof ApiError) {
    return `${err.code ?? err.status}: ${err.message}`;
  }
  return String(err);
}
