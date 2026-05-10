import { NavLink, Route, Routes, Navigate } from "react-router-dom";
import { hasToken } from "./api";
import { SessionList } from "./routes/SessionList";
import { SessionDetail } from "./routes/SessionDetail";
import { NewSession } from "./routes/NewSession";
import { Settings } from "./routes/Settings";

export function App() {
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
    <div className="app">
      <header className="app-header">
        <span className="brand">agentctl</span>
        <nav>
          <NavLink to="/sessions" end>
            Sessions
          </NavLink>
          <NavLink to="/new">New session</NavLink>
          <NavLink to="/settings">Settings</NavLink>
        </nav>
      </header>
      <main className="app-main">
        <Routes>
          <Route path="/" element={<Navigate to="/sessions" replace />} />
          <Route path="/sessions" element={<SessionList />} />
          <Route path="/sessions/:id" element={<SessionDetail />} />
          <Route path="/new" element={<NewSession />} />
          <Route path="/settings" element={<Settings />} />
          <Route path="*" element={<Navigate to="/sessions" replace />} />
        </Routes>
      </main>
    </div>
  );
}
