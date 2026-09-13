// Package notify tells people when a run's verdict changes.
//
// The rule is deliberately transition-based, not per-run: a workflow that has
// been failing for six hours should have produced one message, not twelve. That
// is the same "告警去重" line the evaluation rubric draws from Mobius's own
// health alerts, and the reason those alerts are still read.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Webhook posts a Slack-shaped message. Slack, Mattermost and Discord's
// /slack endpoint all accept {"text": ...}, which is why that shape rather
// than anything richer: one field that every chat system already understands
// beats a payload only one of them renders.
type Webhook struct {
	URL string
	hc  *http.Client
}

// New returns nil when no URL is configured, so callers can hold a nil
// *Webhook and call Send on it without a branch at every site.
func New(url string) *Webhook {
	if url == "" {
		return nil
	}
	return &Webhook{URL: url, hc: &http.Client{Timeout: 15 * time.Second}}
}

func (w *Webhook) Send(ctx context.Context, text string) error {
	if w == nil {
		return nil
	}
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := w.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("notification webhook returned %s", res.Status)
	}
	return nil
}

// Event is what a settled run has to say for itself.
type Event struct {
	Repo       string
	Workflow   string
	Ref        string
	SHA        string
	Actor      string
	EventName  string
	Result     string
	Previous   string // the last result we notified about, "" if none
	RunID      int64
	RunAttempt int64
	URL        string
}

// Message renders the line a human reads in chat.
//
// Recovery is called out explicitly: "it is green again" is a different piece
// of news from "it is green", and the second one is not worth a message.
func Message(e Event) string {
	var b strings.Builder
	switch {
	case e.Result == "success" && e.Previous != "" && e.Previous != "success":
		b.WriteString("✅ 恢复了：")
	case e.Result == "success":
		b.WriteString("✅ ")
	case e.Result == "cancelled":
		b.WriteString("⚪️ 被取消：")
	default:
		b.WriteString("🔴 失败：")
	}
	b.WriteString(e.Workflow)
	if e.Repo != "" {
		b.WriteString(" · " + e.Repo)
	}
	if ref := shortRef(e.Ref); ref != "" {
		b.WriteString(" · " + ref)
	}
	if len(e.SHA) >= 7 {
		b.WriteString(" · " + e.SHA[:7])
	}
	fmt.Fprintf(&b, "\nrun #%d", e.RunID)
	if e.RunAttempt > 1 {
		fmt.Fprintf(&b, "（第 %d 次）", e.RunAttempt)
	}
	if e.EventName != "" {
		b.WriteString(" · " + e.EventName)
	}
	if e.Actor != "" && e.Actor != "orrery" {
		b.WriteString(" · " + e.Actor)
	}
	if e.URL != "" {
		b.WriteString("\n" + e.URL)
	}
	return b.String()
}

func shortRef(ref string) string {
	return strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")
}
