package store

import (
	"context"
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
