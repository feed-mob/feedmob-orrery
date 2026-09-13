// Package store is Orrery's persistence layer: runs, jobs, runners, logs.
package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed schema.sql
var schemaSQL string

// ErrNotFound is returned when a lookup finds nothing.
var ErrNotFound = errors.New("not found")

// Store wraps the database. SQLite is single-writer, so writes are serialised
// by keeping MaxOpenConns at 1; reads are cheap enough at our scale that the
// simplicity is worth more than the concurrency.
type Store struct {
	db  *sql.DB
	now func() time.Time
	// gate decides whether a blocked job runs once its upstreams settle. nil
	// means the default: run iff every upstream succeeded.
	gate JobGate
	// maxLogBytes caps what one attempt of one job may store. A job that prints
	// without stopping would otherwise fill the disk and take the control plane
	// with it.
	maxLogBytes int64
}

// Open opens (and migrates) the database at path.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := addColumns(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	if err := rebuildForAttempts(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{db: db, now: time.Now, maxLogBytes: DefaultMaxLogBytes}, nil
}

// DefaultMaxLogBytes is how much one attempt of one job may store. GitHub's own
// limit is in the same order; the number matters less than having one.
const DefaultMaxLogBytes = 64 << 20

// SetMaxLogBytes overrides the cap. Zero or less restores the default.
func (s *Store) SetMaxLogBytes(n int64) {
	if n <= 0 {
		n = DefaultMaxLogBytes
	}
	s.maxLogBytes = n
}

// UseJobGate installs the `if:` policy. Called once at startup, before any run
// exists, so there is nothing to race with.
func (s *Store) UseJobGate(g JobGate) { s.gate = g }

// addColumns brings a database created by an earlier build up to date.
//
// The schema is all CREATE TABLE IF NOT EXISTS, so a table that already exists
// is left exactly as it was — a column added to the schema later would never
// appear in it. These run every open and are no-ops once applied.
func addColumns(db *sql.DB) error {
	for _, stmt := range []string{
		`ALTER TABLE runs ADD COLUMN event_payload TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN run_number INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN concurrency_group TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN cancel_in_progress INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN run_attempt INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE jobs ADD COLUMN attempt INTEGER NOT NULL DEFAULT 1`,
		`ALTER TABLE job_log_state ADD COLUMN truncated INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil && !strings.Contains(err.Error(), "duplicate column name") {
			return fmt.Errorf("%s: %w", stmt, err)
		}
	}
	return nil
}

// rebuildForAttempts widens job_logs and job_steps to be keyed by attempt.
//
// The schema is CREATE TABLE IF NOT EXISTS, and SQLite cannot alter a primary
// key, so a database made by an earlier build would keep the two-column key and
// silently refuse the second attempt's rows. Rebuilding is the only way to make
// a migrated database identical to a fresh one, and two databases with the same
// version and different shapes is the kind of difference that surfaces months
// later as "it works on mine".
func rebuildForAttempts(db *sql.DB) error {
	for _, t := range []struct {
		name, create, columns string
	}{
		{
			name: "job_logs",
			create: `CREATE TABLE job_logs_new (
				job_id  INTEGER NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,
				attempt INTEGER NOT NULL DEFAULT 1,
				idx     INTEGER NOT NULL,
				ts      TEXT    NOT NULL,
				content TEXT    NOT NULL,
				PRIMARY KEY (job_id, attempt, idx)
			)`,
			columns: "job_id, idx, ts, content",
		},
		{
			name: "job_steps",
			create: `CREATE TABLE job_steps_new (
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
			)`,
			columns: "job_id, step_index, name, result, started_at, stopped_at, log_index, log_length",
		},
	} {
		keyed, err := attemptIsInKey(db, t.name)
		if err != nil {
			return err
		}
		if keyed {
			continue
		}
		stmts := []string{
			t.create,
			fmt.Sprintf("INSERT INTO %s_new (%s) SELECT %s FROM %s", t.name, t.columns, t.columns, t.name),
			fmt.Sprintf("DROP TABLE %s", t.name),
			fmt.Sprintf("ALTER TABLE %s_new RENAME TO %s", t.name, t.name),
		}
		for _, stmt := range stmts {
			if _, err := db.Exec(stmt); err != nil {
				return fmt.Errorf("%s: %w", stmt, err)
			}
		}
	}
	return nil
}

// attemptIsInKey reports whether the table's primary key already includes the
// attempt column.
func attemptIsInKey(db *sql.DB, table string) (bool, error) {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notNull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &ctype, &notNull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == "attempt" && pk > 0 {
			return true, nil
		}
	}
	return false, rows.Err()
}

// ClaimDelivery records a webhook delivery and reports whether it is new.
// A repeat returns false, which is how a redelivered push avoids building a
// second time.
func (s *Store) ClaimDelivery(ctx context.Context, id, event string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT OR IGNORE INTO deliveries (id, event, received_at) VALUES (?, ?, ?)`,
		id, event, ts(s.now()))
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (s *Store) Close() error { return s.db.Close() }

// DB exposes the handle for callers that need a one-off query.
func (s *Store) DB() *sql.DB { return s.db }

func ts(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func parseTS(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	return &t
}

func encodeJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func decodeStrings(raw string) []string {
	var out []string
	if raw == "" {
		return nil
	}
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

// ---------------------------------------------------------------- runners --

// Runner is a registered runner as stored.
type Runner struct {
	ID           int64
	UUID         string
	Name         string
	Status       string
	Version      string
	Labels       []string
	Capabilities []string
	Ephemeral    bool
	LastSeenAt   time.Time
}

// Supports reports whether the runner advertised a capability flag. A runner
// that does not advertise "cancelling" can only be force-terminated, and the
// ledger records that its cleanup never ran.
func (r *Runner) Supports(capability string) bool {
	for _, c := range r.Capabilities {
		if c == capability {
			return true
		}
	}
	return false
}

func hashToken(tok string) string {
	sum := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(sum[:])
}

func newSecret() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// RegisterRunner mints a runner identity. The returned token is the only time
// the caller sees it in the clear — we keep a hash, so a leaked database cannot
// impersonate a runner.
func (s *Store) RegisterRunner(ctx context.Context, name, version string, labels, capabilities []string, ephemeral bool) (*Runner, string, error) {
	uuid, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	token, err := newSecret()
	if err != nil {
		return nil, "", err
	}
	now := s.now().UTC()
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO runners (uuid, token_hash, name, status, version, labels, capabilities, ephemeral, created_at, last_seen_at)
		VALUES (?, ?, ?, 'idle', ?, ?, ?, ?, ?, ?)`,
		uuid, hashToken(token), name, version,
		encodeJSON(labels), encodeJSON(capabilities), boolInt(ephemeral), ts(now), ts(now))
	if err != nil {
		return nil, "", fmt.Errorf("insert runner: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Runner{
		ID: id, UUID: uuid, Name: name, Status: "idle", Version: version,
		Labels: labels, Capabilities: capabilities, Ephemeral: ephemeral, LastSeenAt: now,
	}, token, nil
}

// RunnerByToken authenticates a runner and stamps last_seen_at. The stamp is
// what staleness detection reads: a runtime that dies does not announce it, its
// token simply stops being used.
func (s *Store) RunnerByToken(ctx context.Context, token string) (*Runner, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, uuid, name, status, version, labels, capabilities, ephemeral, last_seen_at
		FROM runners WHERE token_hash = ?`, hashToken(token))
	var r Runner
	var labels, caps, seen string
	var eph int
	if err := row.Scan(&r.ID, &r.UUID, &r.Name, &r.Status, &r.Version, &labels, &caps, &eph, &seen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	r.Labels, r.Capabilities, r.Ephemeral = decodeStrings(labels), decodeStrings(caps), eph == 1
	r.LastSeenAt, _ = time.Parse(time.RFC3339Nano, seen)
	if _, err := s.db.ExecContext(ctx, `UPDATE runners SET last_seen_at = ? WHERE id = ?`, ts(s.now()), r.ID); err != nil {
		return nil, err
	}
	return &r, nil
}

// DeclareRunner updates the labels and capabilities a runner advertises.
func (s *Store) DeclareRunner(ctx context.Context, id int64, version string, labels, capabilities []string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE runners SET version = ?, labels = ?, capabilities = ?, last_seen_at = ? WHERE id = ?`,
		version, encodeJSON(labels), encodeJSON(capabilities), ts(s.now()), id)
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ------------------------------------------------------------------- runs --

// NewJob describes one job to create alongside a run.
type NewJob struct {
	Key            string
	Name           string
	Needs          []string
	RunsOn         []string
	Payload        string
	TimeoutMinutes int
}

// Run is a stored workflow run.
type Run struct {
	ID           int64
	Repo         string
	WorkflowName string
	WorkflowFile string
	Event        string
	Ref          string
	SHA          string
	Actor        string
	Status       string
	Result       string
	CreatedAt    time.Time
	// EventPayload is the forge event verbatim, which becomes `github.event`.
	// Storing it rather than a summary is what lets a workflow read fields we
	// have never heard of, and lets a run be replayed later.
	EventPayload string
	// RunNumber is github.run_number: a counter per (repo, workflow file).
	RunNumber int64
	// RunAttempt is github.run_attempt: 1 the first time, bumped by a re-run.
	RunAttempt int64
	// ConcurrencyGroup serialises runs that share it; empty means no limit.
	ConcurrencyGroup string
	// CancelInProgress throws away the run already going instead of queueing
	// behind it.
	CancelInProgress bool
}

// Job is a stored job.
type Job struct {
	ID              int64
	RunID           int64
	Key             string
	Name            string
	Needs           []string
	RunsOn          []string
	Payload         string
	Status          string
	Result          string
	TimeoutMinutes  int
	RunnerID        *int64
	StartedAt       *time.Time
	StoppedAt       *time.Time
	StopRequestedAt *time.Time
	StopReason      string
	StopAckedAt     *time.Time
	ForceTerminated bool
	CleanupRan      bool
	// Attempt is which re-run of this job the current state belongs to.
	Attempt int64
	// Steps is filled by the readers that return a whole run; the dispatch path
	// leaves it nil, because a runner claiming work has no use for it.
	Steps []StepReport
}

// CreateRun stores a run and its jobs in one transaction, then bumps the tasks
// version so idle runners stop long-polling against a stale counter.
//
// Jobs with no unmet `needs` land in 'queued'; the rest wait in 'blocked' until
// their upstreams finish.
func (s *Store) CreateRun(ctx context.Context, run Run, jobs []NewJob) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	now := ts(s.now())
	// Decide the run's own status before inserting it: a run that has to wait
	// for its group starts 'pending', and that is what keeps its jobs from
	// being handed out even though they are queued.
	status, superseded, cancel, err := admit(ctx, tx, run)
	if err != nil {
		return 0, err
	}
	res, err := tx.ExecContext(ctx, `
		INSERT INTO runs (repo, workflow_name, workflow_file, event, ref, sha, actor, status, created_at,
		                  event_payload, run_number, concurrency_group, cancel_in_progress)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		        (SELECT COUNT(*) + 1 FROM runs WHERE repo = ? AND workflow_file = ?), ?, ?)`,
		run.Repo, run.WorkflowName, run.WorkflowFile, run.Event, run.Ref, run.SHA, run.Actor,
		status, now, run.EventPayload, run.Repo, run.WorkflowFile,
		run.ConcurrencyGroup, boolInt(run.CancelInProgress))
	if err != nil {
		return 0, fmt.Errorf("insert run: %w", err)
	}
	runID, _ := res.LastInsertId()

	// A newer arrival supersedes the older waiter rather than forming a queue:
	// by the time it would start, a build of the commit before last is almost
	// never what anyone wanted.
	for _, id := range superseded {
		if err := cancelRun(ctx, tx, id, "concurrency", "superseded", now); err != nil {
			return 0, err
		}
	}
	for _, id := range cancel {
		if err := cancelRun(ctx, tx, id, "concurrency", "cancel_in_progress", now); err != nil {
			return 0, err
		}
	}

	for _, j := range jobs {
		status := "queued"
		if len(j.Needs) > 0 {
			status = "blocked"
		}
		timeout := j.TimeoutMinutes
		if timeout <= 0 {
			// Defaults belong to the platform, not the pipeline author.
			timeout = 60
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO jobs (run_id, job_key, name, needs, runs_on, payload, status, timeout_minutes, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			runID, j.Key, j.Name, encodeJSON(j.Needs), encodeJSON(j.RunsOn), j.Payload, status, timeout, now); err != nil {
			return 0, fmt.Errorf("insert job %s: %w", j.Key, err)
		}
	}
	if err := bumpTasksVersion(ctx, tx); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return runID, nil
}

func bumpTasksVersion(ctx context.Context, tx *sql.Tx) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES ('tasks_version', '1')
		ON CONFLICT (key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT)`)
	return err
}

// TasksVersion returns the current dispatch counter.
func (s *Store) TasksVersion(ctx context.Context) (int64, error) {
	var v sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'tasks_version'`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var n int64
	fmt.Sscanf(v.String, "%d", &n)
	return n, nil
}

// ---------------------------------------------------------------- dispatch --

// ClaimJob hands the oldest queued job whose runs_on labels the runner can
// satisfy to that runner, atomically. Two runners racing for the same job means
// exactly one wins: the UPDATE is guarded on status still being 'queued'.
func (s *Store) ClaimJob(ctx context.Context, r *Runner) (*Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	// The join is the concurrency gate: a run waiting for its group has queued
	// jobs like any other, and they must not be handed out.
	rows, err := tx.QueryContext(ctx, `
		SELECT j.id, j.run_id, j.job_key, j.name, j.needs, j.runs_on, j.payload, j.timeout_minutes
		FROM jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.status = 'queued' AND r.status != 'pending'
		ORDER BY j.id ASC`)
	if err != nil {
		return nil, err
	}
	type cand struct {
		j      Job
		labels []string
	}
	var cands []cand
	for rows.Next() {
		var j Job
		var needs, runsOn string
		if err := rows.Scan(&j.ID, &j.RunID, &j.Key, &j.Name, &needs, &runsOn, &j.Payload, &j.TimeoutMinutes); err != nil {
			rows.Close()
			return nil, err
		}
		j.Needs, j.RunsOn = decodeStrings(needs), decodeStrings(runsOn)
		cands = append(cands, cand{j: j, labels: j.RunsOn})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, c := range cands {
		if !runnerSatisfies(r.Labels, c.labels) {
			continue
		}
		now := ts(s.now())
		res, err := tx.ExecContext(ctx, `
			UPDATE jobs SET status = 'running', runner_id = ?, started_at = ?
			WHERE id = ? AND status = 'queued'`, r.ID, now, c.j.ID)
		if err != nil {
			return nil, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			continue // lost the race; try the next candidate
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE runs SET status = 'running', started_at = COALESCE(started_at, ?) WHERE id = ?`,
			now, c.j.RunID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE runners SET status = 'active' WHERE id = ?`, r.ID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		job := c.j
		job.Status = "running"
		job.RunnerID = &r.ID
		return &job, nil
	}
	return nil, ErrNotFound
}

// runnerSatisfies reports whether a runner carrying have can take a job asking
// for want. An empty want matches anything — a workflow that does not pin
// runs_on runs wherever there is capacity.
func runnerSatisfies(have, want []string) bool {
	if len(want) == 0 {
		return true
	}
	set := make(map[string]struct{}, len(have))
	for _, h := range have {
		set[strings.ToLower(h)] = struct{}{}
	}
	for _, w := range want {
		if _, ok := set[strings.ToLower(w)]; !ok {
			return false
		}
	}
	return true
}

// NeedsContext gathers the outputs and results of a job's upstreams.
func (s *Store) NeedsContext(ctx context.Context, runID int64, needs []string) (map[string]map[string]string, map[string]string, error) {
	outputs := map[string]map[string]string{}
	results := map[string]string{}
	for _, key := range needs {
		var id int64
		var result string
		err := s.db.QueryRowContext(ctx, `SELECT id, result FROM jobs WHERE run_id = ? AND job_key = ?`, runID, key).Scan(&id, &result)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		results[key] = result
		rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM job_outputs WHERE job_id = ?`, id)
		if err != nil {
			return nil, nil, err
		}
		kv := map[string]string{}
		for rows.Next() {
			var k, v string
			if err := rows.Scan(&k, &v); err != nil {
				rows.Close()
				return nil, nil, err
			}
			kv[k] = v
		}
		rows.Close()
		outputs[key] = kv
	}
	return outputs, results, nil
}

// ---------------------------------------------------------- concurrency --

// admit decides how a new run enters its concurrency group, and what that does
// to the runs already in it.
//
// GitHub's rules, which this follows: one run of a group is in flight at a
// time; at most one waits behind it, and a newer arrival supersedes the older
// waiter rather than forming a queue — a build of the commit before last is
// almost never what anyone wanted by the time it would start. With
// cancel-in-progress the newcomer takes over instead of waiting.
//
// It returns the new run's status, the ids of runs superseded while waiting,
// and the ids of runs to cancel because the newcomer is taking over.
func admit(ctx context.Context, tx *sql.Tx, run Run) (status string, superseded, cancel []int64, err error) {
	if run.ConcurrencyGroup == "" {
		return "queued", nil, nil, nil
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT id, status FROM runs
		WHERE concurrency_group = ? AND status IN ('pending', 'queued', 'running')
		ORDER BY id ASC`, run.ConcurrencyGroup)
	if err != nil {
		return "", nil, nil, err
	}
	defer rows.Close()

	var inFlight, waiting []int64
	for rows.Next() {
		var id int64
		var st string
		if err := rows.Scan(&id, &st); err != nil {
			return "", nil, nil, err
		}
		if st == "pending" {
			waiting = append(waiting, id)
		} else {
			inFlight = append(inFlight, id)
		}
	}
	if err := rows.Err(); err != nil {
		return "", nil, nil, err
	}

	if len(inFlight) == 0 {
		// Nothing is going; anything that was waiting is now stale.
		return "queued", waiting, nil, nil
	}
	if run.CancelInProgress {
		return "queued", waiting, inFlight, nil
	}
	return "pending", waiting, nil, nil
}

// cancelRun stops a run and everything of it that has not finished.
//
// A running job is asked to stop rather than killed: the runner acknowledges
// once it has wound down, and the reaper takes it away only if it does not.
// Jobs that never started are simply marked, because there is nothing holding
// anything to wind down.
func cancelRun(ctx context.Context, tx *sql.Tx, runID int64, by, reason, now string) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET stop_requested_at = COALESCE(stop_requested_at, ?), stop_requested_by = ?,
		                stop_reason = CASE WHEN stop_reason = '' THEN ? ELSE stop_reason END
		WHERE run_id = ? AND status = 'running'`, now, by, reason, runID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'done', result = 'cancelled', stopped_at = ?,
		                stop_reason = CASE WHEN stop_reason = '' THEN ? ELSE stop_reason END
		WHERE run_id = ? AND status IN ('queued', 'blocked')`, now, reason, runID); err != nil {
		return err
	}
	// A run with nothing left running settles immediately; one with a job still
	// winding down settles when that job reports.
	var running int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM jobs WHERE run_id = ? AND status = 'running'`, runID).Scan(&running); err != nil {
		return err
	}
	if running > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE runs SET status = 'done', result = 'cancelled', stopped_at = ? WHERE id = ?`, now, runID)
	return err
}

// promoteGroup starts the oldest run waiting on a group, now that the group is
// free. Nothing to do when the group is empty or still busy.
func promoteGroup(ctx context.Context, tx *sql.Tx, group string) error {
	if group == "" {
		return nil
	}
	var busy int
	if err := tx.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM runs WHERE concurrency_group = ? AND status IN ('queued', 'running')`,
		group).Scan(&busy); err != nil {
		return err
	}
	if busy > 0 {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE runs SET status = 'queued'
		WHERE id = (SELECT id FROM runs WHERE concurrency_group = ? AND status = 'pending'
		            ORDER BY id ASC LIMIT 1)`, group)
	return err
}
