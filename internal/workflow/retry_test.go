package workflow

import "testing"

func TestRetryDefaults(t *testing.T) {
	wf := mustParse(t, `
name: w
on: [push]
jobs:
  a:
    retry:
      max-attempts: 3
    steps:
      - run: x
`)
	r := wf.Jobs["a"].Retry
	if r == nil {
		t.Fatal("retry was dropped")
	}
	if r.MaxAttempts != 3 {
		t.Errorf("max-attempts = %d", r.MaxAttempts)
	}
	// Immediate retries are one failure reported three times.
	if r.BackoffSeconds != DefaultRetryBackoffSeconds {
		t.Errorf("backoff = %d, want a non-zero default", r.BackoffSeconds)
	}
	if len(r.On) != 1 || r.On[0] != "failure" {
		t.Errorf("on = %v, want failure only by default", r.On)
	}
}

func TestNoRetryKeyMeansNoRetry(t *testing.T) {
	wf := mustParse(t, "name: w\non: [push]\njobs:\n  a:\n    steps:\n      - run: x\n")
	if wf.Jobs["a"].Retry != nil {
		t.Fatal("a job without retry: got one")
	}
	// The nil case has to answer, not panic: most jobs are this case.
	if (*Retry)(nil).Retryable("failure") {
		t.Error("a nil retry policy claimed a failure was retryable")
	}
}

// A typo of 1000 should not be able to occupy a runner for a day.
func TestRetryBoundsAreEnforced(t *testing.T) {
	for _, src := range []string{
		"name: w\non: [push]\njobs:\n  a:\n    retry:\n      max-attempts: 1000\n    steps:\n      - run: x\n",
		"name: w\non: [push]\njobs:\n  a:\n    retry:\n      max-attempts: 2\n      backoff-seconds: -5\n    steps:\n      - run: x\n",
		"name: w\non: [push]\njobs:\n  a:\n    retry:\n      max-attempts: 2\n      on: [nonsense]\n    steps:\n      - run: x\n",
	} {
		if _, err := Parse([]byte(src)); err == nil {
			t.Errorf("accepted a bad retry policy:\n%s", src)
		}
	}
}

// A cancellation is someone deciding this job should stop. Asking to retry it
// is a mistake worth refusing at parse time rather than ignoring at run time.
func TestRetryOnCancelledIsRefused(t *testing.T) {
	src := "name: w\non: [push]\njobs:\n  a:\n    retry:\n      max-attempts: 3\n      on: [cancelled]\n    steps:\n      - run: x\n"
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("retry.on accepted cancelled")
	}
}

func TestRetryable(t *testing.T) {
	r := &Retry{MaxAttempts: 3, On: StringList{"failure", "timeout"}}
	if !r.Retryable("failure") || !r.Retryable("timeout") {
		t.Error("a listed outcome was not retryable")
	}
	if r.Retryable("success") || r.Retryable("skipped") {
		t.Error("an unlisted outcome was retryable")
	}
	// Never, however the policy is written.
	bad := &Retry{MaxAttempts: 3, On: StringList{"cancelled"}}
	if bad.Retryable("cancelled") {
		t.Error("a cancellation was retried")
	}
	if (&Retry{MaxAttempts: 1, On: StringList{"failure"}}).Retryable("failure") {
		t.Error("max-attempts 1 means no retry")
	}
}
