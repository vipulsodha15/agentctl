CREATE TABLE schema_version (
    version INTEGER NOT NULL PRIMARY KEY
);
INSERT INTO schema_version VALUES (1);

CREATE TABLE sessions (
    id                    TEXT PRIMARY KEY,
    name                  TEXT NOT NULL,
    status                TEXT NOT NULL
                            CHECK (status IN ('starting','running','stopped','terminated','error')),
    created_at            TEXT NOT NULL,
    last_activity_at      TEXT NOT NULL,
    terminated_at         TEXT,
    container_id          TEXT,
    image_id              TEXT NOT NULL,
    network_id            TEXT,
    volume_path           TEXT,
    control_sock_path     TEXT,
    skills_snapshot_path  TEXT,
    skills_snapshot_hash  TEXT NOT NULL,
    sdk_session_id        TEXT,
    model                 TEXT NOT NULL,
    mem_limit_bytes       INTEGER NOT NULL,
    cpu_limit_cores       REAL NOT NULL,
    mcp_set_json          TEXT NOT NULL,
    mcp_status_json       TEXT,
    repos_json            TEXT NOT NULL,
    session_token         TEXT NOT NULL,
    last_error            TEXT
);
CREATE INDEX idx_sessions_status_activity ON sessions(status, last_activity_at);

CREATE TABLE mcp_registry (
    name              TEXT PRIMARY KEY,
    url               TEXT NOT NULL,
    transport         TEXT NOT NULL,
    kind              TEXT NOT NULL,
    auth_config_json  TEXT,
    default_enabled   INTEGER NOT NULL DEFAULT 0
                        CHECK (default_enabled IN (0,1)),
    description       TEXT,
    created_at        TEXT NOT NULL,
    updated_at        TEXT NOT NULL
);

CREATE TABLE usage (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id           TEXT NOT NULL REFERENCES sessions(id) ON DELETE NO ACTION,
    turn_id              TEXT NOT NULL,
    at                   TEXT NOT NULL,
    model                TEXT NOT NULL,
    input_tokens         INTEGER NOT NULL DEFAULT 0,
    output_tokens        INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens    INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens   INTEGER NOT NULL DEFAULT 0,
    cost_usd             REAL,
    price_table_version  INTEGER,
    UNIQUE(session_id, turn_id)
);
CREATE INDEX idx_usage_session_at ON usage(session_id, at);
CREATE INDEX idx_usage_at ON usage(at);

CREATE TABLE message_idempotency (
    session_id        TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    idempotency_key   TEXT NOT NULL,
    message_id        TEXT NOT NULL,
    accepted_at       TEXT NOT NULL,
    PRIMARY KEY (session_id, idempotency_key)
);
CREATE INDEX idx_idem_accepted ON message_idempotency(accepted_at);

CREATE TABLE session_lifecycle (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id   TEXT NOT NULL REFERENCES sessions(id) ON DELETE CASCADE,
    at           TEXT NOT NULL,
    event        TEXT NOT NULL,
    detail_json  TEXT
);
CREATE INDEX idx_lifecycle_session_at ON session_lifecycle(session_id, at);

PRAGMA user_version = 1;
