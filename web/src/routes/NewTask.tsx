import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  Agent,
  CreateTaskRequest,
  ListAgentsResponse,
  ListAssemblyLinesResponse,
  Task,
  AssemblyLine,
} from "../types";

type AssignMode = "assembly-line" | "agent";

export function NewTask() {
  const navigate = useNavigate();
  const [assemblyLines, setAssemblyLines] = useState<AssemblyLine[]>([]);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [name, setName] = useState("");
  const [issueMD, setIssueMD] = useState("");
  const [assignMode, setAssignMode] = useState<AssignMode>("assembly-line");
  const [assemblyLineName, setAssemblyLineName] = useState<string>("");
  const [agentName, setAgentName] = useState<string>("");
  const [repoURL, setRepoURL] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    apiJson<ListAssemblyLinesResponse>("/v1/assembly-lines")
      .then((r) => {
        const list = r.assembly_lines ?? [];
        setAssemblyLines(list);
        if (list.length > 0) {
          setAssemblyLineName((prev) => prev || list[0].name);
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
        assembly_line_name:
          assignMode === "assembly-line" ? assemblyLineName || undefined : undefined,
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

  const selectedAssemblyLine = assemblyLines.find((w) => w.name === assemblyLineName);
  const selectedAgent = agents.find((a) => a.name === agentName);

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>New task</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Run a multi-stage assembly line, or chat with a single agent.
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
              aria-label="Assign task to an assembly line or a single agent"
            >
              <button
                type="button"
                role="tab"
                aria-selected={assignMode === "assembly-line"}
                className={`segmented-btn${assignMode === "assembly-line" ? " active" : ""}`}
                onClick={() => setAssignMode("assembly-line")}
              >
                Assembly line
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
          {assignMode === "assembly-line" ? (
            <label className="field">
              <span className="field-label">Assembly line</span>
              <select
                value={assemblyLineName}
                onChange={(e) => setAssemblyLineName(e.target.value)}
              >
                <option value="">(none — attach later)</option>
                {assemblyLines.map((w) => (
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
            {assignMode === "assembly-line" ? "Assembly line preview" : "Agent preview"}
          </div>
          {assignMode === "assembly-line" ? (
            selectedAssemblyLine ? (
              <AssemblyLinePreview assemblyLine={selectedAssemblyLine} />
            ) : (
              <div className="muted">
                No assembly line selected. You can attach one later from the
                task page.
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

function AssemblyLinePreview({ assemblyLine }: { assemblyLine: AssemblyLine }) {
  return (
    <div>
      <div className="task-preview-name">{assemblyLine.name}</div>
      <div className="muted" style={{ marginBottom: 16 }}>
        {assemblyLine.description}
      </div>
      <div className="task-preview-stages">
        {assemblyLine.stages.map((s, idx) => (
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
