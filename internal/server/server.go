// Package server is Orrery's control plane: the runner RPCs, the submit API,
// and the reaper.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// Config tunes the control plane.
type Config struct {
	// RegistrationToken is the shared secret a runner presents once, to trade
	// for its own long-lived token.
	RegistrationToken string
	// FetchHold is how long FetchTask parks a caller that is already current.
	// GitHub holds 50s; we default lower so a stuck server is noticed sooner.
	FetchHold time.Duration
	// StopGrace is how long a runner has to acknowledge a stop before the
	// reaper force-terminates it and records that cleanup never ran.
	StopGrace time.Duration
	// ReaperInterval is how often timeouts and unacked stops are swept.
	ReaperInterval time.Duration
}

func (c *Config) withDefaults() {
	if c.FetchHold <= 0 {
		c.FetchHold = 20 * time.Second
	}
	if c.StopGrace <= 0 {
		c.StopGrace = 30 * time.Second
	}
	if c.ReaperInterval <= 0 {
		c.ReaperInterval = 5 * time.Second
	}
}

// Server wires the store to HTTP.
type Server struct {
	st   *store.Store
	cfg  Config
	log  *slog.Logger
	mux  *http.ServeMux
	wake *notifier
}

// New builds a server.
func New(st *store.Store, cfg Config, log *slog.Logger) *Server {
	cfg.withDefaults()
	s := &Server{st: st, cfg: cfg, log: log, mux: http.NewServeMux(), wake: newNotifier()}
	s.routes()
	return s
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

func (s *Server) routes() {
	p := protocol.ServicePath
	s.mux.HandleFunc("POST "+p+"Register", s.handleRegister)
	s.mux.HandleFunc("POST "+p+"Declare", s.authed(s.handleDeclare))
	s.mux.HandleFunc("POST "+p+"FetchTask", s.authed(s.handleFetchTask))
	s.mux.HandleFunc("POST "+p+"UpdateTask", s.authed(s.handleUpdateTask))
	s.mux.HandleFunc("POST "+p+"UpdateLog", s.authed(s.handleUpdateLog))

	s.mux.HandleFunc("POST /api/runs", s.handleSubmitRun)
	s.mux.HandleFunc("GET /api/runs", s.handleListRuns)
	s.mux.HandleFunc("GET /api/runs/{id}", s.handleGetRun)
	s.mux.HandleFunc("GET /api/jobs/{id}/logs", s.handleJobLogs)
	s.mux.HandleFunc("POST /api/jobs/{id}/stop", s.handleStopJob)
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
}

// ------------------------------------------------------------------ plumbing

type ctxKey int

const runnerKey ctxKey = iota

func (s *Server) authed(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := bearer(r)
		if tok == "" {
			writeErr(w, http.StatusUnauthorized, "missing runner token")
			return
		}
		runner, err := s.st.RunnerByToken(r.Context(), tok)
		if errors.Is(err, store.ErrNotFound) {
			// 401 means stop, do not retry with the same credential.
			writeErr(w, http.StatusUnauthorized, "unknown runner token")
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), runnerKey, runner)))
	}
}

func runnerFrom(ctx context.Context) *store.Runner {
	r, _ := ctx.Value(runnerKey).(*store.Runner)
	return r
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if after, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(after)
	}
	return ""
}

func decode[T any](r *http.Request) (*T, error) {
	var v T
	if err := json.NewDecoder(r.Body).Decode(&v); err != nil {
		return nil, fmt.Errorf("decode body: %w", err)
	}
	return &v, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// notifier lets FetchTask park until work appears instead of hot-polling.
type notifier struct {
	mu sync.Mutex
	ch chan struct{}
}

func newNotifier() *notifier { return &notifier{ch: make(chan struct{})} }

func (n *notifier) wait() <-chan struct{} {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.ch
}

func (n *notifier) broadcast() {
	n.mu.Lock()
	defer n.mu.Unlock()
	close(n.ch)
	n.ch = make(chan struct{})
}

// --------------------------------------------------------------- runner RPCs

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	req, err := decode[protocol.RegisterRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.cfg.RegistrationToken == "" || req.Token != s.cfg.RegistrationToken {
		writeErr(w, http.StatusUnauthorized, "bad registration token")
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	runner, token, err := s.st.RegisterRunner(r.Context(), req.Name, req.Version, req.Labels, req.Capabilities, req.Ephemeral)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("runner registered", "name", runner.Name, "labels", runner.Labels,
		"capabilities", req.Capabilities, "cancelling", runner.Supports(protocol.CapabilityCancelling))
	writeJSON(w, http.StatusOK, protocol.RegisterResponse{Runner: &protocol.Runner{
		ID: runner.ID, UUID: runner.UUID, Token: token, Name: runner.Name,
		Status: protocol.RunnerStatusIdle, Version: runner.Version,
		Labels: runner.Labels, Ephemral: runner.Ephemeral,
	}})
}

func (s *Server) handleDeclare(w http.ResponseWriter, r *http.Request) {
	req, err := decode[protocol.DeclareRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	runner := runnerFrom(r.Context())
	if err := s.st.DeclareRunner(r.Context(), runner.ID, req.Version, req.Labels, req.Capabilities); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.DeclareResponse{Runner: &protocol.Runner{
		ID: runner.ID, UUID: runner.UUID, Name: runner.Name, Status: protocol.RunnerStatusIdle,
		Version: req.Version, Labels: req.Labels, Ephemral: runner.Ephemeral,
	}})
}

// handleFetchTask hands out at most one job. A caller whose tasks_version is
// already current is parked for up to FetchHold rather than told "nothing" —
// the runner needs no inbound port and we need no message broker.
func (s *Server) handleFetchTask(w http.ResponseWriter, r *http.Request) {
	req, err := decode[protocol.FetchTaskRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	runner := runnerFrom(r.Context())

	task, version, err := s.tryClaim(r.Context(), runner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if task != nil || version != req.TasksVersion {
		writeJSON(w, http.StatusOK, protocol.FetchTaskResponse{Task: task, TasksVersion: version})
		return
	}

	wake := s.wake.wait()
	select {
	case <-wake:
		task, version, err = s.tryClaim(r.Context(), runner)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	case <-time.After(s.cfg.FetchHold):
	case <-r.Context().Done():
		return
	}
	writeJSON(w, http.StatusOK, protocol.FetchTaskResponse{Task: task, TasksVersion: version})
}

func (s *Server) tryClaim(ctx context.Context, runner *store.Runner) (*protocol.Task, int64, error) {
	version, err := s.st.TasksVersion(ctx)
	if err != nil {
		return nil, 0, err
	}
	job, err := s.st.ClaimJob(ctx, runner)
	if errors.Is(err, store.ErrNotFound) {
		return nil, version, nil
	}
	if err != nil {
		return nil, version, err
	}
	outputs, results, err := s.st.NeedsContext(ctx, job.RunID, job.Needs)
	if err != nil {
		return nil, version, err
	}
	needs := map[string]protocol.TaskNeed{}
	for k, v := range outputs {
		needs[k] = protocol.TaskNeed{Outputs: v, Result: protocol.Result(results[k])}
	}
	s.log.Info("task dispatched", "job", job.ID, "key", job.Key, "runner", runner.Name, "timeout_min", job.TimeoutMinutes)
	return &protocol.Task{
		ID:              job.ID,
		WorkflowPayload: []byte(job.Payload),
		Needs:           needs,
		TimeoutMinutes:  job.TimeoutMinutes,
	}, version, nil
}

// handleUpdateTask records progress. The reply carries StopRequested, which is
// how a runner learns to wind down — there is no server-to-runner channel, so
// the signal rides the answer to the runner's own heartbeat.
func (s *Server) handleUpdateTask(w http.ResponseWriter, r *http.Request) {
	req, err := decode[protocol.UpdateTaskRequest](r)
	if err != nil || req.State == nil {
		writeErr(w, http.StatusBadRequest, "state is required")
		return
	}
	ctx := r.Context()
	jobID := req.State.ID

	job, err := s.st.JobByID(ctx, jobID)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "unknown task")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}

	if len(req.State.Steps) > 0 {
		steps := make([]store.StepReport, 0, len(req.State.Steps))
		for i, st := range req.State.Steps {
			steps = append(steps, store.StepReport{
				Index: i, Result: string(st.Result),
				StartedAt: st.StartedAt, StoppedAt: st.StoppedAt,
				LogIndex: st.LogIndex, LogLength: st.LogLength,
			})
		}
		if err := s.st.SetSteps(ctx, jobID, steps); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	var sent []string
	if len(req.Outputs) > 0 {
		if sent, err = s.st.SetOutputs(ctx, jobID, req.Outputs); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	// The runner confirming it wound down is the half that makes a stop real.
	if req.State.StopAckedAt != nil {
		if err := s.st.AckStop(ctx, jobID, *req.State.StopAckedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.log.Info("stop acknowledged", "job", jobID)
	}

	if req.State.Result.Done() {
		if err := s.st.FinishJob(ctx, jobID, string(req.State.Result)); err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		s.log.Info("task finished", "job", jobID, "result", req.State.Result)
		s.wake.broadcast()
	}

	pending, err := s.st.StopPending(ctx, jobID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.UpdateTaskResponse{
		State:         &protocol.TaskState{ID: jobID, Result: protocol.Result(job.Result)},
		SentOutputs:   sent,
		StopRequested: pending,
	})
}

func (s *Server) handleUpdateLog(w http.ResponseWriter, r *http.Request) {
	req, err := decode[protocol.UpdateLogRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	lines := make([]store.LogLine, 0, len(req.Rows))
	for _, row := range req.Rows {
		lines = append(lines, store.LogLine{Time: row.Time, Content: row.Content})
	}
	ack, err := s.st.AppendLogs(r.Context(), req.TaskID, req.Index, lines, req.NoMore)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, protocol.UpdateLogResponse{AckIndex: ack})
}

// ----------------------------------------------------------------- submit API

// SubmitRequest asks the server to run a workflow file.
type SubmitRequest struct {
	Repo         string `json:"repo"`
	WorkflowFile string `json:"workflow_file"`
	Workflow     string `json:"workflow"`
	Event        string `json:"event"`
	Ref          string `json:"ref"`
	SHA          string `json:"sha"`
	Actor        string `json:"actor"`
}

// SubmitResponse is what the caller gets back.
type SubmitResponse struct {
	RunID int64    `json:"run_id"`
	Jobs  []string `json:"jobs"`
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	req, err := decode[SubmitRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	wf, err := workflow.Parse([]byte(req.Workflow))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Reject what we cannot execute at submit time rather than letting a runner
	// discover it halfway through a job.
	if bad := wf.UnsupportedSteps(); len(bad) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error":       "workflow uses features this build cannot execute",
			"unsupported": bad,
		})
		return
	}

	jobs := make([]store.NewJob, 0, len(wf.JobOrder))
	for _, key := range wf.JobOrder {
		j := wf.Jobs[key]
		payload, err := wf.JobPayload(key)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		jobs = append(jobs, store.NewJob{
			Key: key, Name: j.Name, Needs: j.Needs, RunsOn: j.RunsOn,
			Payload: string(payload), TimeoutMinutes: j.TimeoutMinutes,
		})
	}
	run := store.Run{
		Repo: req.Repo, WorkflowName: wf.Name, WorkflowFile: req.WorkflowFile,
		Event: orDefault(req.Event, "manual"), Ref: req.Ref, SHA: req.SHA, Actor: req.Actor,
	}
	runID, err := s.st.CreateRun(r.Context(), run, jobs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wake.broadcast()
	s.log.Info("run created", "run", runID, "workflow", wf.Name, "jobs", len(jobs))
	writeJSON(w, http.StatusOK, SubmitResponse{RunID: runID, Jobs: wf.JobOrder})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func (s *Server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.st.ListRuns(r.Context(), limit)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad run id")
		return
	}
	sum, err := s.st.RunByID(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) handleJobLogs(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad job id")
		return
	}
	lines, err := s.st.Logs(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, l := range lines {
		fmt.Fprintf(w, "%s %s\n", l.Time.UTC().Format(time.RFC3339), l.Content)
	}
}

func (s *Server) handleStopJob(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad job id")
		return
	}
	by := orDefault(r.URL.Query().Get("by"), "api")
	if err := s.st.RequestStop(r.Context(), id, by, "human"); errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "job is not stoppable")
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("stop requested", "job", id, "by", by)
	writeJSON(w, http.StatusAccepted, map[string]any{"job_id": id, "stop": "requested"})
}

// ------------------------------------------------------------------- reaper

// RunReaper sweeps for jobs past their timeout and for stops nobody
// acknowledged. It is the only thing that can end a job without the runner's
// cooperation, and it always records that the cleanup did not run.
func (s *Server) RunReaper(ctx context.Context) {
	t := time.NewTicker(s.cfg.ReaperInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		overdue, err := s.st.Overdue(ctx, int(s.cfg.StopGrace/time.Second))
		if err != nil {
			s.log.Error("reaper sweep failed", "err", err)
			continue
		}
		for _, o := range overdue {
			switch o.Reason {
			case "timeout":
				// Ask first. A runner that can cancel gets its grace period to
				// clean up before we take the job away from it.
				if err := s.st.RequestStop(ctx, o.ID, "reaper", "timeout"); err != nil && !errors.Is(err, store.ErrNotFound) {
					s.log.Error("reaper stop request failed", "job", o.ID, "err", err)
					continue
				}
				s.log.Warn("job past timeout, stop requested", "job", o.ID)
			case "stop_unacked":
				if err := s.st.ForceTerminate(ctx, o.ID, "timeout"); err != nil {
					s.log.Error("reaper force terminate failed", "job", o.ID, "err", err)
					continue
				}
				s.log.Warn("stop never acknowledged, force terminated", "job", o.ID, "cleanup_ran", false)
				s.wake.broadcast()
			}
		}
	}
}
