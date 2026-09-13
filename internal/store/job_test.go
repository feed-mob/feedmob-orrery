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
