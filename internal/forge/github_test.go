package forge

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWorkflowsReadsOnlyYAMLAtTheGivenRef(t *testing.T) {
	var refs []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		refs = append(refs, r.URL.Query().Get("ref"))
		if strings.HasSuffix(r.URL.Path, "/contents/.github/workflows") {
			_ = json.NewEncoder(w).Encode([]contentEntry{
				{Name: "ci.yml", Path: ".github/workflows/ci.yml", Type: "file"},
				{Name: "deploy.yaml", Path: ".github/workflows/deploy.yaml", Type: "file"},
				{Name: "README.md", Path: ".github/workflows/README.md", Type: "file"},
				{Name: "shared", Path: ".github/workflows/shared", Type: "dir"},
			})
			return
		}
		_, _ = io.WriteString(w, "name: "+r.URL.Path)
	}))
	defer srv.Close()

	files, err := New(srv.URL, "t").Workflows(context.Background(), "o/r", "refs/heads/main")
	if err != nil {
		t.Fatalf("Workflows: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("got %d files, want the two YAML ones: %+v", len(files), files)
	}
	// Reading at the triggering commit is what makes "the workflow that ran is
	// the workflow that was in the tree" true.
	for _, ref := range refs {
		if ref != "refs/heads/main" {
			t.Errorf("a request went out at ref %q", ref)
		}
	}
}

// A repository with no workflows is not an error; it just has nothing to run.
func TestWorkflowsTreatsAMissingDirectoryAsEmpty(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	files, err := New(srv.URL, "").Workflows(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("a repo without workflows should not be an error: %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("got %d files", len(files))
	}
}

// GitHub returns 422 for a description past 140 characters. Losing the tail of
// a description is better than losing the status.
func TestSetCommitStatusTruncatesLongDescriptions(t *testing.T) {
	var got Status
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	long := strings.Repeat("x", 500)
	if err := New(srv.URL, "t").SetCommitStatus(context.Background(), "o/r", "abc123",
		Status{State: StateFailure, Context: "orrery / CI", Description: long}); err != nil {
		t.Fatalf("SetCommitStatus: %v", err)
	}
	if len(got.Description) != 140 || !strings.HasSuffix(got.Description, "...") {
		t.Errorf("description length %d, suffix %q", len(got.Description), got.Description[max(0, len(got.Description)-3):])
	}
}

func TestSetCommitStatusNeedsASha(t *testing.T) {
	if err := New("https://api.github.com", "t").SetCommitStatus(
		context.Background(), "o/r", "", Status{State: StateSuccess}); err == nil {
		t.Fatal("accepted a status with no commit to attach it to")
	}
}

func TestSetCommitStatusSurfacesAForgeRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"message":"Resource not accessible by integration"}`, http.StatusForbidden)
	}))
	defer srv.Close()
	err := New(srv.URL, "t").SetCommitStatus(context.Background(), "o/r", "abc1234",
		Status{State: StateSuccess, Context: "orrery / CI"})
	if err == nil || !strings.Contains(err.Error(), "not accessible") {
		t.Fatalf("err = %v, want the forge's own message", err)
	}
}

// A run started by a person or a cron carries a ref and no commit. Leaving the
// sha empty does not fail loudly — it fails as "reference not found" three
// layers down, inside a reusable workflow resolution.
func TestResolveRef(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.Path
		if r.Header.Get("Accept") != "application/vnd.github.sha" {
			t.Errorf("Accept = %q; asking for the whole commit wastes a few KB per run",
				r.Header.Get("Accept"))
		}
		_, _ = io.WriteString(w, "beefcafebeefcafebeefcafebeefcafebeefcafe\n")
	}))
	defer srv.Close()

	sha, err := New(srv.URL, "t").ResolveRef(context.Background(), "o/r", "main")
	if err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if sha != "beefcafebeefcafebeefcafebeefcafebeefcafe" {
		t.Errorf("sha = %q", sha)
	}
	if asked != "/repos/o/r/commits/main" {
		t.Errorf("asked %q", asked)
	}
}

// A ref with a slash — release/2026-09 — must not break the path.
func TestResolveRefEscapesTheRef(t *testing.T) {
	var asked string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked = r.URL.EscapedPath()
		_, _ = io.WriteString(w, "beefcafebeefcafebeefcafebeefcafebeefcafe")
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").ResolveRef(context.Background(), "o/r", "release/2026-09"); err != nil {
		t.Fatalf("ResolveRef: %v", err)
	}
	if !strings.Contains(asked, "release%2F2026-09") {
		t.Errorf("escaped path = %q, want the slash escaped", asked)
	}
}

func TestResolveRefSurfacesAMissingRef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "No commit found for SHA", http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").ResolveRef(context.Background(), "o/r", "nope"); err == nil {
		t.Fatal("a missing ref resolved successfully")
	}
}

// A body that is not a sha must be refused rather than passed on as one.
func TestResolveRefRejectsNonsense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "t").ResolveRef(context.Background(), "o/r", "main"); err == nil {
		t.Fatal("accepted a two-character body as a commit sha")
	}
}
