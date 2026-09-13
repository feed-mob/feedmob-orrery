package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newRunner(t *testing.T, st *Store, labels []string, caps []string) *Runner {
	t.Helper()
	r, token, err := st.RegisterRunner(context.Background(), "test", "0", labels, caps, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	// Round-trip through the token so we exercise the auth path too.
	got, err := st.RunnerByToken(context.Background(), token)
	if err != nil {
		t.Fatalf("lookup by token: %v", err)
	}
	if got.ID != r.ID {
		t.Fatalf("token resolved to runner %d, want %d", got.ID, r.ID)
	}
	return got
}

func TestRunnerTokenIsNotStoredInTheClear(t *testing.T) {
	st := testStore(t)
	_, token, err := st.RegisterRunner(context.Background(), "r", "0", nil, nil, false)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	var stored string
	if err := st.DB().QueryRow(`SELECT token_hash FROM runners LIMIT 1`).Scan(&stored); err != nil {
		t.Fatalf("read: %v", err)
	}
	if stored == token {
		t.Fatal("token was stored verbatim; a leaked database would impersonate the runner")
	}
	if _, err := st.RunnerByToken(context.Background(), "not-the-token"); err != ErrNotFound {
		t.Fatalf("bad token error = %v, want ErrNotFound", err)
	}
}

func TestCreateRunQueuesOnlyUnblockedJobs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"}, []NewJob{
		{Key: "build", Payload: "p"},
		{Key: "test", Needs: []string{"build"}, Payload: "p"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, err := st.RunByID(ctx, runID)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	byKey := map[string]Job{}
	for _, j := range sum.Jobs {
		byKey[j.Key] = j
	}
	if byKey["build"].Status != "queued" {
		t.Errorf("build status = %q, want queued", byKey["build"].Status)
	}
	if byKey["test"].Status != "blocked" {
		t.Errorf("test status = %q, want blocked", byKey["test"].Status)
	}
}

func TestCreateRunAppliesDefaultTimeout(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, err := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{{Key: "a", Payload: "p"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	if sum.Jobs[0].TimeoutMinutes <= 0 {
		t.Fatal("a job reached the database with no timeout; the platform default did not apply")
	}
}

func TestClaimJobMatchesLabelsAndIsExclusive(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if _, err := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{
		{Key: "gpu", RunsOn: []string{"gpu"}, Payload: "p"},
		{Key: "any", Payload: "p"},
	}); err != nil {
		t.Fatalf("create: %v", err)
	}
	plain := newRunner(t, st, []string{"self-hosted"}, nil)

	// The gpu job must be skipped; the unlabelled one is fair game.
	job, err := st.ClaimJob(ctx, plain)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if job.Key != "any" {
		t.Fatalf("claimed %q, want the job this runner can satisfy", job.Key)
	}
	// A second claim finds nothing left for this runner.
	if _, err := st.ClaimJob(ctx, plain); err != ErrNotFound {
		t.Fatalf("second claim error = %v, want ErrNotFound", err)
	}

	gpu := newRunner(t, st, []string{"self-hosted", "gpu"}, nil)
	job, err = st.ClaimJob(ctx, gpu)
	if err != nil {
		t.Fatalf("gpu claim: %v", err)
	}
	if job.Key != "gpu" {
		t.Fatalf("gpu runner claimed %q, want gpu", job.Key)
	}
}

func TestFinishJobUnblocksDependentsAndSettlesTheRun(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{
		{Key: "build", Payload: "p"},
		{Key: "test", Needs: []string{"build"}, Payload: "p"},
	})
	sum, _ := st.RunByID(ctx, runID)
	var buildID, testID int64
	for _, j := range sum.Jobs {
		if j.Key == "build" {
			buildID = j.ID
		} else {
			testID = j.ID
		}
	}
	if err := st.FinishJob(ctx, buildID, "success"); err != nil {
		t.Fatalf("finish build: %v", err)
	}
	sum, _ = st.RunByID(ctx, runID)
	for _, j := range sum.Jobs {
		if j.Key == "test" && j.Status != "queued" {
			t.Fatalf("test status = %q, want queued once build succeeded", j.Status)
		}
	}
	if err := st.FinishJob(ctx, testID, "success"); err != nil {
		t.Fatalf("finish test: %v", err)
	}
	sum, _ = st.RunByID(ctx, runID)
	if sum.Run.Status != "done" || sum.Run.Result != "success" {
		t.Fatalf("run = %s/%s, want done/success", sum.Run.Status, sum.Run.Result)
	}
}

func TestFailedUpstreamSkipsDependentsInsteadOfBlockingForever(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{
		{Key: "build", Payload: "p"},
		{Key: "test", Needs: []string{"build"}, Payload: "p"},
	})
	sum, _ := st.RunByID(ctx, runID)
	var buildID int64
	for _, j := range sum.Jobs {
		if j.Key == "build" {
			buildID = j.ID
		}
	}
	if err := st.FinishJob(ctx, buildID, "failure"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	sum, _ = st.RunByID(ctx, runID)
	for _, j := range sum.Jobs {
		if j.Key != "test" {
			continue
		}
		if j.Status != "done" || j.Result != "skipped" {
			t.Fatalf("test = %s/%s, want done/skipped", j.Status, j.Result)
		}
		if j.StopReason != "upstream_failed" {
			t.Errorf("stop reason = %q, want upstream_failed", j.StopReason)
		}
	}
	if sum.Run.Result != "failure" {
		t.Errorf("run result = %q, want failure", sum.Run.Result)
	}
}

// A stop you cannot confirm is not a stop. The two paths below must stay
// distinguishable in the ledger.
func TestAcknowledgedStopRecordsThatCleanupRan(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{{Key: "a", Payload: "p"}})
	sum, _ := st.RunByID(ctx, runID)
	id := sum.Jobs[0].ID

	if err := st.RequestStop(ctx, id, "tester", "human"); err != nil {
		t.Fatalf("request stop: %v", err)
	}
	pending, err := st.StopPending(ctx, id)
	if err != nil || !pending {
		t.Fatalf("StopPending = %v, %v; want true once requested and unacked", pending, err)
	}
	if err := st.AckStop(ctx, id, time.Now()); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if pending, _ = st.StopPending(ctx, id); pending {
		t.Error("StopPending still true after the runner acknowledged")
	}
	if err := st.FinishJob(ctx, id, "cancelled"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	job, _ := st.JobByID(ctx, id)
	if !job.CleanupRan || job.ForceTerminated {
		t.Fatalf("acked stop recorded as cleanup_ran=%v force=%v, want true/false", job.CleanupRan, job.ForceTerminated)
	}
}

func TestForceTerminateRecordsThatCleanupDidNotRun(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{{Key: "a", Payload: "p"}})
	sum, _ := st.RunByID(ctx, runID)
	id := sum.Jobs[0].ID

	if err := st.RequestStop(ctx, id, "reaper", "timeout"); err != nil {
		t.Fatalf("request: %v", err)
	}
	if err := st.ForceTerminate(ctx, id, "timeout"); err != nil {
		t.Fatalf("force: %v", err)
	}
	job, _ := st.JobByID(ctx, id)
	if !job.ForceTerminated {
		t.Error("force_terminated not recorded")
	}
	if job.CleanupRan {
		t.Error("cleanup_ran is true on a job nobody acknowledged; the ledger is lying")
	}
	if job.Result != "cancelled" {
		t.Errorf("result = %q, want cancelled", job.Result)
	}
}

func TestOverdueFindsTimeoutsAndUnackedStops(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{
		{Key: "slow", Payload: "p", TimeoutMinutes: 1},
		{Key: "stuck", Payload: "p", TimeoutMinutes: 600},
	})
	sum, _ := st.RunByID(ctx, runID)
	ids := map[string]int64{}
	for _, j := range sum.Jobs {
		ids[j.Key] = j.ID
	}
	// Put both in flight, started well in the past.
	old := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339Nano)
	for _, id := range ids {
		if _, err := st.DB().Exec(`UPDATE jobs SET status='running', started_at=? WHERE id=?`, old, id); err != nil {
			t.Fatalf("setup: %v", err)
		}
	}
	// "stuck" has an ancient unacknowledged stop but a huge timeout.
	if _, err := st.DB().Exec(`UPDATE jobs SET stop_requested_at=? WHERE id=?`, old, ids["stuck"]); err != nil {
		t.Fatalf("setup: %v", err)
	}

	overdue, err := st.Overdue(ctx, 30)
	if err != nil {
		t.Fatalf("overdue: %v", err)
	}
	reasons := map[int64]string{}
	for _, o := range overdue {
		reasons[o.ID] = o.Reason
	}
	if reasons[ids["slow"]] != "timeout" {
		t.Errorf("slow reason = %q, want timeout", reasons[ids["slow"]])
	}
	if reasons[ids["stuck"]] != "stop_unacked" {
		t.Errorf("stuck reason = %q, want stop_unacked", reasons[ids["stuck"]])
	}
}

// Delivery is defined by the ack, not by the HTTP status of the submission.
func TestAppendLogsIsIdempotentAndRefusesGaps(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{{Key: "a", Payload: "p"}})
	sum, _ := st.RunByID(ctx, runID)
	id := sum.Jobs[0].ID

	now := time.Now()
	rows := []LogLine{{Time: now, Content: "one"}, {Time: now, Content: "two"}}
	ack, err := st.AppendLogs(ctx, id, 0, rows, false)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if ack != 2 {
		t.Fatalf("ack = %d, want 2", ack)
	}

	// A retry of the same window must not duplicate lines.
	if ack, err = st.AppendLogs(ctx, id, 0, rows, false); err != nil || ack != 2 {
		t.Fatalf("retry ack = %d (%v), want 2", ack, err)
	}
	lines, _ := st.Logs(ctx, id)
	if len(lines) != 2 {
		t.Fatalf("stored %d lines after a retry, want 2", len(lines))
	}

	// A window that starts past the ack leaves a hole; refuse it and tell the
	// runner where we actually are.
	if ack, err = st.AppendLogs(ctx, id, 9, []LogLine{{Time: now, Content: "gap"}}, false); err != nil {
		t.Fatalf("gap append: %v", err)
	}
	if ack != 2 {
		t.Fatalf("ack after a gap = %d, want the unchanged 2", ack)
	}
	if lines, _ = st.Logs(ctx, id); len(lines) != 2 {
		t.Fatalf("a gapped window was stored anyway: %d lines", len(lines))
	}
}

func TestNeedsContextCarriesUpstreamOutputs(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	runID, _ := st.CreateRun(ctx, Run{Repo: "r"}, []NewJob{
		{Key: "build", Payload: "p"},
		{Key: "test", Needs: []string{"build"}, Payload: "p"},
	})
	sum, _ := st.RunByID(ctx, runID)
	var buildID int64
	for _, j := range sum.Jobs {
		if j.Key == "build" {
			buildID = j.ID
		}
	}
	if _, err := st.SetOutputs(ctx, buildID, map[string]string{"artifact": "app.tar"}); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if err := st.FinishJob(ctx, buildID, "success"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	outputs, results, err := st.NeedsContext(ctx, runID, []string{"build"})
	if err != nil {
		t.Fatalf("needs: %v", err)
	}
	if outputs["build"]["artifact"] != "app.tar" {
		t.Errorf("outputs = %v, want the upstream artifact", outputs)
	}
	if results["build"] != "success" {
		t.Errorf("result = %q, want success", results["build"])
	}
}
