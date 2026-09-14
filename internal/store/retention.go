package store

import (
	"context"
	"time"
)

// Prune deletes runs that finished longer ago than age, and everything hanging
// off them.
//
// The jobs, logs, steps and outputs go with the run through ON DELETE CASCADE,
// so this is one statement rather than a hand-rolled walk that would eventually
// forget a table someone added later.
//
// Only finished runs: a queued run older than the window is not old, it is
// stuck, and deleting it would hide that.
func (s *Store) Prune(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, nil
	}
	cutoff := ts(s.now().UTC().Add(-age))
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM runs WHERE status = 'done' AND COALESCE(stopped_at, created_at) < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil || n == 0 {
		return n, err
	}
	// Deletes leave free pages behind; without this the file only ever grows,
	// and "we added retention" would not show up on the disk graph anyone is
	// actually watching.
	if _, err := s.db.ExecContext(ctx, `PRAGMA incremental_vacuum`); err != nil {
		// Not fatal: the rows are gone either way.
		return n, nil
	}
	return n, nil
}

// PruneDeliveries forgets webhook delivery ids past the window GitHub could
// still redeliver in. Keeping them forever would make the idempotency table the
// largest thing in the database.
func (s *Store) PruneDeliveries(ctx context.Context, age time.Duration) (int64, error) {
	if age <= 0 {
		return 0, nil
	}
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM deliveries WHERE received_at < ?`, ts(s.now().UTC().Add(-age)))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// RunExists reports whether a run row is still there. The shared artifact store
// uses it to find directories whose run has been pruned: the run table is the
// authority on what an artifact still belongs to.
func (s *Store) RunExists(ctx context.Context, id int64) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE id = ?`, id).Scan(&n)
	return n > 0, err
}
