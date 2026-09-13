package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/forge"
	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// recordDeployment appends to the deployment ledger when a job that declared an
// `environment:` settles.
//
// Called for failures too. A deployment that failed is the most interesting row
// on the page: without it the environment silently keeps showing the version
// before, as though nothing had been attempted.
func (s *Server) recordDeployment(ctx context.Context, jobID int64, result string) {
	info, err := s.st.DeployInfo(ctx, jobID)
	if err != nil || info == nil || info.Environment == "" {
		return
	}
	outputs, err := s.st.JobOutputs(ctx, jobID)
	if err != nil {
		s.log.Error("cannot read job outputs for the deployment ledger", "job", jobID, "err", err)
	}
	env := &workflow.Environment{VersionFrom: info.VersionFrom}
	run, err := s.st.RunMeta(ctx, info.RunID)
	if err != nil {
		s.log.Error("cannot read the run for the deployment ledger", "job", jobID, "err", err)
		return
	}
	d := store.Deployment{
		Repo: run.Repo, Environment: info.Environment,
		Version: env.Version(outputs, info.SHA), SHA: info.SHA, Ref: info.Ref,
		URL: info.URL, RunID: info.RunID, JobID: jobID,
		WorkflowFile: info.WorkflowFile, Actor: info.Actor, Result: result,
	}
	id, err := s.st.RecordDeployment(ctx, d)
	if err != nil {
		s.log.Error("cannot record the deployment", "job", jobID, "err", err)
		return
	}
	s.log.Info("deployment recorded", "id", id, "env", d.Environment,
		"version", d.Version, "result", result)

	if result == "success" || !info.AutoRollback {
		return
	}
	// A failed deployment with auto-rollback: put the last good version back.
	// Detached, because the caller is a runner reporting a finished task and
	// rolling back means dispatching a whole workflow.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := s.rollback(ctx, run.Repo, info.Environment, id, "auto-rollback"); err != nil {
			s.log.Error("automatic rollback failed", "env", info.Environment, "err", err)
		}
	}()
}

// rollback redeploys the last version that worked.
//
// It is a new deployment, not an undo: the workflow that deployed that version
// is dispatched again, at that version. Anything else would be pretending we
// can reach into a server and put a file back — we cannot, and the deploy
// workflow is the only thing that knows how.
//
// This needs the deploy workflow to accept a version input; without one there
// is nothing to tell it which version to put back, and the honest answer is to
// say so rather than redeploy the newest code under the name "rollback".
func (s *Server) rollback(ctx context.Context, repo, env string, from int64, by string) (int64, error) {
	good, err := s.st.LastGoodDeployment(ctx, repo, env, from)
	if errors.Is(err, store.ErrNotFound) {
		return 0, fmt.Errorf("%s/%s has no earlier successful deployment to go back to", repo, env)
	}
	if err != nil {
		return 0, err
	}
	files, err := s.forge.Workflows(ctx, repo, good.SHA)
	if err != nil {
		return 0, fmt.Errorf("read workflows at %s: %w", good.SHA, err)
	}
	var file *forge.File
	for i := range files {
		if files[i].Path == good.WorkflowFile {
			file = &files[i]
			break
		}
	}
	if file == nil {
		return 0, fmt.Errorf("%s is no longer in %s at %s", good.WorkflowFile, repo, good.SHA)
	}
	wf, err := workflow.Parse(file.Content)
	if err != nil {
		return 0, err
	}
	declared, dispatchable, err := wf.DispatchInputs()
	if err != nil {
		return 0, err
	}
	if !dispatchable {
		return 0, fmt.Errorf("%s does not declare `on: workflow_dispatch`; a rollback has no way to "+
			"ask it to deploy %s", good.WorkflowFile, good.Version)
	}
	// Hand the version back through whichever input the workflow offers for it.
	inputs := map[string]string{}
	versionInput := ""
	for _, name := range workflow.DefaultVersionOutputs {
		if _, ok := declared[name]; ok {
			versionInput = name
			break
		}
	}
	if versionInput == "" {
		return 0, fmt.Errorf("%s declares no version input (%v); without one a rollback would "+
			"redeploy the newest code under the name of an old version",
			good.WorkflowFile, workflow.DefaultVersionOutputs)
	}
	inputs[versionInput] = good.Version
	if _, ok := declared["environment"]; ok {
		inputs["environment"] = env
	}
	values, err := workflow.ValidateDispatch(declared, inputs)
	if err != nil {
		return 0, err
	}
	runID, err := s.startDispatch(ctx, repo, file, wf, good.SHA, good.Ref, by, values)
	if err != nil {
		return 0, err
	}
	s.log.Info("rolled back", "repo", repo, "env", env, "to", good.Version,
		"run", runID, "by", by)
	return runID, nil
}

// ---------------------------------------------------------------- HTTP --

func (s *Server) handleDeployments(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if env := r.URL.Query().Get("environment"); env != "" {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		history, err := s.st.DeploymentHistory(r.Context(), repo, env, limit)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, history)
		return
	}
	current, err := s.st.CurrentDeployments(r.Context(), repo)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, current)
}

func (s *Server) handleRollback(w http.ResponseWriter, r *http.Request) {
	req, err := decode[struct {
		Repo        string `json:"repo"`
		Environment string `json:"environment"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Repo == "" || req.Environment == "" {
		writeErr(w, http.StatusBadRequest, "repo and environment are required")
		return
	}
	if !s.repoAllowed(req.Repo) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("repository %q is not in -repos", req.Repo))
		return
	}
	// from 0: go back from wherever the environment is now.
	runID, err := s.rollback(r.Context(), req.Repo, req.Environment, 0, "ui")
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run_id": runID})
}
