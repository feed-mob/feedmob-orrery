package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// JobByID loads a single job.
func (s *Store) JobByID(ctx context.Context, id int64) (*Job, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, run_id, job_key, name, needs, runs_on, payload, status, result, timeout_minutes,
		       runner_id, started_at, stopped_at, stop_requested_at, stop_reason, stop_acked_at,
		       force_terminated, cleanup_ran
		FROM jobs WHERE id = ?`, id)
	return scanJob(row)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanJob(row rowScanner) (*Job, error) {
	var j Job
	var needs, runsOn string
	var runnerID sql.NullInt64
	var started, stopped, stopReq, stopAck sql.NullString
	var force, cleanup int
	err := row.Scan(&j.ID, &j.RunID, &j.Key, &j.Name, &needs, &runsOn, &j.Payload, &j.Status, &j.Result,
		&j.TimeoutMinutes, &runnerID, &started, &stopped, &stopReq, &j.StopReason, &stopAck, &force, &cleanup)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	j.Needs, j.RunsOn = decodeStrings(needs), decodeStrings(runsOn)
	if runnerID.Valid {
		v := runnerID.Int64
		j.RunnerID = &v
	}
	j.StartedAt, j.StoppedAt = parseTS(started), parseTS(stopped)
	j.StopRequestedAt, j.StopAckedAt = parseTS(stopReq), parseTS(stopAck)
	j.ForceTerminated, j.CleanupRan = force == 1, cleanup == 1
	return &j, nil
}

// SetOutputs merges outputs reported by a runner. The runner only sends what it
// has not sent before, so this is an upsert, never a replace.
func (s *Store) SetOutputs(ctx context.Context, jobID int64, outputs map[string]string) ([]string, error) {
	for k, v := range outputs {
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO job_outputs (job_id, key, value) VALUES (?, ?, ?)
			ON CONFLICT (job_id, key) DO UPDATE SET value = excluded.value`, jobID, k, v); err != nil {
			return nil, err
		}
	}
	rows, err := s.db.QueryContext(ctx, `SELECT key FROM job_outputs WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var sent []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		sent = append(sent, k)
	}
	return sent, rows.Err()
}

// StepReport is one step's state as reported by the runner.
type StepReport struct {
	Index     int
	Name      string
	Result    string
	StartedAt *time.Time
	StoppedAt *time.Time
	LogIndex  int64
	LogLength int64
}

// SetSteps upserts per-step state.
func (s *Store) SetSteps(ctx context.Context, jobID int64, steps []StepReport) error {
	for _, st := range steps {
		var started, stopped any
		if st.StartedAt != nil {
			started = ts(*st.StartedAt)
		}
		if st.StoppedAt != nil {
			stopped = ts(*st.StoppedAt)
		}
		if _, err := s.db.ExecContext(ctx, `
			INSERT INTO job_steps (job_id, attempt, step_index, name, result, started_at, stopped_at,
			                       log_index, log_length)
			VALUES (?, (SELECT attempt FROM jobs WHERE id = ?), ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (job_id, attempt, step_index) DO UPDATE SET
				name = excluded.name, result = excluded.result,
				started_at = excluded.started_at, stopped_at = excluded.stopped_at,
				log_index = excluded.log_index, log_length = excluded.log_length`,
			jobID, jobID, st.Index, st.Name, st.Result, started, stopped, st.LogIndex, st.LogLength); err != nil {
			return err
		}
	}
	return nil
}

// FinishJob records a terminal result and propagates it through the DAG:
// dependents whose needs are now all satisfied move to 'queued'; dependents of a
// failed or cancelled job are cancelled themselves rather than left blocked
// forever.
func (s *Store) FinishJob(ctx context.Context, jobID int64, result string) (*RunOutcome, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	now := ts(s.now())
	var runID int64
	if err := tx.QueryRowContext(ctx, `SELECT run_id FROM jobs WHERE id = ?`, jobID).Scan(&runID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE jobs SET status = 'done', result = ?, stopped_at = COALESCE(stopped_at, ?) WHERE id = ?`,
		result, now, jobID); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runners SET status = 'idle' WHERE id = (SELECT runner_id FROM jobs WHERE id = ?)`, jobID); err != nil {
		return nil, err
	}
	outcome, err := propagate(ctx, tx, runID, now, s.gate)
	if err != nil {
		return nil, err
	}
	if err := bumpTasksVersion(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Only report after the commit: telling the forge a run passed and then
	// rolling the transaction back would leave the two disagreeing, with the
	// forge's copy the one people act on.
	return outcome, nil
}

// RunOutcome is a run that has just settled. FinishJob returns nil when the run
// still has jobs in flight, so a caller can report a verdict exactly once.
type RunOutcome struct {
	RunID  int64
	Result string
}

// propagate walks the run's blocked jobs and moves each one forward once its
// upstreams have settled.
func propagate(ctx context.Context, tx *sql.Tx, runID int64, now string, g JobGate) (*RunOutcome, error) {
	meta, err := runInTx(ctx, tx, runID)
	if err != nil {
		return nil, err
	}
	results := map[string]string{}
	statuses := map[string]string{}
	rows, err := tx.QueryContext(ctx, `SELECT job_key, status, result FROM jobs WHERE run_id = ?`, runID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var k, st, res string
		if err := rows.Scan(&k, &st, &res); err != nil {
			rows.Close()
			return nil, err
		}
		statuses[k], results[k] = st, res
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}

	blocked, err := tx.QueryContext(ctx,
		`SELECT id, job_key, needs, payload FROM jobs WHERE run_id = ? AND status = 'blocked'`, runID)
	if err != nil {
		return nil, err
	}
	type pending struct {
		id      int64
		key     string
		needs   []string
		payload string
	}
	var list []pending
	for blocked.Next() {
		var id int64
		var key, needsRaw, payload string
		if err := blocked.Scan(&id, &key, &needsRaw, &payload); err != nil {
			blocked.Close()
			return nil, err
		}
		list = append(list, pending{id: id, key: key, needs: decodeStrings(needsRaw), payload: payload})
	}
	blocked.Close()
	if err := blocked.Err(); err != nil {
		return nil, err
	}

	for _, p := range list {
		ready := true
		upstream := make(map[string]string, len(p.needs))
		cancelled := false
		for _, n := range p.needs {
			if statuses[n] != "done" {
				ready = false
				break
			}
			upstream[n] = results[n]
			if results[n] == "cancelled" {
				cancelled = true
			}
		}
		if !ready {
			continue
		}
		run, reason, err := g.decide(meta, p.payload, p.key, upstream, cancelled)
		if err != nil {
			return nil, err
		}
		if !run {
			// Skipping is the GitHub-compatible result for a job whose `if:`
			// says no, and it beats leaving it blocked until a human notices.
			if _, err := tx.ExecContext(ctx, `
				UPDATE jobs SET status = 'done', result = 'skipped', stop_reason = ?,
				                stopped_at = ? WHERE id = ?`, reason, now, p.id); err != nil {
				return nil, err
			}
			continue
		}
		if _, err := tx.ExecContext(ctx, `UPDATE jobs SET status = 'queued' WHERE id = ?`, p.id); err != nil {
			return nil, err
		}
	}

	// Settle the run once nothing is left in flight.
	var open int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE run_id = ? AND status != 'done'`, runID).Scan(&open); err != nil {
		return nil, err
	}
	if open > 0 {
		return nil, nil
	}
	var failed, cancelled int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE run_id = ? AND result = 'failure'`, runID).Scan(&failed); err != nil {
		return nil, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM jobs WHERE run_id = ? AND result = 'cancelled'`, runID).Scan(&cancelled); err != nil {
		return nil, err
	}
	result := "success"
	switch {
	case failed > 0:
		result = "failure"
	case cancelled > 0:
		result = "cancelled"
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE runs SET status = 'done', result = ?, stopped_at = ? WHERE id = ?`,
		result, now, runID); err != nil {
		return nil, err
	}
	// The group is free now, so whatever was waiting behind this run can start.
	// Doing it here rather than on a timer is what keeps a queued deploy from
	// sitting still after the one ahead of it finished.
	if err := promoteGroup(ctx, tx, meta.ConcurrencyGroup); err != nil {
		return nil, err
	}
	return &RunOutcome{RunID: runID, Result: result}, nil
}

// ------------------------------------------------------------------ stops --

// RequestStop records the ask. It does not stop anything by itself: the runner
// learns about it on its next heartbeat and is expected to wind down and ack.
func (s *Store) RequestStop(ctx context.Context, jobID int64, by, reason string) error {
	res, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET stop_requested_at = COALESCE(stop_requested_at, ?), stop_requested_by = ?, stop_reason = ?
		WHERE id = ? AND status IN ('queued', 'running', 'blocked')`,
		ts(s.now()), by, reason, jobID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AckStop records that the runner confirmed it wound down and ran its cleanup.
// This is the half the upstream proto has no room for, and its absence is what
// turns a stop into a request nobody answers.
func (s *Store) AckStop(ctx context.Context, jobID int64, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET stop_acked_at = COALESCE(stop_acked_at, ?), cleanup_ran = 1 WHERE id = ?`,
		ts(at), jobID)
	return err
}

// ForceTerminate gives up waiting for an ack. The job is marked cancelled with
// cleanup_ran left at 0, so the ledger says plainly that whatever the job was
// holding was never released.
func (s *Store) ForceTerminate(ctx context.Context, jobID int64, reason string) (*RunOutcome, error) {
	if _, err := s.db.ExecContext(ctx, `
		UPDATE jobs SET force_terminated = 1, stop_reason = CASE WHEN stop_reason = '' THEN ? ELSE stop_reason END
		WHERE id = ?`, reason, jobID); err != nil {
		return nil, err
	}
	return s.FinishJob(ctx, jobID, "cancelled")
}

// StopPending reports whether a stop has been asked for and not yet acked.
func (s *Store) StopPending(ctx context.Context, jobID int64) (bool, error) {
	var req, ack sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT stop_requested_at, stop_acked_at FROM jobs WHERE id = ?`, jobID).Scan(&req, &ack)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return req.Valid && req.String != "" && (!ack.Valid || ack.String == ""), nil
}

// ------------------------------------------------------------------- logs --

// AppendLogs stores a contiguous window of lines and returns the new ack index.
//
// Lines at or before the current ack are dropped as duplicates — a runner that
// retries after a lost reply must not double-write. A gap (index beyond ack)
// is refused by returning the unchanged ack, which tells the runner to rewind.
func (s *Store) AppendLogs(ctx context.Context, jobID, index int64, rows []LogLine, noMore bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var ack int64
	err = tx.QueryRowContext(ctx, `SELECT ack_index FROM job_log_state WHERE job_id = ?`, jobID).Scan(&ack)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO job_log_state (job_id, ack_index) VALUES (?, 0)`, jobID); err != nil {
			return 0, err
		}
		ack = 0
	} else if err != nil {
		return 0, err
	}

	if index > ack {
		// The runner skipped ahead; refuse and let it resend from ack.
		return ack, nil
	}
	for i, row := range rows {
		at := index + int64(i)
		if at < ack {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO job_logs (job_id, attempt, idx, ts, content)
			VALUES (?, (SELECT attempt FROM jobs WHERE id = ?), ?, ?, ?)
			ON CONFLICT (job_id, attempt, idx) DO NOTHING`,
			jobID, jobID, at, ts(row.Time), row.Content); err != nil {
			return 0, err
		}
		ack = at + 1
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE job_log_state SET ack_index = ?, no_more = ? WHERE job_id = ?`,
		ack, boolInt(noMore), jobID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return ack, nil
}

// LogLine is one stored log row.
type LogLine struct {
	Time    time.Time
	Content string
}

// Logs returns a job's stored log lines in order.
//
// attempt nil means the current one. Passing an older attempt is how the log of
// a failure survives the re-run that was meant to fix it.
func (s *Store) Logs(ctx context.Context, jobID int64, attempt *int) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT ts, content FROM job_logs
		WHERE job_id = ? AND attempt = COALESCE(?, (SELECT attempt FROM jobs WHERE id = ?))
		ORDER BY idx ASC`, jobID, attempt, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LogLine
	for rows.Next() {
		var t, c string
		if err := rows.Scan(&t, &c); err != nil {
			return nil, err
		}
		parsed, _ := time.Parse(time.RFC3339Nano, t)
		out = append(out, LogLine{Time: parsed, Content: c})
	}
	return out, rows.Err()
}

// ----------------------------------------------------------------- reaper --

// OverdueJob is a running job past its timeout or a job whose stop was never
// acknowledged.
type OverdueJob struct {
	ID              int64
	Reason          string // timeout | stop_unacked
	StopRequestedAt *time.Time
}

// Overdue finds jobs the reaper should act on. graceSeconds is how long a
// runner gets to ack a stop before we force-terminate it.
func (s *Store) Overdue(ctx context.Context, graceSeconds int) ([]OverdueJob, error) {
	now := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, timeout_minutes, started_at, stop_requested_at, stop_acked_at
		FROM jobs WHERE status = 'running'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []OverdueJob
	for rows.Next() {
		var id int64
		var timeout int
		var started, stopReq, stopAck sql.NullString
		if err := rows.Scan(&id, &timeout, &started, &stopReq, &stopAck); err != nil {
			return nil, err
		}
		req, ack := parseTS(stopReq), parseTS(stopAck)
		if req != nil {
			// A stop has already been asked for. Reporting the timeout again
			// every sweep would warn seven times about one event while the
			// grace period runs out, and an alert that repeats itself is an
			// alert people learn to scroll past.
			if ack == nil && now.Sub(*req) > time.Duration(graceSeconds)*time.Second {
				out = append(out, OverdueJob{ID: id, Reason: "stop_unacked", StopRequestedAt: req})
			}
			continue
		}
		if st := parseTS(started); st != nil && now.Sub(*st) > time.Duration(timeout)*time.Minute {
			out = append(out, OverdueJob{ID: id, Reason: "timeout"})
		}
	}
	return out, rows.Err()
}

// ------------------------------------------------------------------ views --

// RunSummary is a run plus its job results, for the CLI.
type RunSummary struct {
	Run  Run
	Jobs []Job
}

// RunMeta returns just the run's identity fields, for building the github
// context a task needs.
func (s *Store) RunMeta(ctx context.Context, runID int64) (*Run, error) {
	var r Run
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repo, workflow_name, workflow_file, event, ref, sha, actor, status, result, created_at,
		       event_payload, run_number, run_attempt
		FROM runs WHERE id = ?`, runID).Scan(&r.ID, &r.Repo, &r.WorkflowName, &r.WorkflowFile, &r.Event,
		&r.Ref, &r.SHA, &r.Actor, &r.Status, &r.Result, &created, &r.EventPayload, &r.RunNumber,
		&r.RunAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return &r, nil
}

// ListRuns returns the most recent runs.
func (s *Store) ListRuns(ctx context.Context, limit int) ([]Run, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, workflow_name, workflow_file, event, ref, sha, actor, status, result, created_at
		FROM runs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Run
	for rows.Next() {
		var r Run
		var created string
		if err := rows.Scan(&r.ID, &r.Repo, &r.WorkflowName, &r.WorkflowFile, &r.Event, &r.Ref, &r.SHA,
			&r.Actor, &r.Status, &r.Result, &created); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, r)
	}
	return out, rows.Err()
}

// RunByID returns a run with its jobs.
func (s *Store) RunByID(ctx context.Context, id int64) (*RunSummary, error) {
	var r Run
	var created string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, repo, workflow_name, workflow_file, event, ref, sha, actor, status, result, created_at,
		       run_number, run_attempt, concurrency_group
		FROM runs WHERE id = ?`, id).Scan(&r.ID, &r.Repo, &r.WorkflowName, &r.WorkflowFile, &r.Event,
		&r.Ref, &r.SHA, &r.Actor, &r.Status, &r.Result, &created,
		&r.RunNumber, &r.RunAttempt, &r.ConcurrencyGroup)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)

	rows, err := s.db.QueryContext(ctx, `
		SELECT id, run_id, job_key, name, needs, runs_on, payload, status, result, timeout_minutes,
		       runner_id, started_at, stopped_at, stop_requested_at, stop_reason, stop_acked_at,
		       force_terminated, cleanup_ran
		FROM jobs WHERE run_id = ? ORDER BY id ASC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var jobs []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, *j)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range jobs {
		steps, err := s.StepsOf(ctx, jobs[i].ID)
		if err != nil {
			return nil, err
		}
		jobs[i].Steps = steps
	}
	return &RunSummary{Run: r, Jobs: jobs}, nil
}

// StepsOf returns a job's step timeline in declaration order. The log_index and
// log_length columns are what let a reader jump straight to the slice of the
// stream a step produced, instead of scrolling a job-length log to find it.
func (s *Store) StepsOf(ctx context.Context, jobID int64) ([]StepReport, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT step_index, name, result, started_at, stopped_at, log_index, log_length
		FROM job_steps
		WHERE job_id = ? AND attempt = (SELECT attempt FROM jobs WHERE id = ?)
		ORDER BY step_index ASC`, jobID, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StepReport
	for rows.Next() {
		var st StepReport
		var started, stopped sql.NullString
		if err := rows.Scan(&st.Index, &st.Name, &st.Result, &started, &stopped,
			&st.LogIndex, &st.LogLength); err != nil {
			return nil, err
		}
		st.StartedAt = parseNullTime(started)
		st.StoppedAt = parseNullTime(stopped)
		out = append(out, st)
	}
	return out, rows.Err()
}

func parseNullTime(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339Nano, v.String)
	if err != nil {
		return nil
	}
	return &t
}

// MarshalNeeds is a helper for building the needs context payload.
func MarshalNeeds(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

var _ = fmt.Sprintf

// JobGate decides whether a blocked job runs now that its upstreams have
// settled. It is injected rather than implemented here so the store stays SQL:
// evaluating `if:` needs an expression interpreter and a workflow model, and
// neither belongs in the layer that owns the transaction.
//
// The default below is the pre-`if:` behaviour, so a store used without a gate
// still schedules correctly for the common case.
// The run is passed whole because a job-level `if:` may read any of the github
// context — `github.ref`, `github.event_name`, `github.event.*` — and a gate
// that cannot see them does not fail, it quietly answers false.
type JobGate func(run *Run, payload, jobKey string, upstream map[string]string, runCancelled bool) (bool, error)

func (g JobGate) decide(r *Run, payload, key string, upstream map[string]string, cancelled bool) (bool, string, error) {
	if g != nil {
		run, err := g(r, payload, key, upstream, cancelled)
		if err != nil {
			return false, "", err
		}
		if run {
			return true, "", nil
		}
		return false, "if_false", nil
	}
	for _, r := range upstream {
		if r != "success" {
			return false, "upstream_failed", nil
		}
	}
	return true, "", nil
}

// runInTx reads the run's identity inside the caller's transaction, so the gate
// sees the same snapshot the scheduling decision is made against.
func runInTx(ctx context.Context, tx *sql.Tx, runID int64) (*Run, error) {
	var r Run
	err := tx.QueryRowContext(ctx, `
		SELECT id, repo, workflow_name, workflow_file, event, ref, sha, actor, status, result,
		       event_payload, run_number, concurrency_group, run_attempt
		FROM runs WHERE id = ?`, runID).Scan(&r.ID, &r.Repo, &r.WorkflowName, &r.WorkflowFile,
		&r.Event, &r.Ref, &r.SHA, &r.Actor, &r.Status, &r.Result, &r.EventPayload, &r.RunNumber,
		&r.ConcurrencyGroup, &r.RunAttempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	return &r, err
}
