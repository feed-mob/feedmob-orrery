package store

import (
	"context"
	"database/sql"
	"errors"
)

// NoteResult records a run's verdict for its workflow-and-ref and reports what
// the previous one was, together with whether this is a change worth telling
// anyone about.
//
// Same-result runs are recorded but not announced. That is the whole mechanism:
// the first failure is news, the fourth failure of the same broken thing is
// not, and the success after them is news again.
func (s *Store) NoteResult(ctx context.Context, scope, result string) (previous string, changed bool, err error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `SELECT result FROM notify_state WHERE scope = ?`, scope).Scan(&previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", false, err
	}
	// A first-ever success is not an announcement: nobody needs to be told that
	// a workflow they just set up worked. A first-ever failure is.
	changed = previous != result && !(previous == "" && result == "success")

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO notify_state (scope, result, notified_at) VALUES (?, ?, ?)
		ON CONFLICT (scope) DO UPDATE SET result = excluded.result, notified_at = excluded.notified_at`,
		scope, result, ts(s.now())); err != nil {
		return "", false, err
	}
	return previous, changed, tx.Commit()
}
