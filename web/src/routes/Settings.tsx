import { useEffect, useState } from "react";
import { ApiError, apiJson, jsonBody } from "../api";
import type { AddMcpRequest, McpEntry, UpdateMcpRequest } from "../types";

interface FormState {
  mode: "add" | "edit";
  name: string;
  url: string;
  transport: string;
  kind: string;
  default_enabled: boolean;
  description: string;
  auth_config_json: string;
}

const EMPTY_FORM: FormState = {
  mode: "add",
  name: "",
  url: "",
  transport: "http",
  kind: "none",
  default_enabled: true,
  description: "",
  auth_config_json: "",
};

export function Settings() {
  const [mcps, setMcps] = useState<McpEntry[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [form, setForm] = useState<FormState | null>(null);
  const [busy, setBusy] = useState(false);

  async function load() {
    try {
      const r = await apiJson<{ mcps: McpEntry[] }>("/v1/mcps");
      setMcps(r.mcps ?? []);
      setError(null);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    }
  }

  useEffect(() => {
    void load();
  }, []);

  function startAdd() {
    setForm({ ...EMPTY_FORM });
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

  async function onRemove(entry: McpEntry) {
    if (
      !window.confirm(
        `Remove MCP "${entry.name}"? Running sessions are unaffected.`,
      )
    ) {
      return;
    }
    setBusy(true);
    try {
      await apiJson(`/v1/mcps/${encodeURIComponent(entry.name)}`, {
        method: "DELETE",
      });
      await load();
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
    <section>
      <h2>Settings — MCPs</h2>
      <p className="warning">
        Changes apply only to future sessions; running sessions are unaffected.
      </p>
      {error && <div className="error-text">{error}</div>}
      <div className="toolbar">
        <button className="primary" onClick={startAdd} disabled={busy}>
          Add MCP
        </button>
      </div>
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
                    onClick={() => onRemove(m)}
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
              onChange={(e) =>
                setForm({ ...form, name: e.target.value })
              }
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
    </section>
  );
}
