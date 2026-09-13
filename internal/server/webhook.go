package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"

	"github.com/feed-mob/feedmob-orrery/internal/forge"
	"github.com/feed-mob/feedmob-orrery/internal/store"
	"github.com/feed-mob/feedmob-orrery/internal/workflow"
)

// maxWebhookBody bounds what we will read from a delivery. GitHub's own limit
// is 25MB; we read a little past it and refuse the rest rather than letting an
// unauthenticated caller decide how much memory to take.
const maxWebhookBody = 26 << 20

// handleGitHubWebhook turns a forge event into runs.
//
// Everything here is untrusted until the signature checks out: the body is read
// but nothing is parsed, matched or dispatched before that.
func (s *Server) handleGitHubWebhook(w http.ResponseWriter, r *http.Request) {
	if s.cfg.WebhookSecret == "" {
		// Refusing is the only safe default. An unsigned webhook endpoint lets
		// anyone who can reach the port start a run on any repository, with
		// whatever secrets that run is configured to receive.
		writeErr(w, http.StatusServiceUnavailable, "webhooks are not configured: set -webhook-secret")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "cannot read body")
		return
	}
	if !validSignature(s.cfg.WebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
		s.log.Warn("rejected webhook with a bad signature",
			"delivery", r.Header.Get("X-GitHub-Delivery"), "event", r.Header.Get("X-GitHub-Event"))
		writeErr(w, http.StatusUnauthorized, "bad signature")
		return
	}

	event := r.Header.Get("X-GitHub-Event")
	delivery := r.Header.Get("X-GitHub-Delivery")
	if event == "ping" {
		writeJSON(w, http.StatusOK, map[string]string{"status": "pong"})
		return
	}

	// GitHub redelivers on timeout, and a redelivered push must not build
	// twice. The delivery id is the only stable identity a webhook carries.
	if delivery != "" {
		fresh, err := s.st.ClaimDelivery(r.Context(), delivery, event)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if !fresh {
			s.log.Info("ignored a repeated delivery", "delivery", delivery, "event", event)
			writeJSON(w, http.StatusOK, map[string]any{"status": "duplicate", "runs": []int64{}})
			return
		}
	}

	ids, err := s.dispatchEvent(r.Context(), event, body)
	if err != nil {
		s.log.Error("webhook dispatch failed", "delivery", delivery, "event", event, "err", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.log.Info("webhook dispatched", "event", event, "delivery", delivery, "runs", ids)
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "runs": ids})
}

// validSignature compares GitHub's HMAC over the raw body.
func validSignature(secret, header string, body []byte) bool {
	want, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	sum := hex.EncodeToString(mac.Sum(nil))
	// Constant time: a fast reject on the first wrong byte leaks the signature
	// one byte at a time to anyone willing to time the endpoint.
	return hmac.Equal([]byte(sum), []byte(want))
}

// hookPayload is the slice of GitHub's event we read. Everything else is passed
// through untouched as `github.event`, so a workflow can reach fields we have
// never heard of.
type hookPayload struct {
	Action     string `json:"action"`
	Ref        string `json:"ref"`
	After      string `json:"after"`
	Repository struct {
		FullName      string `json:"full_name"`
		DefaultBranch string `json:"default_branch"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	HeadCommit *struct {
		ID string `json:"id"`
	} `json:"head_commit"`
	Commits []struct {
		Added    []string `json:"added"`
		Removed  []string `json:"removed"`
		Modified []string `json:"modified"`
	} `json:"commits"`
	PullRequest *struct {
		Number int `json:"number"`
		Head   struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
		Base struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"base"`
	} `json:"pull_request"`
}

// dispatchEvent reads the repository's workflows at the triggering commit,
// keeps the ones whose `on:` accepts this event, and queues a run for each.
func (s *Server) dispatchEvent(ctx context.Context, event string, body []byte) ([]int64, error) {
	var p hookPayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", event, err)
	}
	if p.Repository.FullName == "" {
		return nil, fmt.Errorf("%s payload names no repository", event)
	}
	// The signature proves the sender knows the shared secret, not that the
	// repository it names is one we build. Every repo pointing at this server
	// shares that secret, so without a list any of them — or anyone the secret
	// leaks to — could name someone else's repository and have us read its
	// workflows with our forge token and run them with our deploy secrets.
	if !s.repoAllowed(p.Repository.FullName) {
		s.log.Warn("refused a signed delivery for a repository that is not on the list",
			"repo", p.Repository.FullName, "event", event)
		return nil, fmt.Errorf("repository %q is not in -repos", p.Repository.FullName)
	}
	// A branch deletion carries the all-zero sha: there is no tree to read a
	// workflow from, and nothing to build.
	if event == "push" && (p.After == "" || strings.Trim(p.After, "0") == "") {
		return nil, nil
	}

	ev, sha := eventFor(event, &p)
	if sha == "" {
		return nil, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode %s payload: %w", event, err)
	}

	files, err := s.forge.Workflows(ctx, p.Repository.FullName, sha)
	if err != nil {
		return nil, fmt.Errorf("read workflows at %s: %w", sha, err)
	}

	// A push to the default branch is the moment a schedule change takes
	// effect, because the default branch is the only place GitHub honours
	// `on: schedule` from — and so the only place where reviewing a cron
	// change means anything.
	if event == "push" && p.Repository.DefaultBranch != "" &&
		ev.Ref == "refs/heads/"+p.Repository.DefaultBranch {
		if err := s.refreshSchedules(ctx, p.Repository.FullName, p.Repository.DefaultBranch, files); err != nil {
			// The push still produces its runs; only the crons are stale.
			s.log.Error("refreshing schedules failed", "repo", p.Repository.FullName, "err", err)
		}
	}

	var ids []int64
	for _, f := range files {
		wf, err := workflow.Parse(f.Content)
		if err != nil {
			// One unparseable file must not stop the others: a broken workflow
			// should fail loudly on its own, not silently disable the repo.
			s.log.Warn("skipping unparseable workflow", "repo", p.Repository.FullName,
				"file", f.Path, "err", err)
			continue
		}
		if _, ok, err := wf.Matches(ev); err != nil {
			s.log.Warn("skipping workflow with a bad `on:`", "file", f.Path, "err", err)
			continue
		} else if !ok {
			continue
		}
		id, err := s.queueRun(ctx, wf, f, p, ev, sha, raw)
		if err != nil {
			return ids, fmt.Errorf("queue %s: %w", f.Path, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// eventFor maps a webhook into the shape `on:` filters against, and says which
// commit the workflows should be read from.
func eventFor(event string, p *hookPayload) (workflow.Event, string) {
	ev := workflow.Event{Name: event, Action: p.Action, Ref: p.Ref}
	sha := p.After
	if sha == "" && p.HeadCommit != nil {
		sha = p.HeadCommit.ID
	}
	switch {
	case strings.HasPrefix(event, "pull_request") && p.PullRequest != nil:
		ev.Ref = "refs/heads/" + p.PullRequest.Head.Ref
		ev.BaseRef = p.PullRequest.Base.Ref
		// Read the workflows from the head of the PR, so a pull request can
		// change its own CI — the same rule GitHub applies to `pull_request`
		// (and pointedly not to `pull_request_target`, which we do not accept).
		sha = p.PullRequest.Head.SHA
	case event == "push":
		for _, c := range p.Commits {
			ev.Paths = append(ev.Paths, c.Added...)
			ev.Paths = append(ev.Paths, c.Modified...)
			ev.Paths = append(ev.Paths, c.Removed...)
		}
	}
	return ev, sha
}

func (s *Server) queueRun(ctx context.Context, wf *workflow.Workflow, f forge.File,
	p hookPayload, ev workflow.Event, sha string, raw map[string]any) (int64, error) {
	jobs, err := newJobsFor(wf, f.Content)
	if err != nil {
		return 0, err
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return 0, err
	}
	run := store.Run{
		Repo:         p.Repository.FullName,
		WorkflowName: wf.Name,
		WorkflowFile: f.Path,
		Event:        ev.Name,
		Ref:          ev.Ref,
		SHA:          sha,
		Actor:        p.Sender.Login,
		EventPayload: string(payload),
	}
	if err := s.applyConcurrency(wf, &run); err != nil {
		return 0, err
	}
	id, err := s.st.CreateRun(ctx, run, jobs)
	if err != nil {
		return 0, err
	}
	s.wake.broadcast()
	// Report pending immediately. A required check that only appears once the
	// run finishes gives a reviewer a green pull request in the window where
	// nothing has been checked yet.
	run.ID = id
	s.reportStatus(&run, forge.StatePending, fmt.Sprintf("%d job(s) queued", len(jobs)))
	return id, nil
}

// ------------------------------------------------------------- dispatch --

// DispatchRequest is a manual run: the `workflow_dispatch` button, as an API.
type DispatchRequest struct {
	Repo         string            `json:"repo"`
	WorkflowFile string            `json:"workflow_file"`
	Ref          string            `json:"ref"`
	Actor        string            `json:"actor"`
	Inputs       map[string]string `json:"inputs"`
}

// handleDispatch starts a run of one workflow, read from the forge at a ref.
//
// This is how a deploy or a rollback gets triggered by a person rather than by
// a commit, and it is the only trigger where the inputs come from outside the
// repository — which is why they are checked against what the workflow
// declared before anything starts.
func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	req, err := decode[DispatchRequest](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Repo == "" || req.WorkflowFile == "" {
		writeErr(w, http.StatusBadRequest, "repo and workflow_file are required")
		return
	}
	if !s.repoAllowed(req.Repo) {
		writeErr(w, http.StatusForbidden, fmt.Sprintf("repository %q is not in -repos", req.Repo))
		return
	}
	ref := req.Ref
	if ref == "" {
		ref = "HEAD"
	}
	ctx := r.Context()

	files, err := s.forge.Workflows(ctx, req.Repo, ref)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	var file *forge.File
	for i := range files {
		if files[i].Path == req.WorkflowFile || path.Base(files[i].Path) == req.WorkflowFile {
			file = &files[i]
			break
		}
	}
	if file == nil {
		writeErr(w, http.StatusNotFound, fmt.Sprintf("no workflow %q in %s at %s", req.WorkflowFile, req.Repo, ref))
		return
	}

	wf, err := workflow.Parse(file.Content)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	declared, ok, err := wf.DispatchInputs()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !ok {
		writeErr(w, http.StatusBadRequest,
			fmt.Sprintf("%s does not declare `on: workflow_dispatch`", file.Path))
		return
	}
	inputs, err := workflow.ValidateDispatch(declared, req.Inputs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	jobs, err := newJobsFor(wf, file.Content)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// GitHub puts the values under github.event.inputs, and workflows read them
	// there; `inputs.*` is the newer spelling of the same thing.
	payload, err := json.Marshal(map[string]any{
		"inputs":   inputs,
		"ref":      ref,
		"workflow": file.Path,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	run := store.Run{
		Repo: req.Repo, WorkflowName: wf.Name, WorkflowFile: file.Path,
		Event: "workflow_dispatch", Ref: ref, Actor: orDefault(req.Actor, "api"),
		EventPayload: string(payload),
	}
	if err := s.applyConcurrency(wf, &run); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	runID, err := s.st.CreateRun(ctx, run, jobs)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.wake.broadcast()
	s.log.Info("workflow dispatched", "run", runID, "repo", req.Repo,
		"workflow", file.Path, "ref", ref, "inputs", keysOfAny(inputs))
	writeJSON(w, http.StatusOK, SubmitResponse{RunID: runID, Jobs: wf.JobOrder})
}

// keysOfAny logs which inputs were supplied without logging their values: a
// dispatch input is a plausible place for someone to paste a token.
func keysOfAny(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
