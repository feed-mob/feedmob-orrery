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
	return &Store{db: db, now: time.Now}, nil
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
	res, err := tx.ExecContext(ctx, `
		INSERT INTO runs (repo, workflow_name, workflow_file, event, ref, sha, actor, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 'queued', ?)`,
		run.Repo, run.WorkflowName, run.WorkflowFile, run.Event, run.Ref, run.SHA, run.Actor, now)
	if err != nil {
		return 0, fmt.Errorf("insert run: %w", err)
	}
	runID, _ := res.LastInsertId()

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

	rows, err := tx.QueryContext(ctx, `
		SELECT id, run_id, job_key, name, needs, runs_on, payload, timeout_minutes
		FROM jobs WHERE status = 'queued' ORDER BY id ASC`)
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
