package store

import (
	"context"
	"errors"
	"testing"
	"time"
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
	good, err := st.LastGoodDeployment(ctx, "feed-mob/app", "production", badID, "v4")
	if err != nil {
		t.Fatalf("last good: %v", err)
	}
	if good.Version != "v2" {
		t.Errorf("last good = %q, want v2", good.Version)
	}
	// Rolling back again walks further back rather than bouncing between two.
	again, err := st.LastGoodDeployment(ctx, "feed-mob/app", "production", good.ID, good.Version)
	if err != nil {
		t.Fatalf("last good again: %v", err)
	}
	if again.Version != "v1" {
		t.Errorf("second rollback target = %q, want v1", again.Version)
	}

	// Nothing to go back to must say so rather than invent a version.
	if _, err := st.LastGoodDeployment(ctx, "feed-mob/app", "never-deployed", 0, ""); !errors.Is(err, ErrNotFound) {
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

// Deploying v2 twice and then rolling back should land on v1. The row before is
// still v2, and "rolling back" to the version you are already running is the
// button doing nothing while looking like it worked.
func TestRollbackSkipsTheVersionAlreadyLive(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	runID, err := st.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
		[]NewJob{{Key: "d", Payload: "p", Environment: "production"}})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	sum, _ := st.RunByID(ctx, runID)
	for _, v := range []string{"v1", "v2", "v2"} {
		if _, err := st.RecordDeployment(ctx, Deployment{
			Repo: "r", Environment: "production", Version: v,
			RunID: runID, JobID: sum.Jobs[0].ID, Result: "success",
		}); err != nil {
			t.Fatalf("record %s: %v", v, err)
		}
	}
	current, _ := st.CurrentDeployments(ctx, "r")
	live := current[0]
	if live.Version != "v2" {
		t.Fatalf("live = %q", live.Version)
	}
	good, err := st.LastGoodDeployment(ctx, "r", "production", live.ID, live.Version)
	if err != nil {
		t.Fatalf("last good: %v", err)
	}
	if good.Version != "v1" {
		t.Errorf("rollback target = %q, want v1 — the row before is still v2", good.Version)
	}

	// Only ever one version deployed: there is nothing to go back to, and
	// saying so beats redeploying what is already there.
	st2 := testStore(t)
	r2, _ := st2.CreateRun(ctx, Run{Repo: "r", WorkflowName: "w"},
		[]NewJob{{Key: "d", Payload: "p"}})
	s2, _ := st2.RunByID(ctx, r2)
	_, _ = st2.RecordDeployment(ctx, Deployment{
		Repo: "r", Environment: "production", Version: "v1",
		RunID: r2, JobID: s2.Jobs[0].ID, Result: "success",
	})
	cur, _ := st2.CurrentDeployments(ctx, "r")
	if _, err := st2.LastGoodDeployment(ctx, "r", "production", cur[0].ID, "v1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a single-version history offered a rollback target: %v", err)
	}
}

// The charter's P1 is "take the bill back from GitHub", and that is not a
// conversation anyone can have without knowing which workflow spends the time.
func TestUsageSince(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := newRunner(t, st, []string{"self-hosted"}, nil)

	run := func(repo, wfName string, results ...string) {
		id, err := st.CreateRun(ctx, Run{Repo: repo, WorkflowName: wfName},
			[]NewJob{{Key: "a", Payload: "p", RunsOn: []string{"self-hosted"}}})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		sum, _ := st.RunByID(ctx, id)
		job, err := st.ClaimJob(ctx, r)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		_ = sum
		// Give it a measurable duration.
		if _, err := st.db.ExecContext(ctx,
			`UPDATE jobs SET started_at = ?, stopped_at = ? WHERE id = ?`,
			ts(time.Now().UTC().Add(-time.Minute)), ts(time.Now().UTC()), job.ID); err != nil {
			t.Fatalf("times: %v", err)
		}
		if _, err := st.FinishJob(ctx, job.ID, results[0]); err != nil {
			t.Fatalf("finish: %v", err)
		}
	}
	run("feed-mob/app", "Deploy", "success")
	run("feed-mob/app", "Deploy", "failure")
	run("feed-mob/other", "CI", "success")

	usage, err := st.UsageSince(ctx, time.Now().UTC().AddDate(0, 0, -1))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	got := map[string]Usage{}
	for _, u := range usage {
		got[u.Repo+"/"+u.WorkflowName] = u
	}
	deploy := got["feed-mob/app/Deploy"]
	if deploy.Runs != 2 || deploy.Jobs != 2 {
		t.Errorf("Deploy = %+v, want 2 runs and 2 jobs", deploy)
	}
	if deploy.Failed != 1 {
		t.Errorf("Deploy failures = %d, want 1", deploy.Failed)
	}
	// A minute each, so roughly two minutes — not exact, the clock moved.
	if deploy.Millis < 100_000 || deploy.Millis > 130_000 {
		t.Errorf("Deploy millis = %d, want about 120000", deploy.Millis)
	}
	if got["feed-mob/other/CI"].Runs != 1 {
		t.Errorf("the other repository was merged in: %+v", got)
	}

	// A window that excludes everything reports nothing rather than everything.
	empty, err := st.UsageSince(ctx, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("usage: %v", err)
	}
	if len(empty) != 0 {
		t.Errorf("a future window returned %d rows", len(empty))
	}
}
