-- Tasks and stages — assembly-lines-task-management-architecture.md §4.1.
-- A task is an assembly line + issue + repo; a stage is one agent's run within
-- the task, backed by exactly one session.
CREATE TABLE tasks (
    task_id           TEXT PRIMARY KEY,
    name              TEXT NOT NULL,
    assembly_line_name TEXT,                             -- NULL while not-started
    repo_url          TEXT,
    base_sha          TEXT,
    source_kind       TEXT NOT NULL
                        CHECK (source_kind IN ('github_issue','freeform')),
    source_url        TEXT,
    issue_md          TEXT NOT NULL,
    current_stage_id  TEXT REFERENCES stages(stage_id) DEFERRABLE INITIALLY DEFERRED,
    status            TEXT NOT NULL
                        CHECK (status IN ('not-started','working','done','abandoned')),
    created_at        TEXT NOT NULL,
    started_at        TEXT,
    ended_at          TEXT
);
CREATE INDEX idx_tasks_status_started ON tasks(status, started_at);

CREATE TABLE stages (
    stage_id     TEXT PRIMARY KEY,
    task_id      TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,
    agent_name   TEXT NOT NULL,
    session_id   TEXT REFERENCES sessions(id),
    volume_name  TEXT,
    synthesis    TEXT,
    status       TEXT NOT NULL
                   CHECK (status IN ('pending','active','done')),
    started_at   TEXT,
    ended_at     TEXT,
    UNIQUE(task_id, position)
);
CREATE INDEX idx_stages_task_position ON stages(task_id, position);
CREATE INDEX idx_stages_session ON stages(session_id);

-- Per-task chat history. We persist task-level chat messages independently
-- from session JSONL so the task page can render done-stage messages even
-- after the per-stage container/volume is destroyed.
CREATE TABLE task_messages (
    task_id      TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    stage_id     TEXT REFERENCES stages(stage_id) ON DELETE CASCADE,
    agent_name   TEXT,
    at           TEXT NOT NULL,
    role         TEXT NOT NULL
                   CHECK (role IN ('user','assistant','system','seam','synthesis','error')),
    content      TEXT NOT NULL,
    PRIMARY KEY (task_id, seq)
);
CREATE INDEX idx_task_messages_stage ON task_messages(stage_id);

-- Bookkeeping on existing sessions table so stage-backed sessions can be
-- identified.
ALTER TABLE sessions ADD COLUMN task_id TEXT REFERENCES tasks(task_id);
ALTER TABLE sessions ADD COLUMN stage_id TEXT REFERENCES stages(stage_id);
CREATE INDEX idx_sessions_task_stage ON sessions(task_id, stage_id);

INSERT INTO schema_version VALUES (3);
PRAGMA user_version = 3;
