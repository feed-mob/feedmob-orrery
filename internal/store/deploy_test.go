package store

import (
	"context"
	"errors"
	"testing"
)

func TestDeploymentLedger(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	runID, err := st.CreateRun(ctx, Run{Repo: "feed-mob/app", WorkflowName: "Deploy"},
		[]NewJob{{Key: "d", Payload: "p", Environment: "production"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	jobID := sum.Jobs[0].ID

	rec := func(env, version, result string) int64 {
		id, err := st.RecordDeployment(ctx, Deployment{
			Repo: "feed-mob/app", Environment: env, Version: version,
			SHA: "abc123", RunID: runID, JobID: jobID, Result: result,
			WorkflowFile: ".github/workflows/deploy.yml",
		})
		if err != nil {
			t.Fatalf("record: %v", err)
		}
		return id
	}
	rec("production", "v1", "success")
	rec("production", "v2", "success")
	rec("staging", "v3", "success")
	badID := rec("production", "v4", "failure")

	// Latest, not latest-successful: an environment whose last deploy failed is
	// not still running v2 in any sense anyone can rely on.
	current, err := st.CurrentDeployments(ctx, "feed-mob/app")
	if err != nil {
		t.Fatalf("current: %v", err)
	}
	got := map[string]Deployment{}
	for _, d := range current {
		got[d.Environment] = d
	}
	if len(got) != 2 {
		t.Fatalf("got %d environments, want production and staging: %+v", len(got), current)
	}
	if got["production"].Version != "v4" || got["production"].Result != "failure" {
		t.Errorf("production = %+v; a failed deploy must be visible, not hidden behind v2", got["production"])
	}
	if got["staging"].Version != "v3" {
		t.Errorf("staging = %+v", got["staging"])
	}

	// Rollback goes back to the last success before the one being replaced.
	good, err := st.LastGoodDeployment(ctx, "feed-mob/app", "production", badID)
	if err != nil {
		t.Fatalf("last good: %v", err)
	}
	if good.Version != "v2" {
		t.Errorf("last good = %q, want v2", good.Version)
	}
	// Rolling back again walks further back rather than bouncing between two.
	again, err := st.LastGoodDeployment(ctx, "feed-mob/app", "production", good.ID)
	if err != nil {
		t.Fatalf("last good again: %v", err)
	}
	if again.Version != "v1" {
		t.Errorf("second rollback target = %q, want v1", again.Version)
	}

	// Nothing to go back to must say so rather than invent a version.
	if _, err := st.LastGoodDeployment(ctx, "feed-mob/app", "never-deployed", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("an environment with no history: %v", err)
	}

	history, err := st.DeploymentHistory(ctx, "feed-mob/app", "production", 10)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 3 || history[0].Version != "v4" {
		t.Errorf("history = %+v, want newest first", history)
	}
}

// The job has to carry its environment through to the ledger, or a deployment
// is just another run.
func TestDeployInfoJoinsTheRun(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	runID, err := st.CreateRun(ctx, Run{
		Repo: "feed-mob/app", WorkflowName: "Deploy",
		WorkflowFile: ".github/workflows/deploy.yml", Ref: "refs/heads/main",
		SHA: "abc123", Actor: "yongcheng",
	}, []NewJob{
		{Key: "d", Payload: "p", Environment: "production",
			EnvironmentURL: "https://app", AutoRollback: true, VersionFrom: "image_tag"},
		{Key: "plain", Payload: "p"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	byKey := map[string]int64{}
	for _, j := range sum.Jobs {
		byKey[j.Key] = j.ID
	}

	info, err := st.DeployInfo(ctx, byKey["d"])
	if err != nil {
		t.Fatalf("deploy info: %v", err)
	}
	if info.Environment != "production" || !info.AutoRollback || info.VersionFrom != "image_tag" {
		t.Errorf("info = %+v", info)
	}
	if info.SHA != "abc123" || info.WorkflowFile != ".github/workflows/deploy.yml" {
		t.Errorf("the run's fields did not join through: %+v", info)
	}

	// A job with no environment is not a deployment and must not look like one.
	plain, err := st.DeployInfo(ctx, byKey["plain"])
	if err != nil {
		t.Fatalf("deploy info: %v", err)
	}
	if plain.Environment != "" {
		t.Errorf("an ordinary job reported an environment: %+v", plain)
	}
}
