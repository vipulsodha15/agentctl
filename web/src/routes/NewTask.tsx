import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { ApiError, apiJson, jsonBody } from "../api";
import type {
  CreateTaskRequest,
  ListWorkflowsResponse,
  Task,
  Workflow,
} from "../types";

export function NewTask() {
  const navigate = useNavigate();
  const [workflows, setWorkflows] = useState<Workflow[]>([]);
  const [name, setName] = useState("");
  const [issueMD, setIssueMD] = useState("");
  const [workflowName, setWorkflowName] = useState<string>("");
  const [repoURL, setRepoURL] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState(false);

  useEffect(() => {
    apiJson<ListWorkflowsResponse>("/v1/workflows")
      .then((r) => {
        setWorkflows(r.workflows ?? []);
        if ((r.workflows ?? []).length > 0 && !workflowName) {
          setWorkflowName(r.workflows[0].name);
        }
      })
      .catch((err) => setError(String(err)));
  // eslint-disable-next-line react-hooks/exhaustive-deps
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
        workflow_name: workflowName || undefined,
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

  return (
    <section className="page">
      <div className="page-header">
        <div style={{ flex: 1 }}>
          <h2>New task</h2>
          <div className="muted" style={{ marginTop: 4 }}>
            Spin up a multi-stage workflow against a task.
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
            Workflow preview
          </div>
          {selectedWorkflow ? (
            <WorkflowPreview workflow={selectedWorkflow} />
          ) : (
            <div className="muted">
              No workflow selected. You can attach one later from the task
              page.
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
