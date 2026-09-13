package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
	"github.com/feed-mob/feedmob-orrery/internal/store"
)

const hookSecret = "s3cret"

// fakeForge stands in for GitHub: it serves one workflow file and records the
// commit statuses written back to it.
type fakeForge struct {
	workflow string
	statuses chan map[string]any
}

func (f *fakeForge) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/{owner}/{repo}/contents/{path...}", func(w http.ResponseWriter, r *http.Request) {
		path := r.PathValue("path")
		if path == ".github/workflows" {
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"name": "ci.yml", "path": ".github/workflows/ci.yml", "type": "file"},
				{"name": "notes.md", "path": ".github/workflows/notes.md", "type": "file"},
			})
			return
		}
		_, _ = io.WriteString(w, f.workflow)
	})
	// Resolving a ref to a commit: a dispatched or scheduled run carries a
	// branch and needs the sha that branch points at.
	mux.HandleFunc("GET /repos/{owner}/{repo}/commits/{ref}", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "beefcafebeefcafebeefcafebeefcafebeefcafe")
	})
	mux.HandleFunc("POST /repos/{owner}/{repo}/statuses/{sha}", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["sha"] = r.PathValue("sha")
		select {
		case f.statuses <- body:
		default:
		}
		w.WriteHeader(http.StatusCreated)
	})
	return mux
}

func webhookServer(t *testing.T, wf string) (*httptest.Server, *store.Store, *fakeForge) {
	t.Helper()
	forge := &fakeForge{workflow: wf, statuses: make(chan map[string]any, 8)}
	gh := httptest.NewServer(forge.handler())

	st, err := store.Open(filepath.Join(t.TempDir(), "hook.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	srv := New(st, Config{
		RegistrationToken: regToken,
		FetchHold:         50 * time.Millisecond,
		Forge:             Forge{URL: "https://github.com", APIURL: gh.URL},
		ForgeToken:        "t0ken",
		WebhookSecret:     hookSecret,
		PublicURL:         "https://orrery.internal",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); gh.Close(); st.Close() })
	return hs, st, forge
}

func deliver(t *testing.T, base, event, delivery string, payload any, sign bool) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/api/webhooks/github", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-GitHub-Delivery", delivery)
	if sign {
		mac := hmac.New(sha256.New, []byte(hookSecret))
		mac.Write(body)
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	} else {
		req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func pushPayload(ref, sha string, paths ...string) map[string]any {
	return map[string]any{
		"ref":        ref,
		"after":      sha,
		"repository": map[string]any{"full_name": "feed-mob/app", "default_branch": "main"},
		"sender":     map[string]any{"login": "someone"},
		"commits":    []map[string]any{{"modified": paths}},
	}
}

const ciWorkflow = `
name: CI
on:
  push:
    branches: [main]
jobs:
  build:
    runs-on: [self-hosted]
    steps:
      - run: echo hi
`

// An unsigned webhook endpoint lets anyone who can reach the port start a run
// on any repository, with whatever secrets that run is configured to receive.
func TestWebhookRejectsABadSignature(t *testing.T) {
	hs, st, _ := webhookServer(t, ciWorkflow)
	code, _ := deliver(t, hs.URL, "push", "d1", pushPayload("refs/heads/main", "abc123"), false)
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
	runs, _ := st.ListRuns(t.Context(), 10)
	if len(runs) != 0 {
		t.Fatalf("an unsigned delivery created %d runs", len(runs))
	}
}

func TestWebhookQueuesAMatchingWorkflow(t *testing.T) {
	hs, st, forge := webhookServer(t, ciWorkflow)
	code, body := deliver(t, hs.URL, "push", "d1", pushPayload("refs/heads/main", "abc123", "main.go"), true)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%v)", code, body)
	}
	runs, err := st.ListRuns(t.Context(), 10)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("got %d runs, want 1", len(runs))
	}
	r := runs[0]
	if r.Repo != "feed-mob/app" || r.SHA != "abc123" || r.Event != "push" || r.WorkflowName != "CI" {
		t.Errorf("run = %+v", r)
	}
	// The event is stored verbatim so `github.event` can carry fields we never
	// modelled. It is read on the dispatch path, not listed: a run listing does
	// not need to carry a 25MB payload per row.
	meta, err := st.RunMeta(t.Context(), r.ID)
	if err != nil {
		t.Fatalf("run meta: %v", err)
	}
	var event map[string]any
	if err := json.Unmarshal([]byte(meta.EventPayload), &event); err != nil {
		t.Fatalf("stored event payload is not the delivery we sent: %v", err)
	}
	if repo, _ := event["repository"].(map[string]any); repo["full_name"] != "feed-mob/app" {
		t.Errorf("event payload lost the repository: %+v", event)
	}

	// A required check that only appears once the run finishes gives a reviewer
	// a green pull request in the window where nothing has been checked.
	select {
	case st := <-forge.statuses:
		if st["state"] != "pending" || st["context"] != "orrery / CI" || st["sha"] != "abc123" {
			t.Errorf("pending status = %+v", st)
		}
		if st["target_url"] != fmt.Sprintf("https://orrery.internal/runs/%d", r.ID) {
			t.Errorf("target_url = %v", st["target_url"])
		}
	case <-time.After(2 * time.Second):
		t.Error("no pending commit status was written")
	}
}

// A workflow whose `on:` does not accept the event must not run.
func TestWebhookIgnoresANonMatchingEvent(t *testing.T) {
	hs, st, _ := webhookServer(t, ciWorkflow)
	code, _ := deliver(t, hs.URL, "push", "d1", pushPayload("refs/heads/feature", "abc123", "main.go"), true)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 0 {
		t.Fatalf("a push to an unfiltered branch created %d runs", len(runs))
	}
}

// GitHub redelivers on timeout and on a manual redeliver; a redelivered push
// must not build twice.
func TestWebhookIsIdempotentPerDelivery(t *testing.T) {
	hs, st, _ := webhookServer(t, ciWorkflow)
	payload := pushPayload("refs/heads/main", "abc123", "main.go")
	for i := 0; i < 3; i++ {
		if code, _ := deliver(t, hs.URL, "push", "same-id", payload, true); code != http.StatusOK {
			t.Fatalf("delivery %d: status %d", i, code)
		}
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 1 {
		t.Fatalf("three deliveries of one id created %d runs", len(runs))
	}
}

// A branch deletion carries the all-zero sha: there is no tree to read a
// workflow from, and nothing to build.
func TestWebhookIgnoresABranchDeletion(t *testing.T) {
	hs, st, _ := webhookServer(t, ciWorkflow)
	zero := "0000000000000000000000000000000000000000"
	if code, _ := deliver(t, hs.URL, "push", "d1", pushPayload("refs/heads/main", zero), true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 0 {
		t.Fatalf("a branch deletion created %d runs", len(runs))
	}
}

func TestEventForReadsPullRequestsFromTheHead(t *testing.T) {
	var p hookPayload
	raw := []byte(`{"action":"opened","repository":{"full_name":"o/r"},
	  "pull_request":{"number":7,"head":{"ref":"feature","sha":"headsha"},"base":{"ref":"main","sha":"basesha"}}}`)
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ev, sha := eventFor("pull_request", &p)
	// Reading the workflows at the PR head is what lets a pull request change
	// its own CI — the rule GitHub applies to pull_request and pointedly not to
	// pull_request_target.
	if sha != "headsha" {
		t.Errorf("sha = %q, want the PR head", sha)
	}
	if ev.Ref != "refs/heads/feature" || ev.BaseRef != "main" || ev.Action != "opened" {
		t.Errorf("event = %+v", ev)
	}
}

// The final verdict is the whole point: a run that builds and reaches a result
// and never tells the forge leaves a pull request with no checks. Driven
// through the real runner path, because that is where the announcement hangs.
func TestRunResultIsReportedToTheForge(t *testing.T) {
	hs, _, forge := webhookServer(t, ciWorkflow)
	if code, _ := deliver(t, hs.URL, "push", "d1", pushPayload("refs/heads/main", "abc123", "main.go"), true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if got := waitForStatus(t, forge); got["state"] != "pending" {
		t.Fatalf("first status = %+v, want pending", got)
	}

	token := register(t, hs.URL)
	fetched, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](
		t, hs.URL, token, "FetchTask", &protocol.FetchTaskRequest{})
	if fetched.Task == nil {
		t.Fatal("no task was dispatched for the webhook-created run")
	}
	now := time.Now().UTC()
	rpc[protocol.UpdateTaskRequest, protocol.UpdateTaskResponse](t, hs.URL, token, "UpdateTask",
		&protocol.UpdateTaskRequest{State: &protocol.TaskState{
			ID: fetched.Task.ID, Result: protocol.ResultFailure, StoppedAt: &now,
		}})

	if got := waitForStatus(t, forge); got["state"] != "failure" || got["sha"] != "abc123" {
		t.Errorf("final status = %+v, want failure on abc123", got)
	}
}

func waitForStatus(t *testing.T, f *fakeForge) map[string]any {
	t.Helper()
	select {
	case st := <-f.statuses:
		return st
	case <-time.After(2 * time.Second):
		t.Fatal("no commit status was written")
		return nil
	}
}

const dispatchWorkflow = `
name: Manual Deploy
on:
  workflow_dispatch:
    inputs:
      environment:
        required: true
        type: choice
        options: [staging, production]
      version:
        default: latest
jobs:
  deploy:
    runs-on: [self-hosted]
    steps:
      - run: echo ${{ github.event.inputs.environment }}
`

func postJSON(t *testing.T, url string, body any) (int, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	res, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestDispatchValidatesInputsBeforeAnythingStarts(t *testing.T) {
	hs, st, _ := webhookServer(t, dispatchWorkflow)

	code, body := postJSON(t, hs.URL+"/api/dispatch", DispatchRequest{
		Repo: "feed-mob/app", WorkflowFile: "ci.yml", Ref: "main",
		Inputs: map[string]string{"environment": "production"},
	})
	if code != http.StatusOK {
		t.Fatalf("status = %d (%v)", code, body)
	}
	runs, _ := st.ListRuns(t.Context(), 10)
	if len(runs) != 1 || runs[0].Event != "workflow_dispatch" {
		t.Fatalf("runs = %+v", runs)
	}
	// The values have to reach the workflow where it reads them.
	meta, _ := st.RunMeta(t.Context(), runs[0].ID)
	var payload struct {
		Inputs map[string]any `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(meta.EventPayload), &payload); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	if payload.Inputs["environment"] != "production" || payload.Inputs["version"] != "latest" {
		t.Errorf("inputs = %+v, want the supplied value and the default", payload.Inputs)
	}

	// A bad value must be refused before a run exists, not after a job started.
	code, _ = postJSON(t, hs.URL+"/api/dispatch", DispatchRequest{
		Repo: "feed-mob/app", WorkflowFile: "ci.yml", Ref: "main",
		Inputs: map[string]string{"environment": "prod"},
	})
	if code != http.StatusBadRequest {
		t.Errorf("a value outside the declared choices got %d, want 400", code)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 1 {
		t.Errorf("a refused dispatch created a run")
	}
}

// A workflow that does not offer the button must not be startable by pressing it.
func TestDispatchRefusesAWorkflowThatDoesNotDeclareIt(t *testing.T) {
	hs, st, _ := webhookServer(t, ciWorkflow)
	code, body := postJSON(t, hs.URL+"/api/dispatch", DispatchRequest{
		Repo: "feed-mob/app", WorkflowFile: "ci.yml", Ref: "main",
	})
	if code != http.StatusBadRequest {
		t.Fatalf("status = %d (%v), want 400", code, body)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 0 {
		t.Error("a run was created for a workflow with no workflow_dispatch")
	}
}

const scheduledWorkflow = `
name: Nightly
on:
  push:
    branches: [main]
  schedule:
    - cron: '17 3 * * *'
    - cron: '*/30 * * * *'
jobs:
  sweep:
    runs-on: [self-hosted]
    steps:
      - run: echo sweep
`

// A cron added on a feature branch must not fire before it is merged, which is
// the only rule under which reviewing a schedule change means anything.
func TestSchedulesComeFromTheDefaultBranchOnly(t *testing.T) {
	hs, st, _ := webhookServer(t, scheduledWorkflow)

	push := pushPayload("refs/heads/feature", "abc123", "main.go")
	if code, _ := deliver(t, hs.URL, "push", "d1", push, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if due, _ := st.DueSchedules(t.Context(), time.Now().Add(24*time.Hour)); len(due) != 0 {
		t.Fatalf("a push to a feature branch registered %d schedules", len(due))
	}

	push = pushPayload("refs/heads/main", "abc123", "main.go")
	if code, _ := deliver(t, hs.URL, "push", "d2", push, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	due, err := st.DueSchedules(t.Context(), time.Now().Add(48*time.Hour))
	if err != nil {
		t.Fatalf("due: %v", err)
	}
	if len(due) != 2 {
		t.Fatalf("got %d schedules, want the workflow's two crons: %+v", len(due), due)
	}
	for _, sc := range due {
		if sc.Repo != "feed-mob/app" || sc.WorkflowFile != ".github/workflows/ci.yml" || sc.Ref != "main" {
			t.Errorf("schedule = %+v", sc)
		}
		if sc.NextDueAt.Before(time.Now()) {
			t.Errorf("schedule %q is due in the past: %v", sc.Cron, sc.NextDueAt)
		}
	}
}

// A cron deleted from the default branch has to stop firing, and its absence is
// the only signal we get.
func TestSchedulesAreReplacedNotMerged(t *testing.T) {
	forgeWF := scheduledWorkflow
	f := &fakeForge{workflow: forgeWF, statuses: make(chan map[string]any, 8)}
	gh := httptest.NewServer(f.handler())
	defer gh.Close()

	st, err := store.Open(filepath.Join(t.TempDir(), "sched.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	srv := New(st, Config{
		RegistrationToken: regToken, WebhookSecret: hookSecret,
		Forge: Forge{URL: "https://github.com", APIURL: gh.URL}, ForgeToken: "t",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	hs := httptest.NewServer(srv)
	defer hs.Close()

	push := pushPayload("refs/heads/main", "abc123", "main.go")
	if code, _ := deliver(t, hs.URL, "push", "d1", push, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if due, _ := st.DueSchedules(t.Context(), time.Now().Add(48*time.Hour)); len(due) != 2 {
		t.Fatalf("want 2 schedules first, got %d", len(due))
	}

	// The workflow loses one cron and is pushed again.
	f.workflow = strings.Replace(scheduledWorkflow, "    - cron: '*/30 * * * *'\n", "", 1)
	if code, _ := deliver(t, hs.URL, "push", "d2", push, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	due, _ := st.DueSchedules(t.Context(), time.Now().Add(48*time.Hour))
	if len(due) != 1 || due[0].Cron != "17 3 * * *" {
		t.Fatalf("after removing a cron: %+v", due)
	}
}

// An unparseable cron must not take the rest of the file down with it.
func TestABadCronIsSkippedNotFatal(t *testing.T) {
	wf := strings.Replace(scheduledWorkflow, "'*/30 * * * *'", "'not a cron'", 1)
	hs, st, _ := webhookServer(t, wf)
	push := pushPayload("refs/heads/main", "abc123", "main.go")
	if code, _ := deliver(t, hs.URL, "push", "d1", push, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	due, _ := st.DueSchedules(t.Context(), time.Now().Add(48*time.Hour))
	if len(due) != 1 || due[0].Cron != "17 3 * * *" {
		t.Fatalf("a bad cron took the good one with it: %+v", due)
	}
}

// A schedule that fires and then fails to be rescheduled would fire again on
// the next tick; a cron that runs every few seconds because its workflow is
// broken is worse than one that misses an occurrence.
func TestSchedulerAdvancesBeforeItStarts(t *testing.T) {
	f := &fakeForge{workflow: scheduledWorkflow, statuses: make(chan map[string]any, 8)}
	gh := httptest.NewServer(f.handler())
	defer gh.Close()
	st, err := store.Open(filepath.Join(t.TempDir(), "tick.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	srv := New(st, Config{
		RegistrationToken: regToken, ScheduleInterval: 10 * time.Millisecond,
		Forge: Forge{URL: "https://github.com", APIURL: gh.URL}, ForgeToken: "t",
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))

	past := time.Now().UTC().Add(-time.Hour)
	if err := st.ReplaceSchedules(t.Context(), "feed-mob/app", []store.Schedule{
		{Repo: "feed-mob/app", WorkflowFile: ".github/workflows/ci.yml", Ref: "main",
			Cron: "17 3 * * *", NextDueAt: past},
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	go srv.RunScheduler(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runs, _ := st.ListRuns(t.Context(), 10); len(runs) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Let several more ticks go by; a schedule that did not advance fires again.
	time.Sleep(200 * time.Millisecond)
	cancel()

	runs, _ := st.ListRuns(t.Context(), 10)
	if len(runs) != 1 {
		t.Fatalf("got %d runs from one due schedule, want exactly 1", len(runs))
	}
	if runs[0].Event != "schedule" || runs[0].Actor != "orrery" {
		t.Errorf("run = %+v", runs[0])
	}
	due, _ := st.DueSchedules(t.Context(), time.Now().UTC())
	if len(due) != 0 {
		t.Errorf("the schedule is still due after firing: %+v", due)
	}
}

const issueWorkflow = `
name: On Issues
on:
  issues:
    types: [opened, labeled]
jobs:
  triage:
    runs-on: [self-hosted]
    steps:
      - run: echo ${{ github.event.issue.number }}
`

// Most events carry no commit — issues, issue_comment, release, label. GitHub
// runs their workflows from the default branch; bailing out instead is how
// `on: issues` silently never fires.
func TestEventsWithoutACommitRunFromTheDefaultBranch(t *testing.T) {
	hs, st, _ := webhookServer(t, issueWorkflow)
	payload := map[string]any{
		"action":     "opened",
		"repository": map[string]any{"full_name": "feed-mob/app", "default_branch": "main"},
		"sender":     map[string]any{"login": "someone"},
		"issue":      map[string]any{"number": 7, "title": "broken"},
	}
	code, body := deliver(t, hs.URL, "issues", "d1", payload, true)
	if code != http.StatusOK {
		t.Fatalf("status = %d (%v)", code, body)
	}
	runs, _ := st.ListRuns(t.Context(), 10)
	if len(runs) != 1 {
		t.Fatalf("an issues event produced %d runs, want 1", len(runs))
	}
	if runs[0].Event != "issues" || runs[0].SHA != "main" {
		t.Errorf("run = %+v; workflows should be read from the default branch", runs[0])
	}
	// The event itself has to reach the job, or `github.event.issue.number`
	// resolves to nothing.
	meta, _ := st.RunMeta(t.Context(), runs[0].ID)
	if !strings.Contains(meta.EventPayload, `"number":7`) {
		t.Errorf("event payload lost the issue: %s", meta.EventPayload)
	}
}

// An activity type the workflow did not ask for must not fire it.
func TestIssueActivityTypesAreHonoured(t *testing.T) {
	hs, st, _ := webhookServer(t, issueWorkflow)
	payload := map[string]any{
		"action":     "closed",
		"repository": map[string]any{"full_name": "feed-mob/app", "default_branch": "main"},
		"sender":     map[string]any{"login": "someone"},
		"issue":      map[string]any{"number": 7},
	}
	if code, _ := deliver(t, hs.URL, "issues", "d1", payload, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 0 {
		t.Errorf("`types: [opened, labeled]` ran on a closed event")
	}
}

// A repository with no default branch and no commit has nothing to read.
func TestEventWithNothingToReadFromIsIgnored(t *testing.T) {
	hs, st, _ := webhookServer(t, issueWorkflow)
	payload := map[string]any{
		"action":     "opened",
		"repository": map[string]any{"full_name": "feed-mob/app"},
		"sender":     map[string]any{"login": "someone"},
	}
	if code, _ := deliver(t, hs.URL, "issues", "d1", payload, true); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if runs, _ := st.ListRuns(t.Context(), 10); len(runs) != 0 {
		t.Errorf("created %d runs with nothing to read a workflow from", len(runs))
	}
}
