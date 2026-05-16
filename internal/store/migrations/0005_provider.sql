-- ADR 0020 (OpenAI Codex as a second agent provider) — phase 1.
--
-- 1. Add `provider` to `sessions`. Per ADR §1 the provider is a session-set-
--    once dimension; the session manager always writes it at create. The
--    column carries a DEFAULT 'anthropic' so historic rows backfill to the
--    only provider that existed before this migration. New inserts always
--    pass the value explicitly; the DEFAULT exists only for the backfill
--    and for any code path that hasn't been threaded yet (defense in depth).
--
-- 2. Add `workspace_state` — a simple kv table for per-workspace, daemon-
--    persistent state. First user is `last_used_provider`, the sticky-per-
--    workspace tiebreak the resolver consults when multiple providers are
--    enabled and nothing on the call site says which to pick (ADR §3 and
--    §UX principles). Lives in sqlite (not config.toml) because it's
--    behavioural, not configuration: new users have one provider and
--    never see the choice, and the only writer is the session manager
--    itself on each create.

ALTER TABLE sessions ADD COLUMN provider TEXT NOT NULL DEFAULT 'anthropic';

CREATE TABLE workspace_state (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

INSERT INTO schema_version VALUES (5);
PRAGMA user_version = 5;
