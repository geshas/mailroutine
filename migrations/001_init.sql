-- MailRoutine initial schema. All timestamps are UTC unix milliseconds.

CREATE TABLE accounts (
    id                      TEXT PRIMARY KEY,
    provider                TEXT NOT NULL DEFAULT 'gmail' CHECK (provider IN ('gmail')),
    email                   TEXT NOT NULL,
    encrypted_refresh_token TEXT NOT NULL,
    connection_status       TEXT NOT NULL DEFAULT 'connected' CHECK (connection_status IN ('connected', 'needs_reauth')),
    created_at              INTEGER NOT NULL,
    updated_at              INTEGER NOT NULL,
    UNIQUE (provider, email)
);

CREATE TABLE routines (
    id                   TEXT PRIMARY KEY,
    account_id           TEXT NOT NULL REFERENCES accounts (id) ON DELETE CASCADE,
    name                 TEXT NOT NULL,
    description          TEXT NOT NULL DEFAULT '',
    gmail_query          TEXT NOT NULL,
    action_type          TEXT NOT NULL CHECK (action_type IN ('archive', 'trash', 'mark_read', 'mark_unread', 'add_label', 'remove_label')),
    action_config        TEXT NOT NULL DEFAULT '{}',
    schedule_type        TEXT NOT NULL CHECK (schedule_type IN ('manual', 'hourly', 'daily', 'weekly', 'monthly')),
    schedule_config      TEXT NOT NULL DEFAULT '{}',
    enabled              INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0, 1)),
    max_messages_per_run INTEGER NOT NULL CHECK (max_messages_per_run BETWEEN 1 AND 10000),
    last_run_at          INTEGER,
    next_run_at          INTEGER,
    -- Execution lease: set while a run is active, refreshed by heartbeats.
    claim_token          TEXT,
    claimed_at           INTEGER,
    created_at           INTEGER NOT NULL,
    updated_at           INTEGER NOT NULL
);
CREATE INDEX idx_routines_due ON routines (enabled, next_run_at);
CREATE INDEX idx_routines_account ON routines (account_id);

CREATE TABLE executions (
    id              TEXT PRIMARY KEY,
    routine_id      TEXT NOT NULL REFERENCES routines (id) ON DELETE CASCADE,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    status          TEXT NOT NULL CHECK (status IN ('running', 'success', 'partial', 'failed', 'cancelled')),
    matched_count   INTEGER NOT NULL DEFAULT 0,
    processed_count INTEGER NOT NULL DEFAULT 0,
    failed_count    INTEGER NOT NULL DEFAULT 0,
    error_message   TEXT NOT NULL DEFAULT '',
    dry_run         INTEGER NOT NULL DEFAULT 0 CHECK (dry_run IN (0, 1)),
    trigger         TEXT NOT NULL DEFAULT 'schedule' CHECK (trigger IN ('schedule', 'manual'))
);
CREATE INDEX idx_executions_routine ON executions (routine_id, started_at DESC);
CREATE INDEX idx_executions_started ON executions (started_at DESC);

CREATE TABLE message_actions (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    execution_id     TEXT NOT NULL REFERENCES executions (id) ON DELETE CASCADE,
    gmail_message_id TEXT NOT NULL,
    action           TEXT NOT NULL,
    status           TEXT NOT NULL CHECK (status IN ('success', 'failed', 'dry_run')),
    error            TEXT NOT NULL DEFAULT '',
    created_at       INTEGER NOT NULL
);
CREATE INDEX idx_message_actions_execution ON message_actions (execution_id, status);
CREATE INDEX idx_message_actions_created ON message_actions (created_at);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- Only a keyed hash of the session token is stored, never the token itself.
CREATE TABLE sessions (
    id                     TEXT PRIMARY KEY,
    token_hash             TEXT NOT NULL UNIQUE,
    created_at             INTEGER NOT NULL,
    last_activity_at       INTEGER NOT NULL,
    expires_at             INTEGER NOT NULL,
    absolute_expires_at    INTEGER, -- NULL when no absolute timeout is configured
    credential_fingerprint TEXT NOT NULL
);
CREATE INDEX idx_sessions_expires ON sessions (expires_at);
