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
    event_payload TEXT NOT NULL DEFAULT '',
    -- github.run_number: a per-workflow counter, not the primary key. Workflows
    -- put it in image tags and release names, where a number that jumps because
    -- another workflow ran in between is a bug people chase for hours.
    run_number    INTEGER NOT NULL DEFAULT 0,
    -- github.run_attempt. Re-running keeps the run and bumps this, so the
    -- artifacts and outputs of the jobs that passed are still there for the
    -- jobs being re-run — which is the whole point of a partial re-run.
    run_attempt   INTEGER NOT NULL DEFAULT 1,
    -- Runs sharing a group run one at a time. status 'pending' means the run is
    -- waiting for its group, and is the reason ClaimJob joins back to runs:
    -- a pending run's jobs are queued but must not be handed out.
    concurrency_group  TEXT    NOT NULL DEFAULT '',
    cancel_in_progress INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_runs_group ON runs (concurrency_group, status);

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

    -- Which re-run this is. Logs and steps are keyed by it, so re-running a
    -- failed job does not overwrite the evidence of the failure you re-ran —
    -- which is exactly the moment you want to keep looking at it.
    attempt INTEGER NOT NULL DEFAULT 1,

    -- Automatic retry. GitHub has none: a deploy that failed because a registry
    -- timed out waits for a human to press a button. retry_after is when this
    -- job may be handed out again, which is why ClaimJob checks it.
    retry_max     INTEGER NOT NULL DEFAULT 1,
    retry_backoff INTEGER NOT NULL DEFAULT 0,
    retry_on      TEXT    NOT NULL DEFAULT '[]',
    retry_after   TEXT,

    -- `environment:` on the job. Set means this job is a deployment, and its
    -- outcome belongs in the deployments ledger as well as the run's.
    environment      TEXT    NOT NULL DEFAULT '',
    environment_url  TEXT    NOT NULL DEFAULT '',
    auto_rollback    INTEGER NOT NULL DEFAULT 0,
    version_from     TEXT    NOT NULL DEFAULT '',

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
    attempt    INTEGER NOT NULL DEFAULT 1,
    step_index INTEGER NOT NULL,
    name       TEXT    NOT NULL DEFAULT '',
    result     TEXT    NOT NULL DEFAULT '',
    started_at TEXT,
    stopped_at TEXT,
    log_index  INTEGER NOT NULL DEFAULT 0,
    log_length INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (job_id, attempt, step_index)
);

-- Logs are one append-only stream per job; steps index into it via
-- job_steps.log_index/log_length. Delivery is defined by ack_index in the
-- runner's reply, not by the HTTP status of the submission.
CREATE TABLE IF NOT EXISTS job_logs (
    job_id  INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
    attempt INTEGER NOT NULL DEFAULT 1,
    idx     INTEGER NOT NULL,
    ts      TEXT    NOT NULL,
    content TEXT    NOT NULL,
    PRIMARY KEY (job_id, attempt, idx)
);

CREATE TABLE IF NOT EXISTS job_log_state (
    job_id    INTEGER PRIMARY KEY REFERENCES jobs (id) ON DELETE CASCADE,
    ack_index INTEGER NOT NULL DEFAULT 0,
    no_more   INTEGER NOT NULL DEFAULT 0,
    -- Set once this attempt's log hit the size cap, so the notice is written
    -- once rather than on every submission that arrives afterwards.
    truncated INTEGER NOT NULL DEFAULT 0
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

-- `on: schedule` crons, read from each repository's default branch.
--
-- Stored rather than re-read from the forge every tick: a cron that lives only
-- in a workflow file costs one API call per repository per minute, against a
-- rate limit shared with everything else Orrery does.
CREATE TABLE IF NOT EXISTS schedules (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    repo          TEXT NOT NULL,
    workflow_file TEXT NOT NULL,
    ref           TEXT NOT NULL,
    cron          TEXT NOT NULL,
    next_due_at   TEXT NOT NULL,
    last_fired_at TEXT,
    UNIQUE (repo, workflow_file, cron)
);

CREATE INDEX IF NOT EXISTS idx_schedules_due ON schedules (next_due_at);

-- What we last told people about each workflow-and-ref.
--
-- Notification is transition-based: a workflow that has been failing for six
-- hours should have produced one message, not twelve. This table is the memory
-- that makes "changed" answerable.
CREATE TABLE IF NOT EXISTS notify_state (
    scope       TEXT PRIMARY KEY,   -- repo + workflow file + ref
    result      TEXT NOT NULL,
    notified_at TEXT NOT NULL
);

-- Deployments: what went where, when, and whether it worked.
--
-- A run's ledger answers "did job 47 pass". It cannot answer "which version is
-- on production right now", which is the question anyone asks first when
-- something is wrong. GitHub records deployments too and then gives you almost
-- nothing to do with the record; this table is what rollback reads.
--
-- Append-only. A rollback is a new row, not an edit of the one it replaces:
-- "we went back" is itself a thing that happened.
CREATE TABLE IF NOT EXISTS deployments (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    repo        TEXT    NOT NULL,
    environment TEXT    NOT NULL,
    -- What was deployed, as an operator would name it: usually an image tag.
    -- Falls back to the commit when the job reports nothing better.
    version     TEXT    NOT NULL,
    sha         TEXT    NOT NULL DEFAULT '',
    ref         TEXT    NOT NULL DEFAULT '',
    url         TEXT    NOT NULL DEFAULT '',
    run_id      INTEGER NOT NULL REFERENCES runs (id) ON DELETE CASCADE,
    job_id      INTEGER NOT NULL,
    workflow_file TEXT  NOT NULL DEFAULT '',
    actor       TEXT    NOT NULL DEFAULT '',
    result      TEXT    NOT NULL,              -- success|failure|cancelled
    -- Set when this deployment exists because an earlier one was rolled back.
    rolled_back_from INTEGER,
    created_at  TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_deployments_current
    ON deployments (repo, environment, id DESC);
