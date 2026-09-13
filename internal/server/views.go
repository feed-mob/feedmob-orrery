package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// handleQueryRuns is the dashboard's list: filtered, paged, and with a total so
// the page can say "50 of 1,204" instead of pretending fifty is all there is.
func (s *Server) handleQueryRuns(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	page, err := s.st.QueryRuns(r.Context(), store.RunFilter{
		Repo:     q.Get("repo"),
		Workflow: q.Get("workflow"),
		Status:   q.Get("status"),
		Result:   q.Get("result"),
		Event:    q.Get("event"),
		Actor:    q.Get("actor"),
		Branch:   q.Get("branch"),
		Before:   before,
		Limit:    limit,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) handleFacets(w http.ResponseWriter, r *http.Request) {
	f, err := s.st.Facets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// runnerView is what the dashboard shows about a runner. Health is computed
// here rather than in the browser so the CLI and the page agree on when a
// runner counts as missing.
type runnerView struct {
	store.Runner
	// Health is one of online, idle-long, offline. A runner is expected to poll
	// continuously, so silence is the signal — "last seen 40 minutes ago" means
	// it is gone, not that it is quiet.
	Health string `json:"Health"`
	Since  string `json:"Since"`
}

const (
	runnerStale   = 2 * time.Minute
	runnerMissing = 15 * time.Minute
)

func (s *Server) handleRunners(w http.ResponseWriter, r *http.Request) {
	runners, err := s.st.Runners(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	now := time.Now().UTC()
	out := make([]runnerView, 0, len(runners))
	for _, rn := range runners {
		age := now.Sub(rn.LastSeenAt)
		health := "online"
		switch {
		case age > runnerMissing:
			health = "offline"
		case age > runnerStale:
			health = "stale"
		}
		out = append(out, runnerView{Runner: rn, Health: health, Since: age.Round(time.Second).String()})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleSchedules(w http.ResponseWriter, r *http.Request) {
	scheds, err := s.st.Schedules(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, scheds)
}

// handleCancelRun stops a whole run in one call.
func (s *Server) handleCancelRun(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad run id")
		return
	}
	by := orDefault(r.URL.Query().Get("by"), "api")
	outcome, err := s.st.CancelRun(r.Context(), id, by, "human")
	if errors.Is(err, store.ErrNotFound) {
		writeErr(w, http.StatusNotFound, "no such run")
		return
	}
	if err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	s.announce(r.Context(), outcome)
	s.wake.broadcast()
	s.log.Info("run cancelled", "run", id, "by", by)
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": id, "cancel": "requested"})
}

// handleWorkflowsOf lists the workflows in a repository at a ref, so the
// dispatch form can offer a list instead of asking someone to type a path.
func (s *Server) handleWorkflowsOf(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		writeErr(w, http.StatusBadRequest, "repo is required")
		return
	}
	if !s.repoAllowed(repo) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("repository %q is not in -repos", repo))
		return
	}
	ref := orDefault(r.URL.Query().Get("ref"), "HEAD")
	files, err := s.forge.Workflows(r.Context(), repo, ref)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	type entry struct {
		Path   string                           `json:"path"`
		Name   string                           `json:"name"`
		Inputs map[string]workflowDispatchInput `json:"inputs,omitempty"`
	}
	out := []entry{}
	for _, f := range files {
		wf, err := workflow.Parse(f.Content)
		if err != nil {
			continue
		}
		declared, ok, err := wf.DispatchInputs()
		if err != nil || !ok {
			// Only workflows that offer the button belong on a form that
			// presses it.
			continue
		}
		e := entry{Path: f.Path, Name: orDefault(wf.Name, f.Path)}
		if len(declared) > 0 {
			e.Inputs = map[string]workflowDispatchInput{}
			for name, in := range declared {
				e.Inputs[name] = workflowDispatchInput{
					Description: in.Description, Required: in.Required,
					Default: in.Default, Type: in.Type, Options: in.Options,
				}
			}
		}
		out = append(out, e)
	}
	writeJSON(w, http.StatusOK, out)
}

// workflowDispatchInput mirrors the workflow package's type over the wire, so
// the form can render the right control and validate before submitting.
type workflowDispatchInput struct {
	Description string   `json:"description,omitempty"`
	Required    bool     `json:"required,omitempty"`
	Default     string   `json:"default,omitempty"`
	Type        string   `json:"type,omitempty"`
	Options     []string `json:"options,omitempty"`
}

// config tells the dashboard what this server can do, so the page offers only
// what will work: no dispatch form when there is no forge token to read
// workflows with.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"forge_url":     s.cfg.Forge.URL,
		"can_dispatch":  s.cfg.ForgeToken != "",
		"repos":         s.cfg.Repos,
		"retention":     s.cfg.Retention.String(),
		"auth_required": s.cfg.APIToken != "",
	})
}

// handleUsage reports machine time by repository and workflow.
//
// Minutes, not money: what a minute costs depends on where the runner runs, and
// a number pretending to be dollars while nobody has told it the machine's
// price is worse than no number.
func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 365 {
		days = 30
	}
	usage, err := s.st.UsageSince(r.Context(), time.Now().UTC().AddDate(0, 0, -days))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var totalMillis int64
	var totalRuns, totalJobs int
	for _, u := range usage {
		totalMillis += u.Millis
		totalRuns += u.Runs
		totalJobs += u.Jobs
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"days": days, "rows": usage,
		"total_millis": totalMillis, "total_runs": totalRuns, "total_jobs": totalJobs,
	})
}
