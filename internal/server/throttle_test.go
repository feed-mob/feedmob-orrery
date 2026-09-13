package server

import (
	"testing"
	"time"
)

// A force-push touching forty branches turns into forty queued runs, and every
// runner spends the next hour on work nobody wanted.
func TestThrottleCapsAndThenRecovers(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	th := newThrottle(3, time.Minute)
	th.now = func() time.Time { return now }

	for i := 1; i <= 3; i++ {
		if ok, n := th.allow("feed-mob/app"); !ok || n != i {
			t.Fatalf("attempt %d: ok=%v n=%d", i, ok, n)
		}
	}
	if ok, n := th.allow("feed-mob/app"); ok {
		t.Fatalf("the fourth was allowed (n=%d)", n)
	}
	// A different repository has its own budget: one noisy repo must not stop
	// everyone else's CI.
	if ok, _ := th.allow("feed-mob/other"); !ok {
		t.Error("another repository was caught by the first one's limit")
	}
	// The window slides rather than resetting on a fixed boundary.
	now = now.Add(61 * time.Second)
	if ok, n := th.allow("feed-mob/app"); !ok || n != 1 {
		t.Errorf("after the window: ok=%v n=%d", ok, n)
	}
}

// Zero means no cap, and callers hold a nil limiter without a branch.
func TestThrottleDisabled(t *testing.T) {
	if newThrottle(0, time.Minute) != nil {
		t.Fatal("a zero limit should yield no limiter")
	}
	var none *throttle
	for i := 0; i < 1000; i++ {
		if ok, _ := none.allow("x"); !ok {
			t.Fatal("a nil limiter refused something")
		}
	}
}

// Refusals must not keep the window pinned open: a repository that keeps
// hammering should still recover once it stops.
func TestThrottleRefusalsDoNotExtendTheWindow(t *testing.T) {
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	th := newThrottle(2, time.Minute)
	th.now = func() time.Time { return now }
	th.allow("r")
	th.allow("r")
	for i := 0; i < 10; i++ {
		now = now.Add(time.Second)
		if ok, _ := th.allow("r"); ok {
			t.Fatal("allowed inside the window")
		}
	}
	// 61s after the first two, not after the last refusal.
	now = now.Add(51 * time.Second)
	if ok, _ := th.allow("r"); !ok {
		t.Error("refusals extended the window; a noisy repo would never recover")
	}
}
