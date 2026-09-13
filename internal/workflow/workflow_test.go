package workflow

import (
	"strings"
	"testing"
)

func TestParseAppliesPlatformDefaultTimeout(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
jobs:
  a:
    steps:
      - run: echo hi
  b:
    timeout-minutes: 5
    steps:
      - run: echo hi
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := wf.Jobs["a"].TimeoutMinutes; got != DefaultTimeoutMinutes {
		t.Errorf("job a timeout = %d, want the platform default %d", got, DefaultTimeoutMinutes)
	}
	if got := wf.Jobs["b"].TimeoutMinutes; got != 5 {
		t.Errorf("job b timeout = %d, want its own 5", got)
	}
}

func TestParseRejectsUnknownNeeds(t *testing.T) {
	_, err := Parse([]byte(`
name: t
jobs:
  a:
    needs: nope
    steps:
      - run: echo hi
`))
	if err == nil || !strings.Contains(err.Error(), "unknown job") {
		t.Fatalf("want unknown-job error, got %v", err)
	}
}

func TestParseRejectsCycles(t *testing.T) {
	_, err := Parse([]byte(`
name: t
jobs:
  a:
    needs: b
    steps: [{run: echo a}]
  b:
    needs: a
    steps: [{run: echo b}]
`))
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("want cycle error, got %v", err)
	}
}

func TestNeedsAcceptsScalarAndSequence(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
jobs:
  a:
    steps: [{run: echo a}]
  b:
    steps: [{run: echo b}]
  c:
    needs: a
    steps: [{run: echo c}]
  d:
    needs: [a, b]
    steps: [{run: echo d}]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := []string(wf.Jobs["c"].Needs); len(got) != 1 || got[0] != "a" {
		t.Errorf("scalar needs = %v, want [a]", got)
	}
	if got := []string(wf.Jobs["d"].Needs); len(got) != 2 {
		t.Errorf("sequence needs = %v, want two entries", got)
	}
}

func TestJobOrderFollowsTheFile(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
jobs:
  zebra:
    steps: [{run: echo z}]
  alpha:
    steps: [{run: echo a}]
  middle:
    steps: [{run: echo m}]
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := []string{"zebra", "alpha", "middle"}
	for i, k := range want {
		if wf.JobOrder[i] != k {
			t.Fatalf("JobOrder = %v, want source order %v", wf.JobOrder, want)
		}
	}
}

func TestUnsupportedStepsAreReportedBeforeDispatch(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
jobs:
  a:
    steps:
      - uses: actions/checkout@v4
      - run: echo hi
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bad := wf.UnsupportedSteps()
	if len(bad) != 1 || !strings.Contains(bad[0], "actions/checkout@v4") {
		t.Fatalf("UnsupportedSteps = %v, want the uses step named", bad)
	}
}

func TestJobPayloadRoundTripMergesWorkflowEnv(t *testing.T) {
	wf, err := Parse([]byte(`
name: t
env:
  FROM_WORKFLOW: "1"
  OVERRIDDEN: workflow
jobs:
  a:
    env:
      OVERRIDDEN: job
    steps:
      - name: hi
        run: echo hi
`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	payload, err := wf.JobPayload("a")
	if err != nil {
		t.Fatalf("JobPayload: %v", err)
	}
	key, job, err := JobFromPayload(payload)
	if err != nil {
		t.Fatalf("JobFromPayload: %v", err)
	}
	if key != "a" {
		t.Errorf("job key = %q, want a", key)
	}
	if job.Env["FROM_WORKFLOW"] != "1" {
		t.Error("workflow-level env did not reach the job")
	}
	if job.Env["OVERRIDDEN"] != "job" {
		t.Errorf("OVERRIDDEN = %q, want the job's value to win", job.Env["OVERRIDDEN"])
	}
	if len(job.Steps) != 1 || job.Steps[0].Run != "echo hi" {
		t.Errorf("steps did not survive the round trip: %+v", job.Steps)
	}
}

func TestStepLabelFallsBackToTheCommand(t *testing.T) {
	if got := (Step{Name: "named"}).Label(0); got != "named" {
		t.Errorf("Label = %q, want the name", got)
	}
	if got := (Step{Run: "echo hello\necho world"}).Label(0); got != "echo hello" {
		t.Errorf("Label = %q, want the first line of run", got)
	}
	if got := (Step{}).Label(2); got != "step 3" {
		t.Errorf("Label = %q, want a positional fallback", got)
	}
}
