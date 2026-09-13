package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMessageDistinguishesRecoveryFromSuccess(t *testing.T) {
	base := Event{Repo: "feed-mob/app", Workflow: "Deploy", Ref: "refs/heads/main",
		SHA: "abc1234def", RunID: 12, EventName: "push", Actor: "yongcheng",
		URL: "https://orrery.internal/runs/12"}

	fail := base
	fail.Result = "failure"
	if got := Message(fail); !strings.Contains(got, "🔴 失败") || !strings.Contains(got, "abc1234") {
		t.Errorf("failure message = %q", got)
	}

	// "It is green again" is different news from "it is green", and the second
	// is not worth a message at all.
	recovered := base
	recovered.Result, recovered.Previous = "success", "failure"
	if got := Message(recovered); !strings.Contains(got, "恢复了") {
		t.Errorf("recovery message = %q", got)
	}

	cancelled := base
	cancelled.Result = "cancelled"
	if got := Message(cancelled); strings.Contains(got, "🔴") {
		t.Errorf("a cancellation should not read as a failure: %q", got)
	}

	attempted := base
	attempted.Result, attempted.RunAttempt = "failure", 3
	if got := Message(attempted); !strings.Contains(got, "第 3 次") {
		t.Errorf("re-run message lost the attempt: %q", got)
	}
	// A scheduled run's actor is the engine; saying so adds nothing.
	sched := base
	sched.Result, sched.Actor, sched.URL = "failure", "orrery", ""
	if strings.Contains(Message(sched), "orrery") {
		t.Errorf("the engine named itself as the actor: %q", Message(sched))
	}
}

func TestWebhookSendsSlackShapedJSON(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	if err := New(srv.URL).Send(context.Background(), "hello"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if got["text"] != "hello" {
		t.Errorf("payload = %+v, want a Slack-shaped text field", got)
	}
}

// A nil Webhook is the "not configured" case, and callers hold it without a
// branch at every site.
func TestNilWebhookIsQuiet(t *testing.T) {
	if New("") != nil {
		t.Fatal("an empty URL should yield no webhook")
	}
	if err := New("").Send(context.Background(), "x"); err != nil {
		t.Errorf("sending on a nil webhook: %v", err)
	}
}

func TestWebhookSurfacesAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no_such_channel", http.StatusNotFound)
	}))
	defer srv.Close()
	if err := New(srv.URL).Send(context.Background(), "x"); err == nil {
		t.Fatal("a rejected notification reported success; delivery is not the same as success")
	}
}
