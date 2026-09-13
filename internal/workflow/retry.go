package workflow

import "fmt"

// Retry is `retry:` on a job — Orrery's addition, not GitHub syntax.
//
// GitHub has no job retry at all: a deploy that fails because a registry
// timed out is re-run by a human, or not at all. That is the single most
// common reason someone goes back and presses a button, and it is the one
// thing a machine is better at than a person.
//
// Deliberately job-level rather than step-level. A step that half-ran is not
// safe to resume; a job re-runs from its first step in a clean workspace,
// which is the only retry that is honest about what it repeats.
type Retry struct {
	// MaxAttempts counts the first try. 1 means no retry; 0 means unset.
	MaxAttempts int `yaml:"max-attempts"`
	// BackoffSeconds is the wait before attempt two, doubling after that. A
	// registry or an SSH host that just refused you is not ready one second
	// later, and three immediate retries are one failure reported three times.
	BackoffSeconds int `yaml:"backoff-seconds"`
	// On lists the outcomes worth repeating. Default: failure only.
	//
	// `cancelled` is never retryable, even if asked for: someone or something
	// decided this job should stop, and restarting it is arguing with them.
	On StringList `yaml:"on"`
}

// DefaultRetryBackoffSeconds is the wait before the second attempt.
const DefaultRetryBackoffSeconds = 15

// MaxRetryAttempts bounds what a workflow may ask for. A typo of 1000 should
// not be able to occupy a runner for a day.
const MaxRetryAttempts = 10

func (r *Retry) normalise(jobKey string) error {
	if r == nil {
		return nil
	}
	if r.MaxAttempts < 0 || r.MaxAttempts > MaxRetryAttempts {
		return fmt.Errorf("job %q: retry.max-attempts must be between 1 and %d, got %d",
			jobKey, MaxRetryAttempts, r.MaxAttempts)
	}
	if r.MaxAttempts == 0 {
		r.MaxAttempts = 1
	}
	if r.BackoffSeconds < 0 {
		return fmt.Errorf("job %q: retry.backoff-seconds cannot be negative", jobKey)
	}
	if r.BackoffSeconds == 0 {
		r.BackoffSeconds = DefaultRetryBackoffSeconds
	}
	for _, on := range r.On {
		switch on {
		case "failure", "timeout":
		case "cancelled":
			return fmt.Errorf("job %q: retry.on cannot include cancelled; "+
				"a cancellation is someone deciding this job should stop", jobKey)
		default:
			return fmt.Errorf("job %q: retry.on has unknown outcome %q (want failure or timeout)", jobKey, on)
		}
	}
	if len(r.On) == 0 {
		r.On = StringList{"failure"}
	}
	return nil
}

// Retryable reports whether this outcome is one the author asked to repeat.
func (r *Retry) Retryable(result string) bool {
	if r == nil || r.MaxAttempts <= 1 {
		return false
	}
	// A cancellation is never repeated, however the workflow is written: the
	// operator, the reaper or a concurrency group decided this should stop.
	if result == "cancelled" {
		return false
	}
	for _, on := range r.On {
		if on == result {
			return true
		}
	}
	return false
}
