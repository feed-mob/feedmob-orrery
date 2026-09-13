package server

import (
	"sync"
	"time"
)

// throttle bounds how many runs one repository can start in a window.
//
// A webhook storm — a force-push touching forty branches, a bot relabelling
// every issue, a misconfigured integration retrying — turns into forty queued
// runs, and every runner in the pool spends the next hour on work nobody
// wanted. The concurrency group serialises runs that share a group; it does
// nothing about forty runs of forty different workflows.
//
// Deliberately a cap on *starting*, not a queue: a webhook that is refused is
// a delivery GitHub will redeliver, and a run that would have been superseded
// anyway is better never created. The alternative — queueing everything and
// sorting it out later — is how the queue becomes the outage.
type throttle struct {
	limit  int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	seen map[string][]time.Time
}

func newThrottle(limit int, window time.Duration) *throttle {
	if limit <= 0 {
		return nil
	}
	if window <= 0 {
		window = time.Minute
	}
	return &throttle{limit: limit, window: window, now: time.Now, seen: map[string][]time.Time{}}
}

// allow records an attempt and reports whether it is within the cap.
//
// nil means no limit configured, so the nil receiver answers yes rather than
// making every caller check.
func (t *throttle) allow(key string) (bool, int) {
	if t == nil {
		return true, 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	now := t.now()
	cutoff := now.Add(-t.window)
	kept := t.seen[key][:0]
	for _, at := range t.seen[key] {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) >= t.limit {
		t.seen[key] = kept
		return false, len(kept)
	}
	t.seen[key] = append(kept, now)
	return true, len(kept) + 1
}
