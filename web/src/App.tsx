import { useEffect, useState } from "react";
import { NavLink, Route, Routes, Navigate } from "react-router-dom";
import { hasToken } from "./api";
import { SessionList } from "./routes/SessionList";
import { SessionDetail } from "./routes/SessionDetail";
import { NewSession } from "./routes/NewSession";
import { Settings } from "./routes/Settings";
import { Usage } from "./routes/Usage";

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
        <h2>agentctl Web UI</h2>
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
          <span className="logo">a</span>
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
            <span>Coding agent</span>
            <span>v0.1 · local daemon</span>
          </div>
        )}
      </aside>
      <main className="app-main">
        <Routes>
          <Route path="/" element={<Navigate to="/sessions" replace />} />
          <Route path="/sessions" element={<SessionList />} />
          <Route path="/sessions/:id" element={<SessionDetail />} />
          <Route path="/new" element={<NewSession />} />
          <Route path="/usage" element={<Usage />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/sessions" replace />} />
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
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="3" y="4" width="18" height="4" rx="1.5" />
      <rect x="3" y="10" width="18" height="4" rx="1.5" />
      <rect x="3" y="16" width="18" height="4" rx="1.5" />
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
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <rect x="3" y="3" width="18" height="18" rx="3" />
      <path d="M12 8v8M8 12h8" />
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
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <path d="M3 3v18h18" />
      <path d="M7 14v3M12 9v8M17 5v12" />
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
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden
    >
      <circle cx="12" cy="12" r="3" />
      <path d="M19 12a7 7 0 0 0-.1-1.2l2-1.6-2-3.4-2.4.9a7 7 0 0 0-2-1.2L14 3h-4l-.5 2.5a7 7 0 0 0-2 1.2l-2.4-.9-2 3.4 2 1.6A7 7 0 0 0 5 12c0 .4 0 .8.1 1.2l-2 1.6 2 3.4 2.4-.9a7 7 0 0 0 2 1.2L10 21h4l.5-2.5a7 7 0 0 0 2-1.2l2.4.9 2-3.4-2-1.6c.1-.4.1-.8.1-1.2z" />
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
