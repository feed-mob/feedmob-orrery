package store

import (
	"context"
	"time"
)

// Schedule is one `on: schedule` cron of one workflow.
//
// Kept in the database rather than re-read from the forge every tick: a cron
// that only exists in a workflow file means one API call per repository per
// minute, and the rate limit is shared with everything else Orrery does.
type Schedule struct {
	ID           int64
	Repo         string
	WorkflowFile string
	// Ref is the default branch. GitHub only honours schedules on it, and so do
	// we: a cron added on a feature branch must not fire before it is merged.
	Ref       string
	Cron      string
	NextDueAt time.Time
}

// ReplaceSchedules makes the stored schedules for a repository exactly the ones
// given.
//
// Wholesale replacement, not a merge: a cron deleted from the default branch
// has to stop firing, and its absence from this list is the only signal that it
// was deleted. A cron that is unchanged keeps its next-due time, so a push does
// not reset the clock on a nightly job.
func (s *Store) ReplaceSchedules(ctx context.Context, repo string, rows []Schedule) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	keep := map[string]bool{}
	for _, r := range rows {
		keep[r.WorkflowFile+"\x00"+r.Cron] = true
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO schedules (repo, workflow_file, ref, cron, next_due_at)
			VALUES (?, ?, ?, ?, ?)
			ON CONFLICT (repo, workflow_file, cron) DO UPDATE SET ref = excluded.ref`,
			r.Repo, r.WorkflowFile, r.Ref, r.Cron, ts(r.NextDueAt)); err != nil {
			return err
		}
	}
	existing, err := tx.QueryContext(ctx, `SELECT id, workflow_file, cron FROM schedules WHERE repo = ?`, repo)
	if err != nil {
		return err
	}
	var stale []int64
	for existing.Next() {
		var id int64
		var file, expr string
		if err := existing.Scan(&id, &file, &expr); err != nil {
			existing.Close()
			return err
		}
		if !keep[file+"\x00"+expr] {
			stale = append(stale, id)
		}
	}
	existing.Close()
	if err := existing.Err(); err != nil {
		return err
	}
	for _, id := range stale {
		if _, err := tx.ExecContext(ctx, `DELETE FROM schedules WHERE id = ?`, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// DueSchedules returns the schedules that should have fired by now.
func (s *Store) DueSchedules(ctx context.Context, now time.Time) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, workflow_file, ref, cron, next_due_at
		FROM schedules WHERE next_due_at <= ? ORDER BY next_due_at ASC`, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Schedule
	for rows.Next() {
		var sc Schedule
		var due string
		if err := rows.Scan(&sc.ID, &sc.Repo, &sc.WorkflowFile, &sc.Ref, &sc.Cron, &due); err != nil {
			return nil, err
		}
		sc.NextDueAt, _ = time.Parse(time.RFC3339Nano, due)
		out = append(out, sc)
	}
	return out, rows.Err()
}

// AdvanceSchedule moves a schedule to its next occurrence. Called before the
// run is created, so a workflow that cannot start does not re-fire every tick.
func (s *Store) AdvanceSchedule(ctx context.Context, id int64, next time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE schedules SET next_due_at = ?, last_fired_at = ? WHERE id = ?`,
		ts(next), ts(s.now()), id)
	return err
}
