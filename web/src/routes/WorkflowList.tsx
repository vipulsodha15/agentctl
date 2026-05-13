import { useEffect, useState } from "react";
import { ApiError, apiJson } from "../api";
import type {
  Agent,
  ListAgentsResponse,
  ListWorkflowsResponse,
  Workflow,
} from "../types";

export function WorkflowList() {
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [agents, setAgents] = useState<Record<string, Agent>>({});
  const [selected, setSelected] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    Promise.all([
      apiJson<ListWorkflowsResponse>("/v1/workflows"),
      apiJson<ListAgentsResponse>("/v1/agents"),
    ])
      .then(([w, a]) => {
        setWorkflows(w.workflows ?? []);
        const map: Record<string, Agent> = {};
        for (const ag of a.agents ?? []) map[ag.name] = ag;
        setAgents(map);
        if ((w.workflows ?? []).length > 0) {
          setSelected(w.workflows[0].name);
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

  const wf = workflows.find((w) => w.name === selected);

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>Workflows</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Ordered chains of role-distinct agents. Each task runs one workflow.
          </div>
        </div>
      </div>
      {error && <div className="error-text">{error}</div>}
      <div className="two-pane">
        <div className="two-pane-list">
          {workflows.map((w) => (
            <button
              key={w.name}
              className={`list-item${w.name === selected ? " active" : ""}`}
              onClick={() => setSelected(w.name)}
            >
              <div className="list-item-body">
                <div className="list-item-title">{w.name}</div>
                <div className="list-item-sub muted">{w.description}</div>
              </div>
              <div className="workflow-stagecount">
                {w.stages.length} stage{w.stages.length === 1 ? "" : "s"}
              </div>
              {w.source === "builtin" && (
                <span className="badge-builtin">builtin</span>
              )}
            </button>
          ))}
        </div>
        <div className="two-pane-detail panel">
          {wf ? (
            <WorkflowDetail workflow={wf} agents={agents} />
          ) : (
            <div className="empty">Select a workflow to inspect.</div>
          )}
        </div>
      </div>
    </section>
  );
}

function WorkflowDetail({
  workflow,
  agents,
}: {
  workflow: Workflow;
  agents: Record<string, Agent>;
}) {
  return (
    <div>
      <div className="agent-header">
        <div>
          <div className="agent-name">{workflow.name}</div>
          <div className="muted">{workflow.description}</div>
        </div>
        {workflow.source === "builtin" && (
          <span className="badge-builtin">builtin</span>
        )}
      </div>
      <div className="section-label" style={{ marginTop: 24 }}>Stages</div>
      <div className="workflow-stages">
        {workflow.stages.map((s, idx) => {
          const a = agents[s.agent];
          return (
            <div key={idx} className="workflow-stage-card">
              <div className="workflow-stage-head">
                <span className="workflow-stage-num">{idx + 1}</span>
                <span className={`agent-swatch swatch-${a?.colour ?? "slate"}`} />
                <div>
                  <div className="workflow-stage-agent">{s.agent}</div>
                  {a && (
                    <div className="muted workflow-stage-desc">
                      {a.description}
                    </div>
                  )}
                </div>
              </div>
              {idx < workflow.stages.length - 1 && (
                <div className="workflow-stage-connector" aria-hidden />
              )}
            </div>
          );
        })}
      </div>
    </div>
  );
}
