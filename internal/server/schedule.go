package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/feed-mob/feedmob-orrery/internal/forge"
	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// cronParser is GitHub's dialect: five fields, no seconds, no @-shorthands
// beyond the standard ones. Parsing with a wider dialect would accept a
// six-field expression here that GitHub rejects, and the workflow would run on
// a schedule its author never sees on GitHub.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

// refreshSchedules records what a repository's workflows ask to be run on.
//
// Schedules are read from the default branch, the way GitHub does it: a cron
// added on a feature branch does not start firing before it is merged, which is
// the only rule under which reviewing a schedule change means anything.
func (s *Server) refreshSchedules(ctx context.Context, repo, defaultBranch string, files []forge.File) error {
	var rows []store.Schedule
	for _, f := range files {
		wf, err := workflow.Parse(f.Content)
		if err != nil {
			continue
		}
		triggers, err := wf.Triggers()
		if err != nil {
			continue
		}
		for _, t := range triggers {
			if t.Event != "schedule" {
				continue
			}
			for _, expr := range t.Cron {
				sched, err := cronParser.Parse(expr)
				if err != nil {
					s.log.Warn("ignoring an unparseable cron",
						"repo", repo, "file", f.Path, "cron", expr, "err", err)
					continue
				}
				rows = append(rows, store.Schedule{
					Repo: repo, WorkflowFile: f.Path, Ref: defaultBranch,
					Cron: expr, NextDueAt: sched.Next(time.Now().UTC()),
				})
			}
		}
	}
	// Replace wholesale: a cron deleted from the default branch has to stop
	// firing, and merging its removal is the only signal we get.
	return s.st.ReplaceSchedules(ctx, repo, rows)
}

// RunScheduler fires schedules as they come due. It shares the reaper's shape:
// one loop, cancelled with the server.
func (s *Server) RunScheduler(ctx context.Context) {
	t := time.NewTicker(s.cfg.ScheduleInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		due, err := s.st.DueSchedules(ctx, time.Now().UTC())
		if err != nil {
			s.log.Error("reading due schedules failed", "err", err)
			continue
		}
		for _, sc := range due {
			// Advance first. A schedule that fires and then fails to be
			// rescheduled would fire again on the next tick, and a cron that
			// runs every five seconds because its workflow is broken is worse
			// than one that misses an occurrence.
			next := time.Now().UTC().Add(time.Minute)
			if sched, err := cronParser.Parse(sc.Cron); err == nil {
				next = sched.Next(time.Now().UTC())
			}
			if err := s.st.AdvanceSchedule(ctx, sc.ID, next); err != nil {
				s.log.Error("advancing a schedule failed", "id", sc.ID, "err", err)
				continue
			}
			runID, err := s.startScheduled(ctx, sc)
			if err != nil {
				s.log.Error("scheduled run failed to start",
					"repo", sc.Repo, "file", sc.WorkflowFile, "cron", sc.Cron, "err", err)
				continue
			}
			s.log.Info("scheduled run queued", "run", runID,
				"repo", sc.Repo, "file", sc.WorkflowFile, "cron", sc.Cron, "next", next)
		}
	}
}

func (s *Server) startScheduled(ctx context.Context, sc store.Schedule) (int64, error) {
	files, err := s.forge.Workflows(ctx, sc.Repo, sc.Ref)
	if err != nil {
		return 0, err
	}
	var file *forge.File
	for i := range files {
		if files[i].Path == sc.WorkflowFile {
			file = &files[i]
			break
		}
	}
	if file == nil {
		return 0, fmt.Errorf("%s is no longer in %s at %s", sc.WorkflowFile, sc.Repo, sc.Ref)
	}
	wf, err := workflow.Parse(file.Content)
	if err != nil {
		return 0, err
	}
	jobs, err := newJobsFor(wf, file.Content)
	if err != nil {
		return 0, err
	}
	// Same reason as a dispatched run: a scheduled one carries a branch and no
	// commit, and `github.sha` being empty surfaces three layers down.
	sha, err := s.forge.ResolveRef(ctx, sc.Repo, sc.Ref)
	if err != nil {
		return 0, fmt.Errorf("resolve %s@%s: %w", sc.Repo, sc.Ref, err)
	}
	payload, _ := json.Marshal(map[string]any{"schedule": sc.Cron, "repository": map[string]any{"full_name": sc.Repo}})
	run := store.Run{
		Repo: sc.Repo, WorkflowName: wf.Name, WorkflowFile: file.Path,
		Event: "schedule", Ref: "refs/heads/" + sc.Ref, SHA: sha, Actor: "orrery",
		EventPayload: string(payload),
	}
	// Overlap is handled by the concurrency group rather than by a policy of
	// its own: a scheduled run that is still going when the next is due makes
	// the next one wait, which is the "skip/queue" behaviour GitHub's schedules
	// do not offer at all.
	return s.prepare(ctx, wf, &run, jobs, nil)
}

// RunPruner enforces the retention window.
//
// Without it the database only grows: a repository building twenty times a day
// keeps every log line of every run forever, and the first anyone hears about
// it is a full disk on the control plane.
func (s *Server) RunPruner(ctx context.Context) {
	if s.cfg.Retention <= 0 {
		return
	}
	// Hourly is plenty for a window measured in days, and cheap enough that the
	// first sweep can happen at startup without anyone noticing.
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		runs, err := s.st.Prune(ctx, s.cfg.Retention)
		if err != nil {
			s.log.Error("pruning old runs failed", "err", err)
		} else if runs > 0 {
			s.log.Info("pruned runs past the retention window", "runs", runs, "retention", s.cfg.Retention)
		}
		// Deliveries live on a much shorter clock: GitHub will not redeliver a
		// week-old event, so remembering it buys nothing.
		if _, err := s.st.PruneDeliveries(ctx, 7*24*time.Hour); err != nil {
			s.log.Error("pruning old deliveries failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// chainWorkflowRun starts the workflows that wait on this one finishing.
//
// `on: workflow_run` is how a repository splits "build" from "deploy" without
// making one workflow that does both — and the split is the point: the deploy
// can have different permissions, a different concurrency group and a different
// approval story from the build that produced the artefact.
//
// Runs from this path are themselves `workflow_run` events, but they do not
// chain further: a workflow that fires on its own downstream is a loop, and one
// hop is what GitHub allows too.
func (s *Server) chainWorkflowRun(ctx context.Context, upstream *store.Run) {
	if upstream == nil || upstream.Event == "workflow_run" {
		return
	}
	if !s.repoAllowed(upstream.Repo) {
		return
	}
	files, err := s.forge.Workflows(ctx, upstream.Repo, upstream.SHA)
	if err != nil {
		s.log.Error("cannot read workflows to chain from", "run", upstream.ID, "err", err)
		return
	}
	ev := workflow.Event{
		Name:       "workflow_run",
		Ref:        upstream.Ref,
		Workflow:   upstream.WorkflowName,
		Conclusion: upstream.Result,
		Action:     "completed",
	}
	payload, _ := json.Marshal(map[string]any{
		"action": "completed",
		"workflow_run": map[string]any{
			"id": upstream.ID, "name": upstream.WorkflowName,
			"conclusion": upstream.Result, "head_sha": upstream.SHA,
			"head_branch": strings.TrimPrefix(upstream.Ref, "refs/heads/"),
			"event":       upstream.Event,
		},
		"repository": map[string]any{"full_name": upstream.Repo},
	})

	for _, f := range files {
		wf, err := workflow.Parse(f.Content)
		if err != nil {
			continue
		}
		// Never chain a workflow off itself, however it is written.
		if f.Path == upstream.WorkflowFile {
			continue
		}
		if _, ok, err := wf.Matches(ev); err != nil || !ok {
			continue
		}
		jobs, err := newJobsFor(wf, f.Content)
		if err != nil {
			s.log.Error("cannot build the chained run", "file", f.Path, "err", err)
			continue
		}
		run := store.Run{
			Repo: upstream.Repo, WorkflowName: wf.Name, WorkflowFile: f.Path,
			Event: "workflow_run", Ref: upstream.Ref, SHA: upstream.SHA,
			Actor: upstream.Actor, EventPayload: string(payload),
		}
		id, err := s.prepare(ctx, wf, &run, jobs, nil)
		if err != nil {
			s.log.Error("cannot create the chained run", "file", f.Path, "err", err)
			continue
		}
		s.reportStatus(&run, forge.StatePending, fmt.Sprintf("%d job(s) queued", len(jobs)))
		s.log.Info("chained a run", "from", upstream.ID, "to", id,
			"workflow", wf.Name, "conclusion", upstream.Result)
	}
}
