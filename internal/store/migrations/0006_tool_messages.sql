-- Allow tool calls in the task chat log so tool widgets survive a page
-- refresh. Until now task_messages was text-only and the per-turn tool UI
-- depended entirely on the SDK JSONL mirror, which is flushed at turn end
-- (and only via a live shim mid-turn). Adding 'tool' here means the durable
-- log carries the tool name + input alongside the existing user/assistant
-- rows. The schema is otherwise unchanged.
--
-- SQLite can't ALTER a CHECK constraint in place, so we recreate the table
-- and copy the existing rows over.

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
