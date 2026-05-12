import { useEffect, useState } from "react";
import { ApiError, apiJson } from "../api";
import type { Agent, ListAgentsResponse } from "../types";

export function AgentList() {
  const [agents, setAgents] = useState<Agent[]>([]);
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    apiJson<ListAgentsResponse>("/v1/agents")
      .then((r) => {
        setAgents(r.agents ?? []);
        if ((r.agents ?? []).length > 0) {
          setSelected(r.agents[0].name);
        }
      })
      .catch((err) =>
        setError(
          err instanceof ApiError
            ? `${err.code ?? err.status}: ${err.message}`
            : String(err),
        ),
      );
  }, []);

  const agent = agents.find((a) => a.name === selected);

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Agents</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Reusable session templates: prompt, MCPs, skills, model, colour.
          </div>
        </div>
      </div>
      {error && <div className="error-text">{error}</div>}
      <div className="two-pane">
        <div className="two-pane-list">
          {agents.map((a) => (
            <button
              key={a.name}
              className={`list-item${a.name === selected ? " active" : ""}`}
              onClick={() => setSelected(a.name)}
            >
              <span className={`agent-swatch swatch-${a.colour ?? "slate"}`} />
              <div className="list-item-body">
                <div className="list-item-title">{a.name}</div>
                <div className="list-item-sub muted">{a.description}</div>
              </div>
              {a.source === "builtin" && (
                <span className="badge-builtin">builtin</span>
              )}
            </button>
          ))}
        </div>
        <div className="two-pane-detail panel">
          {agent ? <AgentDetail agent={agent} /> : (
            <div className="empty">Select an agent to inspect.</div>
          )}
        </div>
      </div>
    </section>
  );
}

function AgentDetail({ agent }: { agent: Agent }) {
  return (
    <div>
      <div className="agent-header">
        <span className={`agent-swatch large swatch-${agent.colour ?? "slate"}`} />
        <div>
          <div className="agent-name">{agent.name}</div>
          <div className="muted">{agent.description}</div>
        </div>
        {agent.source === "builtin" && (
          <span className="badge-builtin">builtin</span>
        )}
      </div>
      <dl className="detail-grid">
        <div>
          <dt>Colour</dt>
          <dd>{agent.colour ?? "slate"}</dd>
        </div>
        <div>
          <dt>Model</dt>
          <dd>{agent.model || <span className="muted">inherit</span>}</dd>
        </div>
        <div>
          <dt>MCPs allowed</dt>
          <dd>
            {agent.mcps_allowed && agent.mcps_allowed.length > 0 ? (
              <span className="chip-row">
                {agent.mcps_allowed.map((m) => (
                  <span key={m} className="chip">{m}</span>
                ))}
              </span>
            ) : (
              <span className="muted">all (no allowlist)</span>
            )}
          </dd>
        </div>
        <div>
          <dt>Skills allowed</dt>
          <dd>
            {agent.skills_allowed && agent.skills_allowed.length > 0 ? (
              <span className="chip-row">
                {agent.skills_allowed.map((s) => (
                  <span key={s} className="chip">{s}</span>
                ))}
              </span>
            ) : (
              <span className="muted">all (no allowlist)</span>
            )}
          </dd>
        </div>
      </dl>
      <div className="section-label" style={{ marginTop: 24 }}>Prompt</div>
      <pre className="prompt-block">{agent.prompt}</pre>
    </div>
  );
}
