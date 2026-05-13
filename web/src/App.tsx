import { useEffect, useState } from "react";
import { NavLink, Route, Routes, Navigate } from "react-router-dom";
import { hasToken } from "./api";
import { SessionList } from "./routes/SessionList";
import { SessionDetail } from "./routes/SessionDetail";
import { NewSession } from "./routes/NewSession";
import { Settings } from "./routes/Settings";
import { Usage } from "./routes/Usage";
import { TaskList } from "./routes/TaskList";
import { TaskDetail } from "./routes/TaskDetail";
import { NewTask } from "./routes/NewTask";
import { AgentList } from "./routes/AgentList";
import { WorkflowList } from "./routes/WorkflowList";
import { WorkflowEditor } from "./routes/WorkflowEditor";

const SIDEBAR_COLLAPSED_KEY = "agentctl.appSidebar.collapsed";

export function App() {
  const [collapsed, setCollapsed] = useState<boolean>(() => {
    try {
      return localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === "1";
    } catch {
      return false;
    }
  });

  useEffect(() => {
    try {
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, collapsed ? "1" : "0");
    } catch {
      // ignore — storage may be disabled
    }
  }, [collapsed]);

  if (!hasToken()) {
    return (
      <div className="no-token">
        <div
          aria-hidden
          style={{
            width: 36,
            height: 36,
            borderRadius: 10,
            background: "var(--c-fg)",
            color: "var(--c-canvas)",
            display: "grid",
            placeItems: "center",
            fontWeight: 600,
            letterSpacing: "-0.04em",
            fontSize: 17,
            marginBottom: 20,
          }}
        >
          a
        </div>
        <h2>Welcome to agentctl</h2>
        <p>
          This page must be opened via <code>agentctl ui</code> from a terminal
          on this machine. The CLI hands the browser a one-time auth fragment
          that the loader stores in a same-site cookie.
        </p>
        <p>
          If you ran the command and still see this, check the daemon is up
          (<code>agentctl doctor</code>) and re-run <code>agentctl ui</code>.
        </p>
      </div>
    );
  }

  return (
    <div className={`app${collapsed ? " sidebar-collapsed" : ""}`}>
      <aside className={`app-sidebar${collapsed ? " collapsed" : ""}`}>
        <div className="brand">
          <span className="logo" aria-hidden>a</span>
          {!collapsed && <span>agentctl</span>}
          <button
            type="button"
            className="sidebar-toggle"
            onClick={() => setCollapsed((v) => !v)}
            aria-label={collapsed ? "Expand sidebar" : "Collapse sidebar"}
            aria-expanded={!collapsed}
            title={collapsed ? "Expand sidebar" : "Collapse sidebar"}
          >
            <IconChevron direction={collapsed ? "right" : "left"} />
          </button>
        </div>
        {!collapsed && <div className="section-label">Tasks</div>}
        <nav>
          <NavLink to="/tasks" title="Tasks">
            <IconTasks />
            {!collapsed && <span>Tasks</span>}
          </NavLink>
          <NavLink to="/tasks/new" title="New task">
            <IconPlus />
            {!collapsed && <span>New task</span>}
          </NavLink>
          <NavLink to="/agents" title="Agents">
            <IconAgents />
            {!collapsed && <span>Agents</span>}
          </NavLink>
          <NavLink to="/workflows" title="Workflows">
            <IconWorkflows />
            {!collapsed && <span>Workflows</span>}
          </NavLink>
        </nav>
        {!collapsed && <div className="section-label">Workspace</div>}
        <nav>
          <NavLink to="/sessions" end title="Sessions">
            <IconSessions />
            {!collapsed && <span>Sessions</span>}
          </NavLink>
          <NavLink to="/new" title="New session">
            <IconPlus />
            {!collapsed && <span>New session</span>}
          </NavLink>
          <NavLink to="/usage" title="Usage">
            <IconChart />
            {!collapsed && <span>Usage</span>}
          </NavLink>
          <NavLink to="/settings" title="Settings">
            <IconCog />
            {!collapsed && <span>Settings</span>}
          </NavLink>
        </nav>
        {!collapsed && (
          <div className="sidebar-footer">
            <span>Local daemon</span>
            <span>agentctl · v0.1</span>
          </div>
        )}
      </aside>
      <main className="app-main">
        <Routes>
          <Route path="/" element={<Navigate to="/tasks" replace />} />
          <Route path="/tasks" element={<TaskList />} />
          <Route path="/tasks/new" element={<NewTask />} />
          <Route path="/tasks/:id" element={<TaskDetail />} />
          <Route path="/agents" element={<AgentList />} />
          <Route path="/workflows" element={<WorkflowList />} />
          <Route path="/workflows/new" element={<WorkflowEditor />} />
          <Route path="/workflows/:name/edit" element={<WorkflowEditor />} />
          <Route path="/sessions" element={<SessionList />} />
          <Route path="/sessions/:id" element={<SessionDetail />} />
          <Route path="/new" element={<NewSession />} />
          <Route path="/usage" element={<Usage />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/tasks" replace />} />
        </Routes>
      </main>
    </div>
  );
}

function IconSessions() {
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M4 6.5h13M4 12h13M4 17.5h9" />
      <circle cx="19.5" cy="6.5" r="1" fill="currentColor" stroke="none" />
      <circle cx="19.5" cy="12" r="1" fill="currentColor" stroke="none" />
    </svg>
  );
}

function IconPlus() {
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

function IconChart() {
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M4 19V5" />
      <path d="M4 19h16" />
      <path d="M8 15v-3M12 15V8M16 15v-5" />
    </svg>
  );
}

function IconCog() {
  return (
    <svg
      className="nav-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.75"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
    </svg>
  );
}

function IconTasks() {
  return (
    <svg className="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <rect x="3" y="5" width="18" height="14" rx="2.5" />
      <path d="M3 10.5h18" />
      <path d="M7.5 14.5h4" />
    </svg>
  );
}

function IconAgents() {
  return (
    <svg className="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <circle cx="9" cy="8.5" r="3" />
      <path d="M3.5 19c.6-2.9 2.9-4.8 5.5-4.8s4.9 1.9 5.5 4.8" />
      <circle cx="17" cy="9.5" r="2.2" />
      <path d="M14.2 18c.4-2.3 1.8-3.7 3.4-3.7 1.3 0 2.5.9 3.1 2.4" />
    </svg>
  );
}

function IconWorkflows() {
  return (
    <svg className="nav-icon" viewBox="0 0 24 24" fill="none" stroke="currentColor"
      strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round" aria-hidden>
      <circle cx="5.5" cy="6.5" r="2" />
      <circle cx="5.5" cy="17.5" r="2" />
      <circle cx="18.5" cy="12" r="2" />
      <path d="M7.5 6.5h5.5a3.5 3.5 0 0 1 3.5 3.5v.5" />
      <path d="M7.5 17.5h5.5a3.5 3.5 0 0 0 3.5-3.5v-.5" />
    </svg>
  );
}

function IconChevron({ direction }: { direction: "left" | "right" }) {
  return (
    <svg
      className="chevron-icon"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      {direction === "left" ? (
        <polyline points="15 6 9 12 15 18" />
      ) : (
        <polyline points="9 6 15 12 9 18" />
      )}
    </svg>
  );
}
