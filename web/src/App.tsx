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
        <div
          aria-hidden
          style={{
            width: 42,
            height: 42,
            borderRadius: 12,
            background:
              "linear-gradient(135deg, #5b5fef 0%, #7c3aed 50%, #0a0e1a 100%)",
            color: "#fff",
            display: "grid",
            placeItems: "center",
            fontWeight: 700,
            letterSpacing: "-0.04em",
            fontSize: 18,
            marginBottom: 18,
            boxShadow:
              "inset 0 1px 0 rgba(255,255,255,0.2), 0 6px 16px -4px rgba(91,95,239,0.35)",
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
