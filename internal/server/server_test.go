package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
	"github.com/feed-mob/feedmob-orrery/internal/store"
)

const regToken = "test-registration-token"

func testServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "srv.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv := New(st, Config{
		RegistrationToken: regToken,
		FetchHold:         50 * time.Millisecond,
		StopGrace:         50 * time.Millisecond,
		ReaperInterval:    20 * time.Millisecond,
	}, log)
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); st.Close() })
	return hs, st
}

func rpc[Req any, Resp any](t *testing.T, base, token, method string, req *Req) (*Resp, int) {
	t.Helper()
	body, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	httpReq, err := http.NewRequest(http.MethodPost, base+protocol.ServicePath+method, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		t.Fatalf("%s: %v", method, err)
	}
	defer res.Body.Close()
	var out Resp
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("%s decode: %v", method, err)
		}
	}
	return &out, res.StatusCode
}

func register(t *testing.T, base string) string {
	t.Helper()
	res, code := rpc[protocol.RegisterRequest, protocol.RegisterResponse](t, base, "", "Register", &protocol.RegisterRequest{
		Name:         "test-runner",
		Token:        regToken,
		Labels:       []string{"self-hosted"},
		Capabilities: []string{protocol.CapabilityCancelling},
	})
	if code != http.StatusOK {
		t.Fatalf("register status = %d", code)
	}
	return res.Runner.Token
}

func submit(t *testing.T, base, wf string) int64 {
	t.Helper()
	body, _ := json.Marshal(SubmitRequest{Repo: "r", WorkflowFile: "w.yml", Workflow: wf})
	res, err := http.Post(base+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("submit status = %d: %s", res.StatusCode, data)
	}
	var out SubmitResponse
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out.RunID
}

func TestRegisterRejectsABadSecret(t *testing.T) {
	hs, _ := testServer(t)
	_, code := rpc[protocol.RegisterRequest, protocol.RegisterResponse](t, hs.URL, "", "Register",
		&protocol.RegisterRequest{Name: "n", Token: "wrong"})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", code)
	}
}

func TestUnknownRunnerTokenGets401(t *testing.T) {
	hs, _ := testServer(t)
	_, code := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, "nonsense", "FetchTask",
		&protocol.FetchTaskRequest{})
	if code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 so the runner stops rather than retries", code)
	}
}

func TestSubmitAcceptsActionsNowThatActRunsThem(t *testing.T) {
	hs, _ := testServer(t)
	body, _ := json.Marshal(SubmitRequest{Repo: "r", Workflow: `
name: t
jobs:
  a:
    steps:
      - uses: actions/checkout@v4
      - run: echo hi
`})
	res, err := http.Post(hs.URL+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(res.Body)
		t.Fatalf("status = %d: %s", res.StatusCode, data)
	}
}

// The github context is what actions/checkout reads; a task carrying only YAML
// cannot run a real workflow.
func TestDispatchedTaskCarriesTheGithubContextAndSecrets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "ctx.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(st, Config{
		RegistrationToken: regToken,
		FetchHold:         50 * time.Millisecond,
		Secrets:           map[string]string{"GITHUB_TOKEN": "tok", "DEPLOY_KEY": "k"},
		Vars:              map[string]string{"REGION": "us-east-1"},
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	hs := httptest.NewServer(srv)
	t.Cleanup(hs.Close)

	token := register(t, hs.URL)
	body, _ := json.Marshal(SubmitRequest{
		Repo: "feed-mob/demo", WorkflowFile: "ci.yml", Event: "push",
		Ref: "refs/heads/main", SHA: "deadbeef", Actor: "someone",
		Workflow: "name: t\njobs:\n  a:\n    steps:\n      - run: echo hi\n",
	})
	res, err := http.Post(hs.URL+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	res.Body.Close()

	fetched, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	if fetched.Task == nil {
		t.Fatal("no task dispatched")
	}
	ctxWant := map[string]string{
		"repository": "feed-mob/demo",
		"ref":        "refs/heads/main",
		"sha":        "deadbeef",
		"actor":      "someone",
		"event_name": "push",
		"token":      "tok",
	}
	for k, want := range ctxWant {
		if got, _ := fetched.Task.Context[k].(string); got != want {
			t.Errorf("context[%q] = %q, want %q", k, got, want)
		}
	}
	if fetched.Task.Secrets["DEPLOY_KEY"] != "k" {
		t.Error("secrets did not reach the task")
	}
	if fetched.Task.Vars["REGION"] != "us-east-1" {
		t.Error("vars did not reach the task")
	}

	// Secrets are injected, never persisted: the database holds what ran, not
	// what it ran with.
	var hits int
	if err := st.DB().QueryRow(
		`SELECT COUNT(*) FROM jobs WHERE payload LIKE '%DEPLOY_KEY%' OR payload LIKE '%tok%'`).Scan(&hits); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if hits != 0 {
		t.Error("a secret value reached the database")
	}
}

func TestFetchTaskDispatchesAndCarriesThePlatformTimeout(t *testing.T) {
	hs, _ := testServer(t)
	token := register(t, hs.URL)
	submit(t, hs.URL, `
name: t
jobs:
  a:
    steps:
      - run: echo hi
`)
	res, code := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if res.Task == nil {
		t.Fatal("no task dispatched")
	}
	if res.Task.TimeoutMinutes <= 0 {
		t.Error("task reached the runner with no timeout; the platform default did not travel")
	}
	if len(res.Task.WorkflowPayload) == 0 {
		t.Error("task carried no workflow payload")
	}
}

func TestFetchTaskParksWhenThereIsNothingToDo(t *testing.T) {
	hs, _ := testServer(t)
	token := register(t, hs.URL)
	// Nothing queued, and the caller is already current: the server should hold
	// rather than answer immediately.
	res, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	start := time.Now()
	_, _ = rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{TasksVersion: res.TasksVersion})
	if elapsed := time.Since(start); elapsed < 40*time.Millisecond {
		t.Errorf("returned after %v; expected the call to park for roughly the hold", elapsed)
	}
}

// The reply to a runner's own heartbeat is the only channel a stop can travel
// on, so it must carry the flag.
func TestStopRequestReachesTheRunnerViaItsHeartbeat(t *testing.T) {
	hs, st := testServer(t)
	token := register(t, hs.URL)
	runID := submit(t, hs.URL, `
name: t
jobs:
  a:
    steps:
      - run: sleep 1
`)
	fetched, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	if fetched.Task == nil {
		t.Fatal("no task")
	}
	taskID := fetched.Task.ID

	// Before the ask, a heartbeat sees nothing.
	hb, _ := rpc[protocol.UpdateTaskRequest, protocol.UpdateTaskResponse](t, hs.URL, token, "UpdateTask",
		&protocol.UpdateTaskRequest{State: &protocol.TaskState{ID: taskID}})
	if hb.StopRequested {
		t.Fatal("stop reported before anyone asked for one")
	}

	res, err := http.Post(hs.URL+"/api/jobs/"+itoa(taskID)+"/stop", "application/json", nil)
	if err != nil {
		t.Fatalf("stop: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("stop status = %d, want 202", res.StatusCode)
	}

	hb, _ = rpc[protocol.UpdateTaskRequest, protocol.UpdateTaskResponse](t, hs.URL, token, "UpdateTask",
		&protocol.UpdateTaskRequest{State: &protocol.TaskState{ID: taskID}})
	if !hb.StopRequested {
		t.Fatal("heartbeat did not carry the stop request")
	}

	// The runner winds down and says so. That ack is what separates a stop
	// from a kill.
	now := time.Now().UTC()
	rpc[protocol.UpdateTaskRequest, protocol.UpdateTaskResponse](t, hs.URL, token, "UpdateTask",
		&protocol.UpdateTaskRequest{State: &protocol.TaskState{
			ID: taskID, Result: protocol.ResultCancelled, StopAckedAt: &now,
		}})

	job, err := st.JobByID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("read job: %v", err)
	}
	if !job.CleanupRan || job.ForceTerminated {
		t.Fatalf("cleanup_ran=%v force=%v, want an acknowledged stop", job.CleanupRan, job.ForceTerminated)
	}
	sum, _ := st.RunByID(context.Background(), runID)
	if sum.Run.Result != "cancelled" {
		t.Errorf("run result = %q, want cancelled", sum.Run.Result)
	}
}

func TestReaperForceTerminatesAStopNobodyAcknowledged(t *testing.T) {
	hs, st := testServer(t)
	token := register(t, hs.URL)
	submit(t, hs.URL, `
name: t
jobs:
  a:
    steps:
      - run: sleep 60
`)
	fetched, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	taskID := fetched.Task.ID

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := New(st, Config{
		RegistrationToken: regToken,
		StopGrace:         30 * time.Millisecond,
		ReaperInterval:    10 * time.Millisecond,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	go srv.RunReaper(ctx)

	if err := st.RequestStop(context.Background(), taskID, "test", "human"); err != nil {
		t.Fatalf("request stop: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		job, err := st.JobByID(context.Background(), taskID)
		if err == nil && job.ForceTerminated {
			if job.CleanupRan {
				t.Fatal("cleanup_ran is true on a job nobody acknowledged")
			}
			if job.Result != "cancelled" {
				t.Fatalf("result = %q, want cancelled", job.Result)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("reaper never force-terminated the unacknowledged stop")
}

func TestUpdateLogAcksAndRefusesGaps(t *testing.T) {
	hs, _ := testServer(t)
	token := register(t, hs.URL)
	submit(t, hs.URL, `
name: t
jobs:
  a:
    steps:
      - run: echo hi
`)
	fetched, _ := rpc[protocol.FetchTaskRequest, protocol.FetchTaskResponse](t, hs.URL, token, "FetchTask",
		&protocol.FetchTaskRequest{})
	taskID := fetched.Task.ID

	now := time.Now().UTC()
	res, _ := rpc[protocol.UpdateLogRequest, protocol.UpdateLogResponse](t, hs.URL, token, "UpdateLog",
		&protocol.UpdateLogRequest{TaskID: taskID, Index: 0, Rows: []protocol.LogRow{
			{Time: now, Content: "one"}, {Time: now, Content: "two"},
		}})
	if res.AckIndex != 2 {
		t.Fatalf("ack = %d, want 2", res.AckIndex)
	}
	// A window past the ack leaves a hole: the ack must not advance.
	res, _ = rpc[protocol.UpdateLogRequest, protocol.UpdateLogResponse](t, hs.URL, token, "UpdateLog",
		&protocol.UpdateLogRequest{TaskID: taskID, Index: 7, Rows: []protocol.LogRow{{Time: now, Content: "gap"}}})
	if res.AckIndex != 2 {
		t.Fatalf("ack after a gap = %d, want the unchanged 2", res.AckIndex)
	}
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := v < 0
	if neg {
		v = -v
	}
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
