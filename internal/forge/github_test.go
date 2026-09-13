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
