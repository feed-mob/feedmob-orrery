package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/forge"
	"github.com/feed-mob/feedmob-orrery/internal/store"
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
}
