package store

import (
	"context"
	"database/sql"
	"fmt"
)

// Rerun starts a run over without a new commit.
//
// The run keeps its id and gains an attempt, the way GitHub does it, and that
// is what makes a partial re-run worth having: the artifacts and outputs of the
// jobs that passed are still there for the jobs being re-run. Creating a fresh
// run instead would leave a re-run deploy job looking for an artifact that the
// build job is no longer going to produce.
//
// failedOnly re-runs the jobs that did not succeed and everything downstream of
// them. Downstream has to come along: its inputs are about to change.
func (s *Store) Rerun(ctx context.Context, runID int64, failedOnly bool) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var status string
	var group string
	if err := tx.QueryRowContext(ctx,
		`SELECT status, concurrency_group FROM runs WHERE id = ?`, runID).Scan(&status, &group); err != nil {
		if err == sql.ErrNoRows {
			return 0, ErrNotFound
		}
		return 0, err
	}
	if status != "done" {
		// Re-running a run that is still going would leave two attempts of the
		// same job in flight against one workspace.
		return 0, fmt.Errorf("run %d is %s; only a finished run can be re-run", runID, status)
	}

	type jobRow struct {
		id     int64
		key    string
		needs  []string
		result string
	}
	rows, err := tx.QueryContext(ctx,
		`SELECT id, job_key, needs, result FROM jobs WHERE run_id = ? ORDER BY id ASC`, runID)
	if err != nil {
		return 0, err
	}
	var jobs []jobRow
	for rows.Next() {
		var j jobRow
		var needs string
		if err := rows.Scan(&j.id, &j.key, &needs, &j.result); err != nil {
			rows.Close()
			return 0, err
		}
		j.needs = decodeStrings(needs)
		jobs = append(jobs, j)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, ErrNotFound
	}

	selected := map[string]bool{}
	for _, j := range jobs {
		if !failedOnly || j.result != "success" {
			selected[j.key] = true
		}
	}
	if len(selected) == 0 {
		return 0, fmt.Errorf("run %d has no failed jobs to re-run", runID)
	}
	// Pull in everything downstream, transitively. A single pass is not enough:
	// a job three deep only becomes reachable once its own upstream is in.
	for changed := true; changed; {
		changed = false
		for _, j := range jobs {
			if selected[j.key] {
				continue
			}
			for _, n := range j.needs {
				if selected[n] {
					selected[j.key] = true
					changed = true
					break
				}
			}
		}
	}

	now := ts(s.now())
	count := 0
	for _, j := range jobs {
		if !selected[j.key] {
			continue
		}
		blocked := false
		for _, n := range j.needs {
			if selected[n] {
				blocked = true
				break
			}
		}
		next := "queued"
		if blocked {
			next = "blocked"
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE jobs SET attempt = attempt + 1, status = ?, result = '', runner_id = NULL,
			                started_at = NULL, stopped_at = NULL,
			                stop_requested_at = NULL, stop_requested_by = '', stop_reason = '',
			                stop_acked_at = NULL, force_terminated = 0, cleanup_ran = 0
			WHERE id = ?`, next, j.id); err != nil {
			return 0, err
		}
		// The new attempt's log stream starts at 0 again; the previous one is
		// kept under its own attempt.
		if _, err := tx.ExecContext(ctx,
			`UPDATE job_log_state SET ack_index = 0, no_more = 0, truncated = 0 WHERE job_id = ?`,
			j.id); err != nil {
			return 0, err
		}
		// Outputs of a job about to run again are stale. Outputs of the jobs
		// that are not being re-run stay, which is how `needs.<job>.outputs`
		// still resolves for the jobs downstream of them.
		if _, err := tx.ExecContext(ctx, `DELETE FROM job_outputs WHERE job_id = ?`, j.id); err != nil {
			return 0, err
		}
		count++
	}

	// Re-entering the concurrency group is not a formality: the whole point is
	// that a re-run of a deploy cannot race a push that is already deploying.
	runStatus, superseded, cancel, err := admit(ctx, tx,
		Run{ConcurrencyGroup: group, CancelInProgress: false})
	if err != nil {
		return 0, err
	}
	for _, id := range append(superseded, cancel...) {
		if id == runID {
			continue
		}
		if err := cancelRun(ctx, tx, id, "concurrency", "superseded", now); err != nil {
			return 0, err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE runs SET run_attempt = run_attempt + 1, status = ?, result = '', stopped_at = NULL
		WHERE id = ?`, runStatus, runID); err != nil {
		return 0, err
	}
	if err := bumpTasksVersion(ctx, tx); err != nil {
		return 0, err
	}
	return count, tx.Commit()
}
