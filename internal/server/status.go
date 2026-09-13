package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/forge"
	"github.com/feed-mob/feedmob-orrery/internal/gate"
	"github.com/feed-mob/feedmob-orrery/internal/notify"
	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// reportStatus writes a run's state back to the forge, where branch protection
// can see it.
//
// Without this Orrery is invisible to the pull requests it is supposed to be
// gating: the code can be built and the verdict reached, and GitHub will still
// show a PR with no checks. It runs detached from the caller's context on
// purpose — a run must not fail because reporting it did, and a report must
// not be abandoned because the HTTP request that triggered it finished.
func (s *Server) reportStatus(run *store.Run, state forge.State, description string) {
	if s.cfg.ForgeToken == "" || run == nil || run.SHA == "" {
		return
	}
	// A run the CLI invented has no commit on any forge to report against.
	if !strings.Contains(run.Repo, "/") {
		return
	}
	st := forge.Status{
		State:       state,
		Context:     statusContext(run),
		Description: description,
		TargetURL:   s.runURL(run.ID),
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.forge.SetCommitStatus(ctx, run.Repo, run.SHA, st); err != nil {
			// Loud, but not fatal: the run itself is unaffected, and an
			// operator needs to know the gate is not being reported.
			s.log.Error("commit status write-back failed",
				"run", run.ID, "repo", run.Repo, "sha", run.SHA, "state", state, "err", err)
			return
		}
		s.log.Info("commit status written", "run", run.ID, "state", state, "context", st.Context)
	}()
}

// statusContext is the name branch protection matches on, so it has to be
// stable across runs of the same workflow and distinct between workflows.
func statusContext(run *store.Run) string {
	name := run.WorkflowName
	if name == "" {
		name = run.WorkflowFile
	}
	return "orrery / " + name
}

func (s *Server) runURL(id int64) string {
	if s.cfg.PublicURL == "" {
		return ""
	}
	return fmt.Sprintf("%s/runs/%d", strings.TrimSuffix(s.cfg.PublicURL, "/"), id)
}

// statusFor maps a run's verdict onto the forge's vocabulary.
//
// Cancelled becomes `error`, not `failure`: a reviewer reading "failure" is
// told the code is bad, when what happened is that we stopped looking.
func statusFor(result string) (forge.State, string) {
	switch result {
	case "success":
		return forge.StateSuccess, "All jobs passed"
	case "failure":
		return forge.StateFailure, "At least one job failed"
	case "cancelled":
		return forge.StateError, "The run was cancelled"
	case "skipped":
		return forge.StateSuccess, "Nothing to do"
	default:
		return forge.StateError, "The run ended without a verdict: " + result
	}
}

// notifyIfChanged tells people when a workflow's verdict changes.
//
// Scoped to repo + workflow file + ref, so a failing nightly job and a failing
// deploy of the same repo are two different stories, and a branch that is
// always red does not drown out main going red.
func (s *Server) notifyIfChanged(ctx context.Context, run *store.Run) {
	if s.notifier == nil || run == nil {
		return
	}
	scope := run.Repo + "\x00" + run.WorkflowFile + "\x00" + run.Ref
	previous, changed, err := s.st.NoteResult(ctx, scope, run.Result)
	if err != nil {
		s.log.Error("recording the notification state failed", "run", run.ID, "err", err)
		return
	}
	if !changed {
		return
	}
	msg := notify.Message(notify.Event{
		Repo: run.Repo, Workflow: orDefault(run.WorkflowName, run.WorkflowFile),
		Ref: run.Ref, SHA: run.SHA, Actor: run.Actor, EventName: run.Event,
		Result: run.Result, Previous: previous,
		RunID: run.ID, RunAttempt: run.RunAttempt, URL: s.runURL(run.ID),
	})
	// Detached for the same reason the commit status is: a run must not fail
	// because telling people about it did.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := s.notifier.Send(ctx, msg); err != nil {
			s.log.Error("notification failed", "run", run.ID, "err", err)
			return
		}
		s.log.Info("notified", "run", run.ID, "result", run.Result, "previous", previous)
	}()
}

// announce reports a settled run to the forge. A nil outcome means the run
// still has jobs in flight, which is the common case — a run of four jobs
// settles once, not four times.
func (s *Server) announce(ctx context.Context, outcome *store.RunOutcome) {
	if outcome == nil {
		return
	}
	run, err := s.st.RunMeta(ctx, outcome.RunID)
	if err != nil {
		s.log.Error("cannot read the run we just settled", "run", outcome.RunID, "err", err)
		return
	}
	state, desc := statusFor(outcome.Result)
	s.reportStatus(run, state, desc)
	s.notifyIfChanged(ctx, run)
	// Detached: chaining reads workflows from the forge, and the caller here is
	// a runner reporting a finished task. It should not wait on a network call
	// to somebody else's API to get its own response.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		s.chainWorkflowRun(ctx, run)
	}()
}

// prepare resolves everything in a run that is written as an expression, then
// creates it.
//
// One seam for all five ways a run starts — CLI submit, webhook, dispatch,
// schedule, chain. They had the same three steps copied five times, which is
// how `environment:` came to be interpolated in none of them.
func (s *Server) prepare(ctx context.Context, wf *workflow.Workflow, run *store.Run,
	jobs []store.NewJob, inputs map[string]string) (int64, error) {
	env := gate.Env{
		Github: gate.GithubFor(run.Repo, run.Ref, run.SHA, run.Actor, run.Event,
			run.WorkflowName, "0", "0", run.EventPayload),
		Vars:   s.cfg.Vars,
		Inputs: anyMap(inputs),
	}
	if err := s.applyConcurrency(wf, run, env); err != nil {
		return 0, err
	}
	// `environment: ${{ inputs.environment }}` is the normal way to write one
	// deploy workflow that serves staging and production. Storing the
	// expression verbatim would file every deployment under the same name, and
	// "which version is on production" would have no answer at all.
	for i := range jobs {
		if jobs[i].Environment == "" {
			continue
		}
		name, err := gate.Interpolate(jobs[i].Environment, env)
		if err != nil {
			return 0, fmt.Errorf("job %q environment: %w", jobs[i].Key, err)
		}
		if name == "" {
			return 0, fmt.Errorf("job %q: environment resolved to nothing (%q)",
				jobs[i].Key, jobs[i].Environment)
		}
		jobs[i].Environment = name
		if url, err := gate.Interpolate(jobs[i].EnvironmentURL, env); err == nil {
			jobs[i].EnvironmentURL = url
		}
	}
	id, err := s.st.CreateRun(ctx, *run, jobs)
	if err != nil {
		return 0, err
	}
	run.ID = id
	s.wake.broadcast()
	return id, nil
}

func anyMap(m map[string]string) map[string]any {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// applyConcurrency resolves the run's concurrency group.
//
// The group is an expression — almost always `${{ github.workflow }}-${{
// github.ref }}` — and means nothing until the run's own context fills it in.
// A workflow that declares none gets the platform default, which is the
// difference between "GitHub has this feature and you forgot to use it" and
// "two pushes cannot deploy at the same time".
func (s *Server) applyConcurrency(wf *workflow.Workflow, run *store.Run, env gate.Env) error {
	c, err := wf.Concurrency()
	if err != nil {
		return err
	}
	expr := s.cfg.DefaultConcurrency
	if c != nil {
		expr, run.CancelInProgress = c.Group, c.CancelInProgress
	}
	if expr == "" {
		return nil
	}
	group, err := gate.Interpolate(expr, env)
	if err != nil {
		return fmt.Errorf("concurrency group: %w", err)
	}
	// Scope the group to the repository. Two repositories that happen to have a
	// workflow called "Deploy" are not competing for anything.
	run.ConcurrencyGroup = run.Repo + "/" + group
	return nil
}
