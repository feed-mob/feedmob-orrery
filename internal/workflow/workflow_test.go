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

func TestUsedActionsAreRecordedForLaterPinning(t *testing.T) {
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
	used := wf.UsedActions()
	if len(used) != 1 || used[0] != "actions/checkout@v4" {
		t.Fatalf("UsedActions = %v, want the one action named", used)
	}
}

func TestJobPayloadKeepsOneJobAndPreservesUnmodelledFields(t *testing.T) {
	src := []byte(`
name: t
env:
  FROM_WORKFLOW: "1"
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo build
  test:
    needs: build
    runs-on: ubuntu-latest
    strategy:
      matrix:
        node: [18, 20]
    container:
      image: node:20
    services:
      db:
        image: postgres:16
    steps:
      - run: echo test
`)
	wf, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	payload, err := wf.JobPayload(src, "test")
	if err != nil {
		t.Fatalf("JobPayload: %v", err)
	}
	out := string(payload)

	// Only the requested job survives.
	if strings.Contains(out, "echo build") {
		t.Error("payload still carries the sibling job")
	}
	if !strings.Contains(out, "echo test") {
		t.Error("payload lost the job it was asked for")
	}
	// Workflow-level keys stay, so act sees the file the author wrote.
	if !strings.Contains(out, "FROM_WORKFLOW") {
		t.Error("workflow-level env was dropped")
	}
	// Fields this parser does not model must survive verbatim; re-serialising
	// our own structs would have silently eaten all three.
	for _, want := range []string{"strategy", "matrix", "container", "services", "postgres:16"} {
		if !strings.Contains(out, want) {
			t.Errorf("payload dropped %q, which this parser does not model", want)
		}
	}
	// needs is the server's business and act would look for a job that is no
	// longer in the file.
	if strings.Contains(out, "needs") {
		t.Error("payload still carries needs:")
	}
}

func TestJobPayloadRejectsAnUnknownJob(t *testing.T) {
	src := []byte("name: t\njobs:\n  a:\n    steps:\n      - run: echo hi\n")
	wf, err := Parse(src)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, err := wf.JobPayload(src, "nope"); err == nil {
		t.Fatal("JobPayload accepted a job that does not exist")
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
