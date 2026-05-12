-- Mirror SDK JSONL conversation records into SQLite so the daemon can serve
-- session history without needing the shim alive. The shim tails its own
-- JSONL after each turn and emits one runtime.message_record frame per line;
-- the actor inserts here with a per-session monotonic seq. record_uuid (when
-- present in the SDK record) carries the dedup key so resends after a shim
-- reconnect are no-ops.
CREATE TABLE messages (
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    seq          INTEGER NOT NULL,
    received_at  TEXT NOT NULL,
    record_uuid  TEXT,
    record_json  TEXT NOT NULL,
    PRIMARY KEY (session_id, seq)
);
CREATE UNIQUE INDEX idx_messages_session_uuid
    ON messages(session_id, record_uuid)
    WHERE record_uuid IS NOT NULL;

INSERT INTO schema_version VALUES (2);
PRAGMA user_version = 2;
