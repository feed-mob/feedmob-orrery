package store

import (
	"context"
	"testing"
)

// Re-running keeps the run and bumps an attempt, so the artifacts and outputs
// of the jobs that passed are still there for the jobs being re-run.
func TestRerunFailedOnlyTakesDownstreamWithIt(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/app", WorkflowName: "w"}, []NewJob{
		{Key: "build", Payload: "p", RunsOn: []string{"self-hosted"}},
		{Key: "test", Needs: []string{"build"}, Payload: "p", RunsOn: []string{"self-hosted"}},
		{Key: "deploy", Needs: []string{"test"}, Payload: "p", RunsOn: []string{"self-hosted"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	byKey := func() map[string]Job {
		sum, err := st.RunByID(ctx, runID)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		m := map[string]Job{}
		for _, j := range sum.Jobs {
			m[j.Key] = j
		}
		return m
	}

	// build succeeds, test fails, deploy is skipped behind it.
	build := byKey()["build"]
	if _, err := st.SetOutputs(ctx, build.ID, map[string]string{"image_tag": "v1"}); err != nil {
		t.Fatalf("outputs: %v", err)
	}
	if _, err := st.FinishJob(ctx, build.ID, "success"); err != nil {
		t.Fatalf("finish build: %v", err)
	}
	if _, err := st.ClaimJob(ctx, r); err != nil {
		t.Fatalf("claim test: %v", err)
	}
	if _, err := st.FinishJob(ctx, byKey()["test"].ID, "failure"); err != nil {
		t.Fatalf("finish test: %v", err)
	}
	jobs := byKey()
	if jobs["deploy"].Result != "skipped" {
		t.Fatalf("deploy = %q, want skipped", jobs["deploy"].Result)
	}

	n, err := st.Rerun(ctx, runID, true)
	if err != nil {
		t.Fatalf("rerun: %v", err)
	}
	// test failed; deploy comes along because its inputs are about to change.
	if n != 2 {
		t.Errorf("re-ran %d jobs, want test and deploy", n)
	}
	jobs = byKey()
	if jobs["build"].Status != "done" || jobs["build"].Result != "success" {
		t.Errorf("build was disturbed: %+v", jobs["build"])
	}
	if jobs["test"].Status != "queued" || jobs["test"].Result != "" {
		t.Errorf("test = %s/%q, want queued", jobs["test"].Status, jobs["test"].Result)
	}
	if jobs["deploy"].Status != "blocked" {
		t.Errorf("deploy = %q, want blocked behind test", jobs["deploy"].Status)
	}

	// The outputs of the job that passed have to survive, or `needs.build.outputs`
	// resolves to nothing on the re-run.
	outs, _, err := st.NeedsContext(ctx, runID, []string{"build"})
	if err != nil {
		t.Fatalf("needs: %v", err)
	}
	if outs["build"]["image_tag"] != "v1" {
		t.Errorf("build outputs after rerun = %+v", outs["build"])
	}

	sum, _ := st.RunByID(ctx, runID)
	if sum.Run.Status != "queued" || sum.Run.RunAttempt != 2 {
		t.Errorf("run = %s attempt %d, want queued attempt 2", sum.Run.Status, sum.Run.RunAttempt)
	}
}

// The log of a failure must survive the re-run that was meant to fix it.
func TestRerunKeepsThePreviousAttemptsLog(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/app", WorkflowName: "w"},
		[]NewJob{{Key: "build", Payload: "p", RunsOn: []string{"self-hosted"}}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	job, err := st.ClaimJob(ctx, r)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := st.AppendLogs(ctx, job.ID, 0,
		[]LogLine{{Time: st.now(), Content: "attempt one exploded"}}, true); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := st.SetSteps(ctx, job.ID, []StepReport{{Index: 0, Name: "boom", Result: "failure"}}); err != nil {
		t.Fatalf("steps: %v", err)
	}
	if _, err := st.FinishJob(ctx, job.ID, "failure"); err != nil {
		t.Fatalf("finish: %v", err)
	}

	if _, err := st.Rerun(ctx, runID, false); err != nil {
		t.Fatalf("rerun: %v", err)
	}
	if _, err := st.ClaimJob(ctx, r); err != nil {
		t.Fatalf("claim second attempt: %v", err)
	}
	// The second attempt's stream starts at 0 again and must not be refused as
	// a duplicate of the first.
	ack, err := st.AppendLogs(ctx, job.ID, 0,
		[]LogLine{{Time: st.now(), Content: "attempt two worked"}}, true)
	if err != nil {
		t.Fatalf("append second: %v", err)
	}
	if ack != 1 {
		t.Fatalf("ack = %d, want 1", ack)
	}

	current, err := st.Logs(ctx, job.ID, nil)
	if err != nil {
		t.Fatalf("logs: %v", err)
	}
	if len(current) != 1 || current[0].Content != "attempt two worked" {
		t.Errorf("current attempt = %+v", current)
	}
	one := 1
	first, err := st.Logs(ctx, job.ID, &one)
	if err != nil {
		t.Fatalf("logs attempt 1: %v", err)
	}
	if len(first) != 1 || first[0].Content != "attempt one exploded" {
		t.Errorf("attempt 1 log was lost: %+v", first)
	}

	// The step timeline shows the current attempt, not a mix of both.
	sum, _ := st.RunByID(ctx, runID)
	if len(sum.Jobs[0].Steps) != 0 {
		t.Errorf("the new attempt reported no steps yet, got %+v", sum.Jobs[0].Steps)
	}
}

func TestRerunRefusesARunThatIsStillGoing(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	runID, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
		[]NewJob{{Key: "a", Payload: "p"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := st.Rerun(ctx, runID, false); err == nil {
		t.Fatal("re-running a queued run was accepted; two attempts of one job would be in flight")
	}
}

func TestRerunFailedOnlyRefusesWhenNothingFailed(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	runID, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
		[]NewJob{{Key: "a", Payload: "p"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	if _, err := st.FinishJob(ctx, sum.Jobs[0].ID, "success"); err != nil {
		t.Fatalf("finish: %v", err)
	}
	if _, err := st.Rerun(ctx, runID, true); err == nil {
		t.Fatal("--failed on an all-green run should say so rather than re-run nothing")
	}
}
