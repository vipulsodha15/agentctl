import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  CreateSessionRequest,
  CreateSessionResponse,
  McpEntry,
  ProvidersResponse,
} from "../types";

const GB = 1024 * 1024 * 1024;

export function NewSession() {
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [reposText, setReposText] = useState("");
  const [providers, setProviders] = useState<ProvidersResponse>({});
  const [provider, setProvider] = useState<string>("");
  const [model, setModel] = useState<string>("");
  const [memGb, setMemGb] = useState<number>(4);
  const [cpuCores, setCpuCores] = useState<number>(2);
  const [mcps, setMcps] = useState<McpEntry[]>([]);
  const [selectedMcps, setSelectedMcps] = useState<Set<string>>(new Set());
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [mcpsError, setMcpsError] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    Promise.all([
      apiJson<{ mcps: McpEntry[] }>("/v1/mcps"),
      apiJson<ProvidersResponse>("/v1/providers").catch(
        () => ({}) as ProvidersResponse,
      ),
    ])
      .then(([r, p]) => {
        if (cancelled) return;
        const list = r.mcps ?? [];
        setMcps(list);
        setSelectedMcps(
          new Set(list.filter((m) => m.default_enabled).map((m) => m.name)),
        );
        const cat = p ?? {};
        setProviders(cat);
        // Preselect the lone enabled provider so the daemon's resolver
        // sees a coherent (provider, model) pair on submit. With 2+
        // enabled we leave provider blank — the resolver picks via
        // workspace.last_used_provider per ADR 0020 §3.
        const enabled = Object.entries(cat)
          .filter(([, v]) => v?.enabled)
          .map(([k]) => k);
        if (enabled.length === 1) {
          setProvider(enabled[0]);
          setModel(cat[enabled[0]]?.default_model ?? "");
        }
      })
      .catch((err) => {
        if (cancelled) return;
        setMcpsError(
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err),
        );
      });
    return () => {
      cancelled = true;
    };
  }, []);

  const enabledProviderIds = useMemo(
    () =>
      Object.entries(providers)
        .filter(([, p]) => p?.enabled)
        .map(([k]) => k)
        .sort(),
    [providers],
  );
  const showProviderSelector = enabledProviderIds.length >= 2;
  const activeProvider = showProviderSelector
    ? provider
    : provider || enabledProviderIds[0] || "";
  const modelOptions = useMemo(() => {
    const base = activeProvider ? providers[activeProvider]?.models ?? [] : [];
    if (model && !base.includes(model)) return [model, ...base];
    return base;
  }, [providers, activeProvider, model]);

  function toggleMcp(name: string) {
    setSelectedMcps((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  }

  async function onSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setSubmitting(true);
    try {
      const repos = reposText
        .split(/\r?\n/)
        .map((s) => s.trim())
        .filter(Boolean);
      const req: CreateSessionRequest = {
        name: name.trim() || `session-${Date.now()}`,
        mcps: Array.from(selectedMcps),
        repos,
        mem_limit_bytes: Math.round(memGb * GB),
        cpu_limit_cores: cpuCores,
      };
      if (provider.trim()) req.provider = provider.trim();
      if (model.trim()) req.model = model.trim();
      const res = await apiJson<CreateSessionResponse>(
        "/v1/sessions",
        { method: "POST", ...jsonBody(req) },
      );
      navigate(`/sessions/${res.session_id}`);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
      setSubmitting(false);
    }
  }

  return (
    <section className="page form-grid">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>New session</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Spin up a fresh container with your repos and tools ready
          </div>
        </div>
      </div>
      <form onSubmit={onSubmit}>
        <div className="panel" style={{ padding: "20px 22px" }}>
          <div className="field">
            <label htmlFor="name">Name</label>
            <input
              id="name"
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="e.g. auth-refactor"
            />
          </div>
          <div className="field">
            <label htmlFor="repos">Repo URLs (one per line)</label>
            <textarea
              id="repos"
              rows={4}
              value={reposText}
              onChange={(e) => setReposText(e.target.value)}
              placeholder="https://github.com/me/foo.git"
            />
          </div>
          {showProviderSelector && (
            <div className="field">
              <label htmlFor="provider">Provider</label>
              <select
                id="provider"
                value={provider}
                onChange={(e) => {
                  const next = e.target.value;
                  setProvider(next);
                  // Reset the model when switching providers: pick the new
                  // provider's default if known, else clear so the user
                  // sees they need to choose. Avoids sending a claude-*
                  // model to OpenAI on submit.
                  if (!next) {
                    setModel("");
                  } else {
                    setModel(providers[next]?.default_model ?? "");
                  }
                }}
              >
                <option value="">Auto (resolver picks)</option>
                {enabledProviderIds.map((p) => (
                  <option key={p} value={p}>
                    {p}
                  </option>
                ))}
              </select>
            </div>
          )}
          <div className="field" style={{ marginBottom: 0 }}>
            <label htmlFor="model">Model</label>
            {modelOptions.length === 0 ? (
              <input
                id="model"
                type="text"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder={
                  showProviderSelector && !provider
                    ? "Pick a provider above"
                    : "Default model"
                }
                disabled={showProviderSelector && !provider}
              />
            ) : (
              <select
                id="model"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                disabled={showProviderSelector && !provider}
              >
                <option value="">
                  {activeProvider
                    ? `Default for ${activeProvider}`
                    : "Default"}
                </option>
                {modelOptions.map((m) => (
                  <option key={m} value={m}>
                    {m}
                  </option>
                ))}
              </select>
            )}
          </div>
        </div>

        <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
          <h3 style={{ marginBottom: 14 }}>MCP servers</h3>
          {mcpsError && <div className="error-text">{mcpsError}</div>}
          {!mcpsError && mcps.length === 0 && (
            <div className="empty">
              No MCPs registered. Add them under Settings.
            </div>
          )}
          {mcps.map((m) => (
            <label key={m.name} className="checkbox-row">
              <input
                type="checkbox"
                checked={selectedMcps.has(m.name)}
                onChange={() => toggleMcp(m.name)}
              />
              <span>
                <strong style={{ fontWeight: 600 }}>{m.name}</strong>{" "}
                <span style={{ color: "var(--c-fg-mute)", fontSize: 12 }}>
                  {m.transport} · {m.kind}
                  {m.description ? ` · ${m.description}` : ""}
                </span>
              </span>
            </label>
          ))}
        </div>

        <div className="panel" style={{ padding: "20px 22px", marginTop: 16 }}>
          <h3 style={{ marginBottom: 14 }}>Resource limits</h3>
          <div style={{ display: "grid", gridTemplateColumns: "1fr 1fr", gap: 16 }}>
            <div className="field" style={{ marginBottom: 0 }}>
              <label htmlFor="mem">Memory (GB)</label>
              <input
                id="mem"
                type="number"
                min={0.5}
                step={0.5}
                value={memGb}
                onChange={(e) => setMemGb(Number(e.target.value))}
              />
            </div>
            <div className="field" style={{ marginBottom: 0 }}>
              <label htmlFor="cpu">CPU (cores)</label>
              <input
                id="cpu"
                type="number"
                min={0.1}
                step={0.1}
                value={cpuCores}
                onChange={(e) => setCpuCores(Number(e.target.value))}
              />
            </div>
          </div>
        </div>

        {error && <div className="error-text" style={{ marginTop: 16 }}>{error}</div>}
        <div className="toolbar" style={{ marginTop: 20 }}>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? "Creating…" : "Create session"}
          </button>
          <button
            type="button"
            onClick={() => navigate("/sessions")}
            disabled={submitting}
          >
            Cancel
          </button>
        </div>
      </form>
    </section>
  );
}
