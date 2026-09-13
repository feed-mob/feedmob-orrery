package store

import (
	"context"
	"database/sql"
	"time"
)

// Deployment is one recorded attempt to put a version somewhere.
type Deployment struct {
	ID             int64
	Repo           string
	Environment    string
	Version        string
	SHA            string
	Ref            string
	URL            string
	RunID          int64
	JobID          int64
	WorkflowFile   string
	Actor          string
	Result         string
	RolledBackFrom *int64
	CreatedAt      time.Time
}

// RecordDeployment appends to the ledger.
//
// Failures are recorded too. A deployment that failed is the most interesting
// row on the page — it is what makes "the last thing we tried did not work"
// visible instead of leaving the environment silently showing the version
// before it.
func (s *Store) RecordDeployment(ctx context.Context, d Deployment) (int64, error) {
	var rolledBack any
	if d.RolledBackFrom != nil {
		rolledBack = *d.RolledBackFrom
	}
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO deployments (repo, environment, version, sha, ref, url, run_id, job_id,
		                         workflow_file, actor, result, rolled_back_from, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		d.Repo, d.Environment, d.Version, d.SHA, d.Ref, d.URL, d.RunID, d.JobID,
		d.WorkflowFile, d.Actor, d.Result, rolledBack, ts(s.now()))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CurrentDeployments is the latest row per environment, whatever its result.
//
// Latest rather than latest-successful: an environment whose last deployment
// failed is not still running the version before it in any sense anyone can
// rely on — the deploy may have half-applied. Showing the failure is the honest
// answer, and the previous success is one column over.
func (s *Store) CurrentDeployments(ctx context.Context, repo string) ([]Deployment, error) {
	where, args := "", []any{}
	if repo != "" {
		where, args = "WHERE repo = ?", []any{repo}
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, environment, version, sha, ref, url, run_id, job_id, workflow_file,
		       actor, result, rolled_back_from, created_at
		FROM deployments `+where+`
		GROUP BY repo, environment
		HAVING id = MAX(id)
		ORDER BY repo, environment`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeployments(rows)
}

// DeploymentHistory lists an environment's deployments, newest first.
func (s *Store) DeploymentHistory(ctx context.Context, repo, env string, limit int) ([]Deployment, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, environment, version, sha, ref, url, run_id, job_id, workflow_file,
		       actor, result, rolled_back_from, created_at
		FROM deployments WHERE repo = ? AND environment = ?
		ORDER BY id DESC LIMIT ?`, repo, env, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanDeployments(rows)
}

// LastGoodDeployment is what a rollback goes back to: the most recent success
// that is not the one being rolled back.
//
// `before` excludes everything from that id onwards, so rolling back twice
// walks further back rather than bouncing between the same two versions.
func (s *Store) LastGoodDeployment(ctx context.Context, repo, env string, before int64) (*Deployment, error) {
	q := `SELECT id, repo, environment, version, sha, ref, url, run_id, job_id, workflow_file,
	             actor, result, rolled_back_from, created_at
	      FROM deployments WHERE repo = ? AND environment = ? AND result = 'success'`
	args := []any{repo, env}
	if before > 0 {
		q += " AND id < ?"
		args = append(args, before)
	}
	q += " ORDER BY id DESC LIMIT 1"

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanDeployments(rows)
	if err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ErrNotFound
	}
	return &out[0], nil
}

func scanDeployments(rows *sql.Rows) ([]Deployment, error) {
	var out []Deployment
	for rows.Next() {
		var d Deployment
		var created string
		var rolled sql.NullInt64
		if err := rows.Scan(&d.ID, &d.Repo, &d.Environment, &d.Version, &d.SHA, &d.Ref, &d.URL,
			&d.RunID, &d.JobID, &d.WorkflowFile, &d.Actor, &d.Result, &rolled, &created); err != nil {
			return nil, err
		}
		if rolled.Valid {
			v := rolled.Int64
			d.RolledBackFrom = &v
		}
		d.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		out = append(out, d)
	}
	return out, rows.Err()
}

// JobOutputs reads back what a job reported, so a deployment can be recorded
// with the version the job actually produced rather than the commit it came
// from.
func (s *Store) JobOutputs(ctx context.Context, jobID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM job_outputs WHERE job_id = ?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		out[k] = v
	}
	return out, rows.Err()
}

// DeployJob is what a settled job needs to say about itself for the ledger.
// Empty Environment means the job was not a deployment.
type DeployJob struct {
	Environment  string
	URL          string
	AutoRollback bool
	VersionFrom  string
	WorkflowFile string
	Ref          string
	SHA          string
	Actor        string
	RunID        int64
}

// DeployInfo reads a job's deployment shape, joined to its run.
func (s *Store) DeployInfo(ctx context.Context, jobID int64) (*DeployJob, error) {
	var d DeployJob
	var auto int
	err := s.db.QueryRowContext(ctx, `
		SELECT j.environment, j.environment_url, j.auto_rollback, j.version_from,
		       r.workflow_file, r.ref, r.sha, r.actor, r.id
		FROM jobs j JOIN runs r ON r.id = j.run_id
		WHERE j.id = ?`, jobID).Scan(&d.Environment, &d.URL, &auto, &d.VersionFrom,
		&d.WorkflowFile, &d.Ref, &d.SHA, &d.Actor, &d.RunID)
	if err != nil {
		return nil, err
	}
	d.AutoRollback = auto == 1
	return &d, nil
}
