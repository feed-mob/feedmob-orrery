package gate

import (
	"testing"

	"gitea.com/gitea/runner/act/model"
)

const wf = `
name: Deploy
on: [push]
jobs:
  build:
    runs-on: [self-hosted]
    steps:
      - run: echo build
  deploy:
    needs: [build]
    runs-on: [self-hosted]
    steps:
      - run: echo deploy
  notify:
    needs: [build]
    if: always()
    runs-on: [self-hosted]
    steps:
      - run: echo notify
  rollback:
    needs: [build]
    if: failure()
    runs-on: [self-hosted]
    steps:
      - run: echo rollback
  tagged:
    needs: [build]
    if: ${{ github.ref == 'refs/heads/main' }}
    runs-on: [self-hosted]
    steps:
      - run: echo tagged
`

func decide(t *testing.T, job, upstream string, env Env) bool {
	t.Helper()
	ok, err := Decide(wf, job, map[string]string{"build": upstream}, env)
	if err != nil {
		t.Fatalf("Decide(%s, build=%s): %v", job, upstream, err)
	}
	return ok
}

// The jobs whose whole reason for existing is to run after a failure are
// exactly the ones a blanket skip never runs.
func TestStatusFunctions(t *testing.T) {
	for _, tc := range []struct {
		job, upstream string
		want          bool
	}{
		{"deploy", "success", true},
		{"deploy", "failure", false},
		{"deploy", "cancelled", false},
		{"deploy", "skipped", false},

		{"notify", "success", true},
		{"notify", "failure", true},
		{"notify", "cancelled", true},

		{"rollback", "success", false},
		{"rollback", "failure", true},
		{"rollback", "cancelled", false},
	} {
		if got := decide(t, tc.job, tc.upstream, Env{}); got != tc.want {
			t.Errorf("%s with build=%s ran=%v, want %v", tc.job, tc.upstream, got, tc.want)
		}
	}
}

// A plain expression is implicitly ANDed with success(), which is the rule that
// makes `if: github.ref == 'refs/heads/main'` not run after a failed build.
func TestPlainExpressionStillImpliesSuccess(t *testing.T) {
	main := Env{Github: githubAt("refs/heads/main")}
	other := Env{Github: githubAt("refs/heads/feature")}

	if !decide(t, "tagged", "success", main) {
		t.Error("main + success should run")
	}
	if decide(t, "tagged", "success", other) {
		t.Error("a non-matching ref should not run")
	}
	if decide(t, "tagged", "failure", main) {
		t.Error("a plain expression must still be ANDed with success()")
	}
}

func TestUnknownUpstreamCountsAsSkipped(t *testing.T) {
	ok, err := Decide(wf, "deploy", nil, Env{})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if ok {
		t.Error("a job whose upstream reported nothing must not run")
	}
	ok, err = Decide(wf, "notify", nil, Env{})
	if err != nil {
		t.Fatalf("Decide: %v", err)
	}
	if !ok {
		t.Error("always() should still run when the upstream reported nothing")
	}
}

func TestBadExpressionIsAnError(t *testing.T) {
	bad := "name: w\non: [push]\njobs:\n  a:\n    if: ${{ nonsense( }}\n    steps:\n      - run: x\n"
	if _, err := Decide(bad, "a", nil, Env{}); err == nil {
		t.Fatal("a malformed `if:` was accepted")
	}
}

func githubAt(ref string) *model.GithubContext {
	return &model.GithubContext{Ref: ref}
}

// A concurrency group means nothing until the run's own context fills it in.
func TestInterpolate(t *testing.T) {
	env := Env{Github: &model.GithubContext{
		Workflow: "Deploy", Ref: "refs/heads/main", EventName: "push",
	}}
	for _, tc := range []struct{ in, want string }{
		{"deploy", "deploy"},
		{"${{ github.workflow }}@${{ github.ref }}", "Deploy@refs/heads/main"},
		{"x-${{ github.event_name }}-y", "x-push-y"},
		{"${{ github.ref == 'refs/heads/main' }}", "true"},
	} {
		got, err := Interpolate(tc.in, env)
		if err != nil {
			t.Errorf("Interpolate(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("Interpolate(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if _, err := Interpolate("${{ github.ref", env); err == nil {
		t.Error("an unterminated expression was accepted")
	}
}
