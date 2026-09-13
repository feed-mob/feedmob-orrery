-- Orrery schema.
--
-- SQLite for P0: one file, no ops. Everything below is plain SQL with no
-- SQLite-only types so the move to Postgres is a driver swap plus s/INTEGER
-- PRIMARY KEY/BIGSERIAL/.
--
-- Two design decisions carry research findings and should not be "simplified"
-- away:
--
--   1. jobs.timeout_minutes is NOT NULL with a default. Tekton, Argo and Dagger
--      all landed on the same shape: defaults belong to the platform, not the
--      pipeline author. Twelve of our thirteen workflows shipped without a
--      timeout because GitHub Actions has no org-level default and forgetting
--      is therefore the norm. Here forgetting is impossible.
--
--   2. Stopping is three columns, not a boolean. stop_requested_at records the
--      ask, stop_acked_at records the runner confirming it wound down, and
--      force_terminated records that we gave up waiting. A stop you cannot
--      confirm is not a stop.

CREATE TABLE IF NOT EXISTS meta (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS runners (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    uuid         TEXT    NOT NULL UNIQUE,
    token_hash   TEXT    NOT NULL,
    name         TEXT    NOT NULL,
    status       TEXT    NOT NULL DEFAULT 'idle',
    version      TEXT    NOT NULL DEFAULT '',
    labels       TEXT    NOT NULL DEFAULT '[]',   -- JSON array
    capabilities TEXT    NOT NULL DEFAULT '[]',   -- JSON array
    ephemeral    INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT    NOT NULL,
    last_seen_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_runners_last_seen ON runners (last_seen_at);

CREATE TABLE IF NOT EXISTS runs (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo          TEXT NOT NULL,
    workflow_name TEXT NOT NULL,
    workflow_file TEXT NOT NULL,
    event         TEXT NOT NULL,
    ref           TEXT NOT NULL DEFAULT '',
    sha           TEXT NOT NULL DEFAULT '',
    actor         TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'queued',  -- queued|running|done
    result        TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL,
    started_at    TEXT,
    stopped_at    TEXT,
    -- The forge event verbatim. It becomes `github.event`, so a workflow can
    -- read fields this server has never heard of, and a run can be replayed.
    event_payload TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_runs_status ON runs (status);

CREATE TABLE IF NOT EXISTS jobs (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    run_id          INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    job_key         TEXT    NOT NULL,              -- the key under `jobs:` in the YAML
    name            TEXT    NOT NULL,
    needs           TEXT    NOT NULL DEFAULT '[]', -- JSON array of job_key
    runs_on         TEXT    NOT NULL DEFAULT '[]', -- JSON array of labels
    payload         TEXT    NOT NULL,              -- expanded YAML for this job alone
    status          TEXT    NOT NULL DEFAULT 'blocked', -- blocked|queued|running|done
    result          TEXT    NOT NULL DEFAULT '',
    timeout_minutes INTEGER NOT NULL DEFAULT 60,
    runner_id       INTEGER REFERENCES runners (id),
    created_at      TEXT    NOT NULL,
    started_at      TEXT,
    stopped_at      TEXT,

    -- Three-state stop. See the note at the top of this file.
    stop_requested_at TEXT,
    stop_requested_by TEXT NOT NULL DEFAULT '',
    stop_reason       TEXT NOT NULL DEFAULT '',  -- human|timeout|budget|upstream_failed
    stop_acked_at     TEXT,
    force_terminated  INTEGER NOT NULL DEFAULT 0,
    cleanup_ran       INTEGER NOT NULL DEFAULT 0,

    UNIQUE (run_id, job_key)
);

CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs (status);
CREATE INDEX IF NOT EXISTS idx_jobs_run ON jobs (run_id);

CREATE TABLE IF NOT EXISTS job_outputs (
    job_id INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    key    TEXT    NOT NULL,
    value  TEXT    NOT NULL,
    PRIMARY KEY (job_id, key)
);

CREATE TABLE IF NOT EXISTS job_steps (
    job_id     INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    step_index INTEGER NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    result     TEXT    NOT NULL DEFAULT '',
    started_at TEXT,
    stopped_at TEXT,
    log_index  INTEGER NOT NULL DEFAULT 0,
    log_length INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (job_id, step_index)
);

-- Logs are one append-only stream per job; steps index into it via
-- job_steps.log_index/log_length. Delivery is defined by ack_index in the
-- runner's reply, not by the HTTP status of the submission.
CREATE TABLE IF NOT EXISTS job_logs (
    job_id  INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    idx     INTEGER NOT NULL,
    ts      TEXT    NOT NULL,
    content TEXT    NOT NULL,
    PRIMARY KEY (job_id, idx)
);

CREATE TABLE IF NOT EXISTS job_log_state (
    job_id    INTEGER PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    ack_index INTEGER NOT NULL DEFAULT 0,
    no_more   INTEGER NOT NULL DEFAULT 0
);

-- Webhook deliveries we have already acted on.
--
-- GitHub redelivers on timeout and on a manual redeliver, and a redelivered
-- push must not build twice. The delivery id is the only stable identity a
-- webhook carries, so it is the idempotency key.
CREATE TABLE IF NOT EXISTS deliveries (
    id          TEXT PRIMARY KEY,
    event       TEXT NOT NULL,
    received_at TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deliveries_received ON deliveries (received_at);
