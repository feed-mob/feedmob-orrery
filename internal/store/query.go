package store

import (
	"context"
	"strings"
	"time"
)

// RunFilter narrows a run listing.
//
// Without one the dashboard shows the newest fifty and nothing else, which is
// fine until the fifty-first run and useless for "what happened to deploy last
// Tuesday".
type RunFilter struct {
	Repo     string
	Workflow string // matched against the workflow name or its file path
	Status   string // queued|pending|running|done
	Result   string // success|failure|cancelled|skipped
	Event    string
	Actor    string
	Branch   string // ref, with or without refs/heads/
	// Before pages backwards: only runs with an id below this. Ids descend, so
	// this is a cursor rather than an offset — an offset would skip or repeat
	// rows as new runs arrive while someone is paging.
	Before int64
	Limit  int
}

// RunPage is one screen of runs plus what the caller needs to ask for the next.
type RunPage struct {
	Runs []Run
	// NextBefore is the cursor for the following page, 0 when there is none.
	NextBefore int64
	// Total is how many runs match the filter, ignoring the cursor.
	Total int
}

// QueryRuns lists runs newest first, filtered and paged.
func (s *Store) QueryRuns(ctx context.Context, f RunFilter) (*RunPage, error) {
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	where, args := runWhere(f)

	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM runs `+where, args...).Scan(&total); err != nil {
		return nil, err
	}

	cursorWhere, cursorArgs := where, args
	if f.Before > 0 {
		if cursorWhere == "" {
			cursorWhere = "WHERE id < ?"
		} else {
			cursorWhere += " AND id < ?"
		}
		cursorArgs = append(append([]any(nil), args...), f.Before)
	}
	// One extra row tells us whether another page exists without a second query.
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, workflow_name, workflow_file, event, ref, sha, actor, status, result,
		       created_at, run_number, run_attempt
		FROM runs `+cursorWhere+` ORDER BY id DESC LIMIT ?`,
		append(cursorArgs, f.Limit+1)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	page := &RunPage{Total: total}
	for rows.Next() {
		var r Run
		var created string
		if err := rows.Scan(&r.ID, &r.Repo, &r.WorkflowName, &r.WorkflowFile, &r.Event, &r.Ref,
			&r.SHA, &r.Actor, &r.Status, &r.Result, &created, &r.RunNumber, &r.RunAttempt); err != nil {
			return nil, err
		}
		r.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		page.Runs = append(page.Runs, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(page.Runs) > f.Limit {
		page.Runs = page.Runs[:f.Limit]
		page.NextBefore = page.Runs[len(page.Runs)-1].ID
	}
	return page, nil
}

func runWhere(f RunFilter) (string, []any) {
	var clauses []string
	var args []any
	add := func(sql string, v any) {
		clauses = append(clauses, sql)
		args = append(args, v)
	}
	if f.Repo != "" {
		add("repo = ?", f.Repo)
	}
	if f.Workflow != "" {
		// Name or file: people type "deploy" meaning either.
		clauses = append(clauses, "(workflow_name LIKE ? OR workflow_file LIKE ?)")
		args = append(args, "%"+f.Workflow+"%", "%"+f.Workflow+"%")
	}
	if f.Status != "" {
		add("status = ?", f.Status)
	}
	if f.Result != "" {
		add("result = ?", f.Result)
	}
	if f.Event != "" {
		add("event = ?", f.Event)
	}
	if f.Actor != "" {
		add("actor = ?", f.Actor)
	}
	if f.Branch != "" {
		// Accept "main" and "refs/heads/main" as the same thing, because the
		// list shows the short form and people type back what they see.
		short := strings.TrimPrefix(strings.TrimPrefix(f.Branch, "refs/heads/"), "refs/tags/")
		clauses = append(clauses, "(ref = ? OR ref = ? OR ref = ?)")
		args = append(args, f.Branch, "refs/heads/"+short, "refs/tags/"+short)
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return "WHERE " + strings.Join(clauses, " AND "), args
}

// Facets are the distinct values worth offering as filters, so the dashboard
// can present what exists rather than a free-text box the user has to guess at.
type Facets struct {
	Repos     []string
	Workflows []string
	Events    []string
	Actors    []string
}

func (s *Store) Facets(ctx context.Context) (*Facets, error) {
	out := &Facets{}
	for _, q := range []struct {
		col  string
		dest *[]string
	}{
		{"repo", &out.Repos},
		{"workflow_name", &out.Workflows},
		{"event", &out.Events},
		{"actor", &out.Actors},
	} {
		rows, err := s.db.QueryContext(ctx,
			`SELECT DISTINCT `+q.col+` FROM runs WHERE `+q.col+` != '' ORDER BY `+q.col+` LIMIT 200`)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var v string
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return nil, err
			}
			*q.dest = append(*q.dest, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Runners lists registered runners, most recently seen first.
func (s *Store) Runners(ctx context.Context) ([]Runner, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, uuid, name, status, version, labels, capabilities, ephemeral, created_at, last_seen_at
		FROM runners ORDER BY last_seen_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Runner
	for rows.Next() {
		var r Runner
		var labels, caps, created, seen string
		var ephemeral int
		if err := rows.Scan(&r.ID, &r.UUID, &r.Name, &r.Status, &r.Version,
			&labels, &caps, &ephemeral, &created, &seen); err != nil {
			return nil, err
		}
		r.Labels, r.Capabilities = decodeStrings(labels), decodeStrings(caps)
		r.Ephemeral = ephemeral == 1
		r.LastSeenAt, _ = time.Parse(time.RFC3339Nano, seen)
		out = append(out, r)
	}
	return out, rows.Err()
}

// Schedules lists every registered cron, soonest first.
func (s *Store) Schedules(ctx context.Context) ([]Schedule, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, repo, workflow_file, ref, cron, next_due_at
		FROM schedules ORDER BY next_due_at ASC`)
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
