-- Persist tool calls and tool results into task_messages so the task chat
-- can render them after a page refresh. Previously tool entries lived only
-- in the per-session SDK JSONL snapshot; that path is unreliable across
-- providers (Codex JSONL records are not in the Anthropic shape the web
-- normalizer understands) and across stage handoffs.
--
-- SQLite can't ALTER a CHECK constraint in place, so we recreate the table.
CREATE TABLE task_messages_new (
    task_id      TEXT NOT NULL REFERENCES tasks(task_id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    stage_id     TEXT REFERENCES stages(stage_id) ON DELETE CASCADE,
    agent_name   TEXT,
    at           TEXT NOT NULL,
    role         TEXT NOT NULL
                   CHECK (role IN ('user','assistant','system','seam','synthesis','error','tool')),
    content      TEXT NOT NULL,
    PRIMARY KEY (task_id, seq)
);
INSERT INTO task_messages_new (task_id, seq, stage_id, agent_name, at, role, content)
    SELECT task_id, seq, stage_id, agent_name, at, role, content FROM task_messages;
DROP TABLE task_messages;
ALTER TABLE task_messages_new RENAME TO task_messages;
CREATE INDEX idx_task_messages_stage ON task_messages(stage_id);

INSERT INTO schema_version VALUES (6);
PRAGMA user_version = 6;
