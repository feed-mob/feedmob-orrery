// Package forge talks to the code host a run belongs to.
//
// Two things Orrery cannot do without: read the workflow files at the commit
// that triggered a run, and report the result back where branch protection can
// see it. Both are GitHub REST calls, and both are the reason Orrery can sit in
// front of an existing repository instead of asking anyone to migrate first.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client is a GitHub REST client scoped to one forge and one credential.
type Client struct {
	apiURL string
	token  string
	hc     *http.Client
}

// New builds a client. An empty token yields a client that can still read
// public repositories, which is enough for a smoke test and not enough for
// anything real.
func New(apiURL, token string) *Client {
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	return &Client{
		apiURL: strings.TrimSuffix(apiURL, "/"),
		token:  token,
		hc:     &http.Client{Timeout: 30 * time.Second},
	}
}

// WorkflowDir is where GitHub keeps workflow files, and where we look too.
const WorkflowDir = ".github/workflows"

// File is one workflow file read at a ref.
type File struct {
	Path    string
	Content []byte
}

type contentEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Type string `json:"type"`
}

// Workflows reads every workflow file in the repository at ref.
//
// Reading them at the triggering commit rather than from a local checkout is
// what makes "the workflow that ran is the workflow that was in the tree" true
// — the same property GitHub Actions has, and the reason a pull request can
// change its own CI.
func (c *Client) Workflows(ctx context.Context, repo, ref string) ([]File, error) {
	listURL := fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s",
		c.apiURL, repo, WorkflowDir, url.QueryEscape(ref))
	body, status, err := c.get(ctx, listURL, "application/vnd.github+json")
	if err != nil {
		return nil, err
	}
	if status == http.StatusNotFound {
		// A repository with no workflows is not an error; it just has nothing
		// for us to run.
		return nil, nil
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("list %s at %s: %s: %s", WorkflowDir, ref, http.StatusText(status), snippet(body))
	}
	var entries []contentEntry
	if err := json.Unmarshal(body, &entries); err != nil {
		return nil, fmt.Errorf("decode contents listing: %w", err)
	}

	var out []File
	for _, e := range entries {
		if e.Type != "file" || !isWorkflowFile(e.Name) {
			continue
		}
		raw, status, err := c.get(ctx,
			fmt.Sprintf("%s/repos/%s/contents/%s?ref=%s", c.apiURL, repo, e.Path, url.QueryEscape(ref)),
			"application/vnd.github.raw")
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("read %s at %s: %s", e.Path, ref, http.StatusText(status))
		}
		out = append(out, File{Path: e.Path, Content: raw})
	}
	return out, nil
}

func isWorkflowFile(name string) bool {
	return strings.HasSuffix(name, ".yml") || strings.HasSuffix(name, ".yaml")
}

// State is a commit status state, in GitHub's vocabulary.
type State string

const (
	StatePending State = "pending"
	StateSuccess State = "success"
	StateFailure State = "failure"
	// StateError is for a run that did not reach a verdict — cancelled, or the
	// engine itself broke. Reporting those as `failure` would tell a reviewer
	// the code is bad when what happened is that we stopped looking.
	StateError State = "error"
)

// Status is one commit status.
type Status struct {
	State       State  `json:"state"`
	TargetURL   string `json:"target_url,omitempty"`
	Description string `json:"description,omitempty"`
	Context     string `json:"context"`
}

// SetCommitStatus reports a run's verdict against a commit.
//
// This is the Commit Status API, not the Checks API. Check runs can only be
// created by a GitHub App installation token: a personal or OAuth token is
// refused outright. Commit statuses take any token that can write the repo and
// are honoured by branch protection's required-checks the same way, so this is
// the path that works before Orrery has an App, and the App is what upgrades it
// to annotations on the diff.
func (c *Client) SetCommitStatus(ctx context.Context, repo, sha string, st Status) error {
	if sha == "" {
		return fmt.Errorf("commit status needs a sha")
	}
	// GitHub truncates at 140 and returns 422 past it; losing the tail of a
	// description is better than losing the status.
	if len(st.Description) > 140 {
		st.Description = st.Description[:137] + "..."
	}
	payload, err := json.Marshal(st)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/repos/%s/statuses/%s", c.apiURL, repo, sha), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c.authorize(req, "application/vnd.github+json")

	res, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<10))
	if res.StatusCode/100 != 2 {
		return fmt.Errorf("set commit status on %s: %s: %s", sha[:min(7, len(sha))], res.Status, snippet(body))
	}
	return nil
}

func (c *Client) get(ctx context.Context, u, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, 0, err
	}
	c.authorize(req, accept)
	res, err := c.hc.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	// A workflow file is small; a multi-megabyte reply means something is wrong
	// and reading it all would be the wrong way to find out.
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return body, res.StatusCode, err
}

func (c *Client) authorize(req *http.Request, accept string) {
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
}

// snippet keeps an error message readable when the body is an HTML error page.
func snippet(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
