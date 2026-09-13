// Package gate decides whether a job runs once its upstreams have settled.
//
// `if: always()` on a notify job, `if: failure()` on a rollback job: without
// this the scheduler skips every dependent of a failed job, so the jobs whose
// whole reason for existing is to run after a failure are exactly the ones that
// never run.
//
// The evaluation is act's own expression interpreter, with act's own workflow
// model underneath. Two engines disagreeing about what `if:` means would be
// worse than one of them doing less, and the disagreement would only show up on
// the day something fails.
package gate

import (
	"encoding/json"
	"fmt"
	"strings"

	"gitea.com/gitea/runner/act/exprparser"
	"gitea.com/gitea/runner/act/model"
)

// GithubFor builds the `github` context a job-level `if:` reads, from the run
// it belongs to. It deliberately mirrors what the dispatch path sends a runner:
// two different pictures of the same run would make `if:` and the job itself
// disagree about which commit they are looking at.
func GithubFor(repo, ref, sha, actor, event, workflow, runID, runNumber, eventPayload string) *model.GithubContext {
	gh := &model.GithubContext{
		Repository:      repo,
		RepositoryOwner: owner(repo),
		Ref:             ref,
		RefName:         strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/"),
		Sha:             sha,
		Actor:           actor,
		EventName:       event,
		Workflow:        workflow,
		RunID:           runID,
		RunNumber:       runNumber,
		Event:           map[string]any{},
	}
	if strings.HasPrefix(ref, "refs/tags/") {
		gh.RefType = "tag"
	} else if ref != "" {
		gh.RefType = "branch"
	}
	if eventPayload != "" {
		var raw map[string]any
		if json.Unmarshal([]byte(eventPayload), &raw) == nil {
			gh.Event = raw
		}
	}
	return gh
}

func owner(repo string) string {
	if i := strings.IndexByte(repo, '/'); i > 0 {
		return repo[:i]
	}
	return ""
}

// Env is everything a job-level `if:` may read. GitHub allows `github`,
// `needs`, `vars` and `inputs` there, and pointedly not `secrets`, `env` or
// `steps` — a job has not started when this is evaluated, so those do not
// exist yet.
type Env struct {
	Github *model.GithubContext
	Vars   map[string]string
	Inputs map[string]any
	// RunCancelled is what `cancelled()` answers. At job level it asks about
	// the run, not about the job, which has not started.
	RunCancelled bool
}

// Decide reports whether the job keyed by jobKey in this workflow payload
// should run, given how its upstreams turned out.
//
// An empty `if:` still goes through the interpreter: the default is
// `success()`, and letting act apply it keeps one definition of "success"
// rather than two.
func Decide(payload, jobKey string, needs map[string]string, env Env) (bool, error) {
	wf, err := model.ReadWorkflow(strings.NewReader(payload))
	if err != nil {
		return false, fmt.Errorf("read workflow: %w", err)
	}
	job := wf.GetJob(jobKey)
	if job == nil {
		return false, fmt.Errorf("payload carries no job %q", jobKey)
	}

	// The upstream jobs ran elsewhere and are not in this payload. success()
	// and failure() read their results off the workflow model, so they have to
	// be put back the same way the executor puts them back.
	needsEnv := map[string]exprparser.Needs{}
	for _, id := range job.Needs() {
		result := needs[id]
		if result == "" {
			result = "skipped"
		}
		if wf.Jobs[id] == nil {
			wf.Jobs[id] = &model.Job{}
		}
		wf.Jobs[id].Result = result
		needsEnv[id] = exprparser.Needs{Result: result}
	}

	jobStatus := "success"
	if env.RunCancelled {
		jobStatus = "cancelled"
	}
	run := &model.Run{Workflow: wf, JobID: jobKey}
	interp := exprparser.NewInterpeter(&exprparser.EvaluationEnvironment{
		Github: env.Github,
		Vars:   env.Vars,
		Inputs: env.Inputs,
		Needs:  needsEnv,
		Job:    &model.JobContext{Status: jobStatus},
	}, exprparser.Config{Run: run, Context: "job"})

	out, err := interp.Evaluate(job.If.Value, exprparser.DefaultStatusCheckSuccess)
	if err != nil {
		return false, fmt.Errorf("evaluate `if:` of job %q: %w", jobKey, err)
	}
	return truthy(out), nil
}

// truthy follows GitHub's coercion: an empty string, zero and null are false.
func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	default:
		return true
	}
}

// Interpolate resolves `${{ … }}` in a string against the same contexts a
// job-level `if:` may read. Used for `concurrency.group`, which is almost
// always `${{ github.workflow }}-${{ github.ref }}` and means nothing until
// those are filled in.
func Interpolate(s string, env Env) (string, error) {
	if !strings.Contains(s, "${{") {
		return s, nil
	}
	interp := exprparser.NewInterpeter(&exprparser.EvaluationEnvironment{
		Github: env.Github,
		Vars:   env.Vars,
		Inputs: env.Inputs,
		Needs:  map[string]exprparser.Needs{},
		Job:    &model.JobContext{},
	}, exprparser.Config{Context: "job"})

	var out strings.Builder
	rest := s
	for {
		open := strings.Index(rest, "${{")
		if open < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		close := strings.Index(rest[open:], "}}")
		if close < 0 {
			return "", fmt.Errorf("unterminated ${{ in %q", s)
		}
		out.WriteString(rest[:open])
		expr := strings.TrimSpace(rest[open+3 : open+close])
		v, err := interp.Evaluate(expr, exprparser.DefaultStatusCheckNone)
		if err != nil {
			return "", fmt.Errorf("evaluate %q: %w", expr, err)
		}
		out.WriteString(asString(v))
		rest = rest[open+close+2:]
	}
}

func asString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		if x {
			return "true"
		}
		return "false"
	case float64:
		if x == float64(int64(x)) {
			return fmt.Sprintf("%d", int64(x))
		}
		return fmt.Sprintf("%v", x)
	default:
		return fmt.Sprintf("%v", x)
	}
}
