package runner

import (
	"log/slog"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"github.com/feed-mob/feedmob-orrery/internal/protocol"
)

// The daemon socket must not be bound into job containers unless an operator
// asks for it: a step holding the host daemon can start a privileged container
// and is out of the sandbox the job container exists to be.
func TestDaemonSocketIsNotMountedByDefault(t *testing.T) {
	e := &actExecutor{dockerHost: "unix:///var/run/docker.sock"}
	if got := e.daemonSocketMount(); got != "-" {
		t.Fatalf("default mount = %q, want %q (act's sentinel for 'mount nothing')", got, "-")
	}
	e.mountDaemonSocket = true
	if got := e.daemonSocketMount(); got != "unix:///var/run/docker.sock" {
		t.Fatalf("opted-in mount = %q, want the daemon host", got)
	}
}

func TestForgeHost(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://github.com", "github.com"},
		{"https://git.feedmob.internal", "git.feedmob.internal"},
		{"http://git.lan:3000", "http://git.lan:3000"}, // scheme must survive
		{"", "github.com"},
	} {
		if got := forgeHost(tc.in); got != tc.want {
			t.Errorf("forgeHost(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// githubContext must carry a server URL: act builds GITHUB_SERVER_URL from it,
// and actions/checkout fails with a bare "Invalid URL" when it is empty.
func TestGithubContextCarriesForgeURLs(t *testing.T) {
	task := &protocol.Task{ID: 7, Context: map[string]any{
		"repository": "feed-mob/feedmob-orrery",
		"ref":        "refs/heads/main",
		"server_url": "https://git.feedmob.internal",
		"api_url":    "https://git.feedmob.internal/api/v1",
	}}
	gh := githubContext(task, "build", "Build")
	if gh.ServerURL != "https://git.feedmob.internal" {
		t.Errorf("ServerURL = %q", gh.ServerURL)
	}
	if gh.APIURL != "https://git.feedmob.internal/api/v1" {
		t.Errorf("APIURL = %q", gh.APIURL)
	}
	if gh.RepositoryOwner != "feed-mob" || gh.RefName != "main" || gh.RefType != "branch" {
		t.Errorf("derived fields wrong: owner=%q ref_name=%q ref_type=%q",
			gh.RepositoryOwner, gh.RefName, gh.RefType)
	}

	// A task that says nothing must still produce a usable context rather than
	// the empty string act turns into "https://".
	bare := githubContext(&protocol.Task{ID: 1}, "j", "J")
	if bare.ServerURL != "https://github.com" || bare.APIURL != "https://api.github.com" {
		t.Errorf("bare context = %q / %q", bare.ServerURL, bare.APIURL)
	}
}

// act runs each step three times (Pre/Main/Post) under one stepNumber. Timing a
// step from whichever pass logged first puts a later step's start before an
// earlier one's, because Pre passes run during job setup.
func TestHookTimesStepsFromTheMainStageOnly(t *testing.T) {
	h := newActHook(newTestShipper(), 2)
	base := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)

	fire := func(n int, name, stage string, at time.Time, result any) {
		data := logrus.Fields{"stepNumber": n, "step": name, "stage": stage}
		if result != nil {
			data["stepResult"] = result
		}
		_ = h.Fire(&logrus.Entry{Data: data, Time: at, Level: logrus.InfoLevel, Message: "x"})
	}

	// Both steps' Pre passes run first, out of declaration order.
	fire(1, "Set up Node", "Pre", base, nil)
	fire(0, "Check out", "Pre", base.Add(time.Second), nil)
	// Then the Main passes, in order.
	fire(0, "Check out", "Main", base.Add(10*time.Second), nil)
	fire(0, "Check out", "Main", base.Add(11*time.Second), "success")
	fire(1, "Set up Node", "Main", base.Add(12*time.Second), nil)
	fire(1, "Set up Node", "Main", base.Add(13*time.Second), "success")
	// A Post failure belongs to the job, not to a step that already finished.
	fire(0, "Check out", "Post", base.Add(20*time.Second), "failure")

	steps := h.result().Steps
	if len(steps) != 2 {
		t.Fatalf("got %d steps, want 2", len(steps))
	}
	if steps[0].Name != "Check out" || steps[1].Name != "Set up Node" {
		t.Fatalf("names = %q, %q", steps[0].Name, steps[1].Name)
	}
	if !steps[0].StartedAt.Equal(base.Add(10 * time.Second)) {
		t.Errorf("step 0 started at %v, want the Main stage at +10s", steps[0].StartedAt)
	}
	if steps[1].StartedAt.Before(*steps[0].StoppedAt) {
		t.Errorf("step 1 starts at %v, before step 0 stopped at %v — Pre timings leaked in",
			steps[1].StartedAt, steps[0].StoppedAt)
	}
	if !steps[1].StartedAt.Before(*steps[1].StoppedAt) {
		t.Errorf("step 1 stops (%v) before it starts (%v)", steps[1].StoppedAt, steps[1].StartedAt)
	}
	if steps[0].Result != protocol.ResultSuccess {
		t.Errorf("a failing Post must not overwrite the step result, got %q", steps[0].Result)
	}
}

// A step whose Pre stage fails never reaches Main, and must still report when
// it started rather than settling with a stop time and no beginning.
func TestHookStartsAStepThatOnlyFailedInPre(t *testing.T) {
	h := newActHook(newTestShipper(), 1)
	at := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	_ = h.Fire(&logrus.Entry{
		Data:  logrus.Fields{"stepNumber": 0, "step": "Broken", "stage": "Pre", "stepResult": "failure"},
		Time:  at,
		Level: logrus.ErrorLevel, Message: "boom",
	})
	steps := h.result().Steps
	if len(steps) != 1 || steps[0].StartedAt == nil || steps[0].Result != protocol.ResultFailure {
		t.Fatalf("step = %+v", steps)
	}
}

// newTestShipper returns a shipper that buffers without ever calling a server:
// under 200 lines and with a long flush interval it never reaches the client,
// so the hook can be exercised on its own.
func newTestShipper() *logShipper {
	return newLogShipper(nil, 0, time.Hour, slog.New(slog.DiscardHandler))
}

// act has no cancelled outcome for a step: a stop surfaces as a plain failure,
// so a job everyone agreed to stop would read as a job that broke.
func TestMarkCancelledOnlyTouchesWhatTheStopInterrupted(t *testing.T) {
	stopAt := time.Date(2026, 9, 13, 12, 0, 10, 0, time.UTC)
	before := stopAt.Add(-5 * time.Second)
	after := stopAt.Add(time.Second)
	started := stopAt.Add(-time.Minute)

	steps := []protocol.StepState{
		{Name: "genuinely broke", Result: protocol.ResultFailure, StartedAt: &started, StoppedAt: &before},
		{Name: "already passed", Result: protocol.ResultSuccess, StartedAt: &started, StoppedAt: &before},
		{Name: "interrupted", Result: protocol.ResultFailure, StartedAt: &started, StoppedAt: &after},
		{Name: "never settled", StartedAt: &started},
		{Name: "never started"},
	}
	got := markCancelled(steps, stopAt)

	want := []protocol.Result{
		protocol.ResultFailure,   // settled before the stop: not the stop's doing
		protocol.ResultSuccess,   // a stop never downgrades a success
		protocol.ResultCancelled, // failed at the moment of the stop
		protocol.ResultCancelled, // running when the stop arrived
		protocol.ResultUnspecified,
	}
	for i := range want {
		if got[i].Result != want[i] {
			t.Errorf("step %q = %q, want %q", got[i].Name, got[i].Result, want[i])
		}
	}
}
