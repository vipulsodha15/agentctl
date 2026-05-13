-- Move agent and workflow definitions out of the filesystem and into sqlite
-- so the daemon is stateless-container friendly (no PVC required for the
-- authoring artefacts; the same DB that already holds tasks/sessions/usage
-- is the one source of truth).
--
-- Built-in YAMLs ship inside the binary via go:embed and are upserted into
-- these tables on every agentd boot. Custom YAMLs created through the CLI
-- or web UI also land here.

CREATE TABLE agents (
    name        TEXT PRIMARY KEY,
    source      TEXT NOT NULL
                  CHECK (source IN ('builtin','custom')),
    yaml_body   TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE workflows (
    name        TEXT PRIMARY KEY,
    source      TEXT NOT NULL
                  CHECK (source IN ('builtin','custom')),
    yaml_body   TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

INSERT INTO schema_version VALUES (4);
PRAGMA user_version = 4;
