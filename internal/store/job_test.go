package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A run's steps are what turn "the job failed" into "this step failed, read
// these lines". They must survive the round-trip through the database with
// their timestamps and log ranges intact.
func TestRunByIDCarriesTheStepTimeline(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/orrery", WorkflowName: "w"},
		[]NewJob{{Key: "build", Payload: "p"}})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	sum, err := st.RunByID(ctx, runID)
	if err != nil {
		t.Fatalf("read run: %v", err)
	}
	jobID := sum.Jobs[0].ID

	started := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	stopped := started.Add(813 * time.Millisecond)
	if err := st.SetSteps(ctx, jobID, []StepReport{
		{Index: 0, Name: "Check out the repository", Result: "success",
			StartedAt: &started, StoppedAt: &stopped, LogIndex: 5, LogLength: 115},
		{Index: 1, Name: "Never ran", Result: "skipped"},
	}); err != nil {
		t.Fatalf("set steps: %v", err)
	}

	sum, err = st.RunByID(ctx, runID)
	if err != nil {
		t.Fatalf("reread run: %v", err)
	}
	steps := sum.Jobs[0].Steps
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Name != "Check out the repository" || steps[0].LogIndex != 5 || steps[0].LogLength != 115 {
		t.Errorf("step 0 = %+v", steps[0])
	}
	if steps[0].StartedAt == nil || !steps[0].StartedAt.Equal(started) {
		t.Errorf("step 0 started_at = %v, want %v", steps[0].StartedAt, started)
	}
	// A step that never ran has no clock, and must come back nil rather than as
	// the zero time — "not started" and "started at year zero" are different.
	if steps[1].StartedAt != nil || steps[1].StoppedAt != nil {
		t.Errorf("step 1 should have no timestamps, got %v / %v", steps[1].StartedAt, steps[1].StoppedAt)
	}

	// A second report for the same step replaces it rather than duplicating.
	if err := st.SetSteps(ctx, jobID, []StepReport{{Index: 1, Name: "Never ran", Result: "cancelled"}}); err != nil {
		t.Fatalf("re-set steps: %v", err)
	}
	sum, _ = st.RunByID(ctx, runID)
	if got := sum.Jobs[0].Steps; len(got) != 2 || got[1].Result != "cancelled" {
		t.Errorf("after upsert: %+v", got)
	}
}

// The scheduler asks the gate; the gate is what knows about `if:`. Without one
// the store keeps its pre-`if:` behaviour, which is what makes it safe to use
// the store on its own.
func TestPropagateAsksTheGate(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)

	var asked []string
	st.UseJobGate(func(run *Run, payload, key string, upstream map[string]string, cancelled bool) (bool, error) {
		asked = append(asked, key)
		if run == nil || run.Repo != "feed-mob/app" {
			t.Errorf("gate got run %+v; a gate that cannot see the run answers false silently", run)
		}
		if upstream["build"] != "failure" {
			t.Errorf("gate saw upstream %v, want build=failure", upstream)
		}
		// Stand in for `if: always()` on notify only.
		return key == "notify", nil
	})

	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/app", WorkflowName: "w"}, []NewJob{
		{Key: "build", Payload: "p"},
		{Key: "deploy", Needs: []string{"build"}, Payload: "p"},
		{Key: "notify", Needs: []string{"build"}, Payload: "p"},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	var buildID int64
	for _, j := range sum.Jobs {
		if j.Key == "build" {
			buildID = j.ID
		}
	}
	if _, err := st.FinishJob(ctx, buildID, "failure"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	sum, _ = st.RunByID(ctx, runID)
	got := map[string]string{}
	for _, j := range sum.Jobs {
		got[j.Key] = j.Status + "/" + j.Result
	}
	if got["deploy"] != "done/skipped" {
		t.Errorf("deploy = %q, want done/skipped", got["deploy"])
	}
	if got["notify"] != "queued/" {
		t.Errorf("notify = %q, want queued — the gate said it should run", got["notify"])
	}
	if len(asked) != 2 {
		t.Errorf("gate was asked about %v, want both dependents", asked)
	}
}

// GitHub has concurrency and defaults it off, which is why two pushes can
// deploy at the same time and nobody notices until they do.
func TestConcurrencyGroupSerialisesRuns(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	newRun := func(cancelInProgress bool) int64 {
		id, err := st.CreateRun(ctx, Run{
			Repo: "feed-mob/app", WorkflowName: "Deploy",
			ConcurrencyGroup: "feed-mob/app/Deploy@main", CancelInProgress: cancelInProgress,
		}, []NewJob{{Key: "deploy", Payload: "p", RunsOn: []string{"self-hosted"}}})
		if err != nil {
			t.Fatalf("create run: %v", err)
		}
		return id
	}
	statusOf := func(id int64) string {
		sum, err := st.RunByID(ctx, id)
		if err != nil {
			t.Fatalf("read run %d: %v", id, err)
		}
		return sum.Run.Status + "/" + sum.Run.Result
	}

	first := newRun(false)
	if _, err := st.ClaimJob(ctx, r); err != nil {
		t.Fatalf("first run should be claimable: %v", err)
	}

	// A second run in the same group waits, and its jobs stay out of reach even
	// though they are queued.
	second := newRun(false)
	if got := statusOf(second); got != "pending/" {
		t.Fatalf("second run = %q, want pending", got)
	}
	if _, err := st.ClaimJob(ctx, r); !errors.Is(err, ErrNotFound) {
		t.Fatalf("a pending run's job was handed out: %v", err)
	}

	// A third supersedes the second: by the time it would start, a deploy of
	// the commit before last is not what anyone wanted.
	third := newRun(false)
	if got := statusOf(second); got != "done/cancelled" {
		t.Errorf("second run = %q, want cancelled as superseded", got)
	}
	if got := statusOf(third); got != "pending/" {
		t.Errorf("third run = %q, want pending", got)
	}

	// Finishing the first frees the group and starts the one waiting.
	sum, _ := st.RunByID(ctx, first)
	if _, err := st.FinishJob(ctx, sum.Jobs[0].ID, "success"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if got := statusOf(third); got != "queued/" {
		t.Errorf("third run = %q after the group freed, want queued", got)
	}
	if _, err := st.ClaimJob(ctx, r); err != nil {
		t.Fatalf("promoted run should be claimable: %v", err)
	}
}

// cancel-in-progress asks the running job to wind down rather than killing it:
// a stop you cannot confirm is not a stop.
func TestCancelInProgressAsksTheRunningJobToStop(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	first, err := st.CreateRun(ctx, Run{
		Repo: "feed-mob/app", WorkflowName: "Deploy", ConcurrencyGroup: "g", CancelInProgress: true,
	}, []NewJob{{Key: "deploy", Payload: "p", RunsOn: []string{"self-hosted"}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job, err := st.ClaimJob(ctx, r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	if _, err := st.CreateRun(ctx, Run{
		Repo: "feed-mob/app", WorkflowName: "Deploy", ConcurrencyGroup: "g", CancelInProgress: true,
	}, []NewJob{{Key: "deploy", Payload: "p", RunsOn: []string{"self-hosted"}}}); err != nil {
		t.Fatalf("create second: %v", err)
	}

	pending, err := st.StopPending(ctx, job.ID)
	if err != nil {
		t.Fatalf("stop pending: %v", err)
	}
	if !pending {
		t.Error("the running job was not asked to stop")
	}
	sum, _ := st.RunByID(ctx, first)
	if sum.Run.Status == "done" {
		t.Error("the run settled before its job acknowledged; a stop you cannot confirm is not a stop")
	}
}

// An alert that repeats itself is an alert people learn to scroll past. Once a
// stop has been asked for, the timeout that caused it must stop re-reporting.
func TestOverdueReportsATimeoutOnlyUntilItAsksForAStop(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/app", WorkflowName: "w"},
		[]NewJob{{Key: "slow", Payload: "p", RunsOn: []string{"self-hosted"}, TimeoutMinutes: 1}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job, err := st.ClaimJob(ctx, r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	_ = runID

	// Pretend the job started two minutes ago, past its one-minute timeout.
	if _, err := st.db.ExecContext(ctx, `UPDATE jobs SET started_at = ? WHERE id = ?`,
		ts(time.Now().UTC().Add(-2*time.Minute)), job.ID); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	due, err := st.Overdue(ctx, 30)
	if err != nil {
		t.Fatalf("overdue: %v", err)
	}
	if len(due) != 1 || due[0].Reason != "timeout" {
		t.Fatalf("first sweep = %+v, want one timeout", due)
	}
	if err := st.RequestStop(ctx, job.ID, "reaper", "timeout"); err != nil {
		t.Fatalf("request stop: %v", err)
	}

	// Within the grace period there is nothing new to say.
	due, err = st.Overdue(ctx, 30)
	if err != nil {
		t.Fatalf("overdue: %v", err)
	}
	if len(due) != 0 {
		t.Errorf("second sweep re-reported the same event: %+v", due)
	}

	// Once the grace period lapses it becomes a different event, and that one
	// does need reporting.
	if _, err := st.db.ExecContext(ctx, `UPDATE jobs SET stop_requested_at = ? WHERE id = ?`,
		ts(time.Now().UTC().Add(-time.Minute)), job.ID); err != nil {
		t.Fatalf("backdate stop: %v", err)
	}
	due, err = st.Overdue(ctx, 30)
	if err != nil {
		t.Fatalf("overdue: %v", err)
	}
	if len(due) != 1 || due[0].Reason != "stop_unacked" {
		t.Fatalf("after the grace period = %+v, want stop_unacked", due)
	}
}

// A job that prints without stopping would otherwise fill the disk and take the
// control plane down with it.
func TestAppendLogsStopsAtTheCap(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	st.SetMaxLogBytes(500)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	runID, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
		[]NewJob{{Key: "noisy", Payload: "p", RunsOn: []string{"self-hosted"}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	_ = runID
	job, err := st.ClaimJob(ctx, r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}

	line := strings.Repeat("x", 100)
	var index int64
	for i := 0; i < 20; i++ {
		ack, err := st.AppendLogs(ctx, job.ID, index, []LogLine{{Time: st.now(), Content: line}}, false)
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		// Past the cap the lines must be acked, not refused: refusing would make
		// the runner resend them forever and turn a noisy job into a hot loop.
		if ack != index+1 {
			t.Fatalf("append %d: ack = %d, want %d — the runner would resend forever", i, ack, index+1)
		}
		index = ack
	}

	lines, err := st.Logs(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(lines) >= 20 {
		t.Fatalf("stored %d lines; the cap did nothing", len(lines))
	}
	last := lines[len(lines)-1].Content
	if !strings.Contains(last, "上限") {
		t.Errorf("the truncation is silent; last line = %q", last)
	}
	// Exactly one notice, however many further lines arrive.
	notices := 0
	for _, l := range lines {
		if strings.Contains(l.Content, "上限") {
			notices++
		}
	}
	if notices != 1 {
		t.Errorf("got %d truncation notices, want 1", notices)
	}
}

// Without retention the database only grows: a repository building twenty times
// a day keeps every log line of every run forever, and the first anyone hears
// about it is a full disk on the control plane.
func TestPruneDropsOldFinishedRunsAndTheirLogs(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	mk := func() int64 {
		id, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
			[]NewJob{{Key: "a", Payload: "p", RunsOn: []string{"self-hosted"}}})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		return id
	}
	oldRun := mk()
	job, err := st.ClaimJob(ctx, r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.AppendLogs(ctx, job.ID, 0,
		[]LogLine{{Time: st.now(), Content: "ancient history"}}, true); err != nil {
		t.Fatalf("append: %v", err)
	}
	if _, err := st.FinishJob(ctx, job.ID, "success"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	// Backdate it past the window.
	if _, err := st.db.ExecContext(ctx, `UPDATE runs SET stopped_at = ? WHERE id = ?`,
		ts(time.Now().UTC().Add(-60*24*time.Hour)), oldRun); err != nil {
		t.Fatalf("backdate: %v", err)
	}
	freshRun := mk()

	n, err := st.Prune(ctx, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d runs, want 1", n)
	}
	if _, err := st.RunByID(ctx, oldRun); !errors.Is(err, ErrNotFound) {
		t.Errorf("the old run survived: %v", err)
	}
	// A queued run older than the window is not old, it is stuck, and deleting
	// it would hide that.
	if _, err := st.RunByID(ctx, freshRun); err != nil {
		t.Errorf("a run that has not finished was pruned: %v", err)
	}
	// The logs went with it, through the cascade rather than a hand-rolled walk
	// that would eventually forget a table.
	var logs int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM job_logs WHERE job_id = ?`, job.ID).Scan(&logs); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if logs != 0 {
		t.Errorf("%d log rows outlived their run", logs)
	}
}

func TestPruneWithNoWindowKeepsEverything(t *testing.T) {
	st := testStore(t)
	if n, err := st.Prune(context.Background(), 0); err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}
