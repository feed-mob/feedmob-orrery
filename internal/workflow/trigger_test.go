package workflow

import "testing"

func mustParse(t *testing.T, src string) *Workflow {
	t.Helper()
	wf, err := Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return wf
}

func TestTriggersReadsAllThreeShapesOfOn(t *testing.T) {
	for _, tc := range []struct {
		name, src string
		want      []string
	}{
		{"bare string", "name: w\non: push\njobs:\n  a:\n    steps:\n      - run: x\n", []string{"push"}},
		{"list", "name: w\non: [push, pull_request]\njobs:\n  a:\n    steps:\n      - run: x\n", []string{"push", "pull_request"}},
		{"mapping", "name: w\non:\n  push:\n    branches: [main]\njobs:\n  a:\n    steps:\n      - run: x\n", []string{"push"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mustParse(t, tc.src).Triggers()
			if err != nil {
				t.Fatalf("Triggers: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %d triggers, want %d", len(got), len(tc.want))
			}
			seen := map[string]bool{}
			for _, g := range got {
				seen[g.Event] = true
			}
			for _, w := range tc.want {
				if !seen[w] {
					t.Errorf("missing trigger %q", w)
				}
			}
		})
	}
}

// Honouring one of a contradictory pair and dropping the other is how a
// workflow ends up running on refs its author believed were excluded.
func TestTriggersRejectContradictoryFilters(t *testing.T) {
	src := "name: w\non:\n  push:\n    branches: [main]\n    branches-ignore: [dev]\njobs:\n  a:\n    steps:\n      - run: x\n"
	if _, err := mustParse(t, src).Triggers(); err == nil {
		t.Fatal("accepted branches together with branches-ignore")
	}
}

func TestMatchesRefFilters(t *testing.T) {
	branchOnly := mustParse(t, "name: w\non:\n  push:\n    branches: [main, 'release/**']\njobs:\n  a:\n    steps:\n      - run: x\n")
	tagOnly := mustParse(t, "name: w\non:\n  push:\n    tags: ['v*']\njobs:\n  a:\n    steps:\n      - run: x\n")
	bare := mustParse(t, "name: w\non: push\njobs:\n  a:\n    steps:\n      - run: x\n")

	check := func(t *testing.T, wf *Workflow, ev Event, want bool) {
		t.Helper()
		_, ok, err := wf.Matches(ev)
		if err != nil {
			t.Fatalf("Matches: %v", err)
		}
		if ok != want {
			t.Errorf("event %+v matched=%v, want %v", ev, ok, want)
		}
	}

	check(t, branchOnly, Event{Name: "push", Ref: "refs/heads/main"}, true)
	check(t, branchOnly, Event{Name: "push", Ref: "refs/heads/release/2026-09"}, true)
	check(t, branchOnly, Event{Name: "push", Ref: "refs/heads/feature/x"}, false)
	// Declaring branches without tags excludes tag pushes entirely — this is
	// the rule that surprises people, and getting it wrong means every release
	// tag kicks off the CI workflow.
	check(t, branchOnly, Event{Name: "push", Ref: "refs/tags/v1.0.0"}, false)

	check(t, tagOnly, Event{Name: "push", Ref: "refs/tags/v1.0.0"}, true)
	check(t, tagOnly, Event{Name: "push", Ref: "refs/heads/main"}, false)

	// No filters at all means everything matches.
	check(t, bare, Event{Name: "push", Ref: "refs/heads/anything"}, true)
	check(t, bare, Event{Name: "push", Ref: "refs/tags/v9"}, true)
	check(t, bare, Event{Name: "pull_request", Ref: "refs/heads/main"}, false)
}

// pull_request filters on the branch being merged into, not on the PR's head.
func TestPullRequestFiltersOnTheBaseBranch(t *testing.T) {
	wf := mustParse(t, "name: w\non:\n  pull_request:\n    branches: [main]\njobs:\n  a:\n    steps:\n      - run: x\n")
	for _, tc := range []struct {
		ev   Event
		want bool
	}{
		{Event{Name: "pull_request", Action: "opened", Ref: "refs/heads/feature", BaseRef: "main"}, true},
		{Event{Name: "pull_request", Action: "opened", Ref: "refs/heads/feature", BaseRef: "release"}, false},
		// closed is not in the default activity types.
		{Event{Name: "pull_request", Action: "closed", BaseRef: "main"}, false},
		{Event{Name: "pull_request", Action: "synchronize", BaseRef: "main"}, true},
	} {
		_, ok, err := wf.Matches(tc.ev)
		if err != nil {
			t.Fatalf("Matches: %v", err)
		}
		if ok != tc.want {
			t.Errorf("%+v matched=%v, want %v", tc.ev, ok, tc.want)
		}
	}
}

func TestPathFilters(t *testing.T) {
	only := mustParse(t, "name: w\non:\n  push:\n    paths: ['src/**', '**.go']\njobs:\n  a:\n    steps:\n      - run: x\n")
	ignore := mustParse(t, "name: w\non:\n  push:\n    paths-ignore: ['docs/**']\njobs:\n  a:\n    steps:\n      - run: x\n")

	match := func(wf *Workflow, paths ...string) bool {
		_, ok, _ := wf.Matches(Event{Name: "push", Ref: "refs/heads/main", Paths: paths})
		return ok
	}
	if !match(only, "src/app/main.go") {
		t.Error("src/** should match a file under src")
	}
	if !match(only, "cmd/tool.go") {
		t.Error("**.go should match a .go file anywhere")
	}
	if match(only, "README.md") {
		t.Error("README.md matched a paths filter it does not satisfy")
	}
	// An event whose diff we could not read must not satisfy a paths filter.
	if match(only) {
		t.Error("a paths-filtered workflow ran on an event with no known paths")
	}
	if match(ignore, "docs/a.md", "docs/b.md") {
		t.Error("a change entirely inside docs should be ignored")
	}
	if !match(ignore, "docs/a.md", "main.go") {
		t.Error("a change touching anything outside docs should run")
	}
}

func TestGlobMatch(t *testing.T) {
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"main", "main", true},
		{"releases/*", "releases/v1", true},
		{"releases/*", "releases/v1/hotfix", false}, // a single * stays in one segment
		{"releases/**", "releases/v1/hotfix", true},
		{"**.go", "a/b/c.go", true},
		{"**.go", "c.go", true},
		{"a/**/b", "a/b", true}, // ** may match zero segments
		{"a/**/b", "a/x/y/b", true},
		{"v?.?", "v1.2", true},
		{"v?.?", "v1/2", false},
		{"*", "anything", true},
		{"*", "with/slash", false},
	} {
		if got := globMatch(tc.pattern, tc.s); got != tc.want {
			t.Errorf("globMatch(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}

// The last matching pattern wins, so a negation can carve an exception out of
// a broad match.
func TestNegatedPatterns(t *testing.T) {
	if !matchAny([]string{"releases/**", "!releases/**-rc"}, "releases/v1") {
		t.Error("releases/v1 should still match")
	}
	if matchAny([]string{"releases/**", "!releases/**-rc"}, "releases/v1-rc") {
		t.Error("the negation should have removed releases/v1-rc")
	}
}

// `on: workflow_run` is how a repository splits build from deploy without
// making one workflow that does both.
func TestWorkflowRunMatching(t *testing.T) {
	wf := mustParse(t, `
name: Deploy
on:
  workflow_run:
    workflows: [Build]
    types: [success]
jobs:
  a:
    steps:
      - run: x
`)
	check := func(upstream, conclusion string, want bool) {
		t.Helper()
		_, ok, err := wf.Matches(Event{Name: "workflow_run", Workflow: upstream, Conclusion: conclusion})
		if err != nil {
			t.Fatalf("Matches: %v", err)
		}
		if ok != want {
			t.Errorf("upstream=%q conclusion=%q matched=%v, want %v", upstream, conclusion, ok, want)
		}
	}
	check("Build", "success", true)
	check("Build", "failure", false)
	check("Build", "cancelled", false)
	// A different upstream is a different story.
	check("Lint", "success", false)
}

// GitHub's default is `completed`, which means any conclusion — so a deploy
// chained off a build also fires when that build failed. Worth reproducing
// faithfully, and worth knowing before wiring a deploy to it.
func TestWorkflowRunDefaultTypeIsAnyConclusion(t *testing.T) {
	wf := mustParse(t, "name: w\non:\n  workflow_run:\n    workflows: [Build]\njobs:\n  a:\n    steps:\n      - run: x\n")
	for _, conclusion := range []string{"success", "failure", "cancelled"} {
		_, ok, err := wf.Matches(Event{Name: "workflow_run", Workflow: "Build", Conclusion: conclusion})
		if err != nil || !ok {
			t.Errorf("conclusion %q: ok=%v err=%v; GitHub's default fires on all of them", conclusion, ok, err)
		}
	}
}

// An unnamed upstream would chain this workflow off every workflow in the
// repository, itself included.
func TestWorkflowRunRequiresWorkflows(t *testing.T) {
	src := "name: w\non:\n  workflow_run:\n    types: [success]\njobs:\n  a:\n    steps:\n      - run: x\n"
	if _, err := mustParse(t, src).Triggers(); err == nil {
		t.Fatal("on.workflow_run without `workflows:` was accepted")
	}
}
