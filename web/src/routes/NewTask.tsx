import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  Agent,
  CreateTaskRequest,
  ListAgentsResponse,
  ListWorkflowsResponse,
  Task,
  Workflow,
} from "../types";

type AssignMode = "workflow" | "agent";

export function NewTask() {
  const navigate = useNavigate();
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [name, setName] = useState("");
  const [issueMD, setIssueMD] = useState("");
  const [assignMode, setAssignMode] = useState<AssignMode>("workflow");
  const [workflowName, setWorkflowName] = useState<string>("");
  const [agentName, setAgentName] = useState<string>("");
  const [repoURL, setRepoURL] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    apiJson<ListWorkflowsResponse>("/v1/workflows")
      .then((r) => {
        const list = r.workflows ?? [];
        setWorkflows(list);
        if (list.length > 0) {
          setWorkflowName((prev) => prev || list[0].name);
        }
      })
      .catch((err) => setError(String(err)));
    apiJson<ListAgentsResponse>("/v1/agents")
      .then((r) => {
        const list = r.agents ?? [];
        setAgents(list);
        if (list.length > 0) {
          setAgentName((prev) => prev || list[0].name);
        }
      })
      .catch((err) => setError(String(err)));
  }, []);

  async function submit() {
    setError(null);
    if (!issueMD.trim()) {
      setError("Add a task description (at least a sentence).");
      return;
    }
    setSubmitting(true);
    try {
      const req: CreateTaskRequest = {
        name: name.trim() || undefined,
        workflow_name:
          assignMode === "workflow" ? workflowName || undefined : undefined,
        agent_name:
          assignMode === "agent" ? agentName || undefined : undefined,
        repo_url: repoURL.trim() || undefined,
        source_kind: "freeform",
        issue_md: issueMD,
      };
      const task = await apiJson<Task>("/v1/tasks", {
        method: "POST",
        ...jsonBody(req),
      });
      navigate(`/tasks/${task.task_id}`);
    } catch (err) {
      setError(
        err instanceof ApiError
          ? `${err.code ?? err.status}: ${err.message}`
          : String(err),
      );
    } finally {
      setSubmitting(false);
    }
  }

  const selectedWorkflow = workflows.find((w) => w.name === workflowName);
  const selectedAgent = agents.find((a) => a.name === agentName);

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>New task</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Run a multi-stage workflow, or chat with a single agent.
          </div>
        </div>
      </div>
      <div className="task-create-grid">
        <div className="panel task-create-form">
          <label className="field">
            <span className="field-label">Title (optional)</span>
            <input
              type="text"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="Auto-derived from the first line if blank"
            />
          </label>
          <label className="field">
            <span className="field-label">Task description</span>
            <span className="field-hint">
              Drop in the issue body, repro steps, anything the first agent
              should see. Markdown is fine — start with a <code>#&nbsp;Title</code> line.
            </span>
            <textarea
              rows={10}
              value={issueMD}
              onChange={(e) => setIssueMD(e.target.value)}
              placeholder="# Short title…"
            />
          </label>
          <label className="field">
            <span className="field-label">Repo URL (optional)</span>
            <input
              type="text"
              value={repoURL}
              onChange={(e) => setRepoURL(e.target.value)}
              placeholder="https://github.com/your-org/your-repo"
            />
          </label>
          <div className="field">
            <span className="field-label">Assign to</span>
            <div
              className="segmented"
              role="tablist"
              aria-label="Assign task to a workflow or a single agent"
            >
              <button
                type="button"
                role="tab"
                aria-selected={assignMode === "workflow"}
                className={`segmented-btn${assignMode === "workflow" ? " active" : ""}`}
                onClick={() => setAssignMode("workflow")}
              >
                Workflow
              </button>
              <button
                type="button"
                role="tab"
                aria-selected={assignMode === "agent"}
                className={`segmented-btn${assignMode === "agent" ? " active" : ""}`}
                onClick={() => setAssignMode("agent")}
              >
                Single agent
              </button>
            </div>
          </div>
          {assignMode === "workflow" ? (
            <label className="field">
              <span className="field-label">Workflow</span>
              <select
                value={workflowName}
                onChange={(e) => setWorkflowName(e.target.value)}
              >
                <option value="">(none — attach later)</option>
                {workflows.map((w) => (
                  <option key={w.name} value={w.name}>
                    {w.name} — {w.description}
                  </option>
                ))}
              </select>
            </label>
          ) : (
            <label className="field">
              <span className="field-label">Agent</span>
              <select
                value={agentName}
                onChange={(e) => setAgentName(e.target.value)}
              >
                <option value="">(none — attach later)</option>
                {agents.map((a) => (
                  <option key={a.name} value={a.name}>
                    {a.name} — {a.description}
                  </option>
                ))}
              </select>
            </label>
          )}
          {error && <div className="error-text">{error}</div>}
          <div className="form-actions">
            <button
              className="primary"
              onClick={submit}
              disabled={submitting || !issueMD.trim()}
            >
              {submitting ? "Creating…" : "Create task"}
            </button>
            <button onClick={() => navigate("/tasks")} disabled={submitting}>
              Cancel
            </button>
          </div>
        </div>
        <div className="panel task-create-preview">
          <div className="section-label" style={{ marginBottom: 8 }}>
            {assignMode === "workflow" ? "Workflow preview" : "Agent preview"}
          </div>
          {assignMode === "workflow" ? (
            selectedWorkflow ? (
              <WorkflowPreview workflow={selectedWorkflow} />
            ) : (
              <div className="muted">
                No workflow selected. You can attach one later from the task
                page.
              </div>
            )
          ) : selectedAgent ? (
            <AgentPreview agent={selectedAgent} />
          ) : (
            <div className="muted">
              No agent selected. You can attach one later from the task page.
            </div>
          )}
        </div>
      </div>
    </section>
  );
}

function WorkflowPreview({ workflow }: { workflow: Workflow }) {
  return (
    <div>
      <div className="task-preview-name">{workflow.name}</div>
      <div className="muted" style={{ marginBottom: 16 }}>
        {workflow.description}
      </div>
      <div className="task-preview-stages">
        {workflow.stages.map((s, idx) => (
          <div key={idx} className="task-preview-stage">
            <span className="task-preview-step">{idx + 1}</span>
            <span className="task-preview-arrow" aria-hidden>→</span>
            <span className="task-preview-agent">{s.agent}</span>
          </div>
        ))}
      </div>
    </div>
  );
}

function AgentPreview({ agent }: { agent: Agent }) {
  return (
    <div>
      <div className="task-preview-name">
        <span
          className={`agent-swatch swatch-${agent.colour ?? "slate"}`}
          style={{ marginRight: 8, verticalAlign: "middle" }}
        />
        {agent.name}
      </div>
      <div className="muted" style={{ marginBottom: 16 }}>
        {agent.description}
      </div>
      <div className="task-preview-stages">
        <div className="task-preview-stage">
          <span className="task-preview-step">1</span>
          <span className="task-preview-arrow" aria-hidden>→</span>
          <span className="task-preview-agent">{agent.name}</span>
        </div>
      </div>
    </div>
  );
}
