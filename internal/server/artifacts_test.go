package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The proxy is the only thing between a job container and every artifact on the
// system, because act's server underneath has no authentication at all. These
// tests are about that boundary, not about the protocol behind it.

func testStore(t *testing.T, holds func(ctx context.Context, runnerID, runID int64) (bool, error)) *ArtifactStore {
	t.Helper()
	a, err := NewArtifactStore(t.TempDir(), "127.0.0.1:1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new artifact store: %v", err)
	}
	t.Cleanup(a.Close)
	a.holds = holds
	return a
}

func artifactReq(t *testing.T, a *ArtifactStore, token, path string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

func TestArtifactStoreRefusesWithoutCredential(t *testing.T) {
	a := testStore(t, func(context.Context, int64, int64) (bool, error) { return true, nil })
	for _, tok := range []string{"", "wrong-token"} {
		w := artifactReq(t, a, tok, "/_apis/pipelines/workflows/7/artifacts")
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("token %q: got %d, want 401", tok, w.Code)
		}
	}
}

func TestArtifactStoreRefusesAnotherRunsArtifacts(t *testing.T) {
	// The credential is per runner and every route names a run, so this is the
	// check that stops a job in one repository from reading another's
	// artifacts with the credential it was legitimately given.
	var asked []int64
	a := testStore(t, func(_ context.Context, runnerID, runID int64) (bool, error) {
		asked = append(asked, runID)
		return runID == 7, nil
	})
	a.register(42, strings.Repeat("a", 64))

	if w := artifactReq(t, a, strings.Repeat("a", 64), "/download/8"); w.Code != http.StatusForbidden {
		t.Fatalf("run 8 with a credential for run 7: got %d, want 403", w.Code)
	}
	if len(asked) != 1 || asked[0] != 8 {
		t.Fatalf("authorization asked about %v, want [8]", asked)
	}
}

func TestArtifactStoreRunFromEveryRoute(t *testing.T) {
	// If a route's run id cannot be parsed the request is refused, so a new
	// route in a future act would fail closed rather than serve unauthorized.
	cases := map[string]int64{
		"/_apis/pipelines/workflows/12/artifacts": 12,
		"/upload/12":                 12,
		"/download/12":               12,
		"/artifact/12/build/out.txt": 12,
	}
	for path, want := range cases {
		got, ok := runFromPath(path)
		if !ok || got != want {
			t.Errorf("runFromPath(%q) = %d, %v; want %d, true", path, got, ok, want)
		}
	}
	for _, path := range []string{"/", "/upload", "/artifact", "/upload/not-a-number", "/_apis/other"} {
		if _, ok := runFromPath(path); ok {
			t.Errorf("runFromPath(%q) parsed a run id it should not have", path)
		}
	}
}

func TestArtifactStoreUnknownRouteIsRefusedNotProxied(t *testing.T) {
	a := testStore(t, func(context.Context, int64, int64) (bool, error) {
		t.Fatal("authorization consulted for a path with no run id")
		return false, nil
	})
	a.register(42, strings.Repeat("b", 64))
	if w := artifactReq(t, a, strings.Repeat("b", 64), "/etc/passwd"); w.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404", w.Code)
	}
}

func TestArtifactStoreOneCredentialPerRunner(t *testing.T) {
	// A restarted runner registers a fresh credential. The old one has to stop
	// working, or every restart leaves a live key behind for the life of the
	// server process.
	a := testStore(t, func(context.Context, int64, int64) (bool, error) { return true, nil })
	old, current := strings.Repeat("c", 64), strings.Repeat("d", 64)
	a.register(42, old)
	a.register(42, current)

	if w := artifactReq(t, a, old, "/download/1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("the superseded credential still works: got %d, want 401", w.Code)
	}
	if _, ok := a.authenticate(bearerReq(current)); !ok {
		t.Fatal("the current credential was rejected")
	}
}

func bearerReq(tok string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/download/1", nil)
	r.Header.Set("Authorization", "Bearer "+tok)
	return r
}

func TestArtifactPruneRemovesOnlyDeadRuns(t *testing.T) {
	// Retention deletes run rows; before this, their files stayed on disk for
	// good. On a control plane sharing a disk with anything else that is the
	// same unbounded-growth failure the cache cap exists to prevent.
	dir := t.TempDir()
	a, err := NewArtifactStore(dir, "127.0.0.1:1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new artifact store: %v", err)
	}
	t.Cleanup(a.Close)

	write := func(run, name string, size int) {
		t.Helper()
		p := filepath.Join(dir, run, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("7", "build/out.bin", 2048) // run still exists
	write("8", "build/out.bin", 4096) // run was pruned
	// Not a run id at all — it did not come from the protocol, so it is left
	// alone rather than guessed at.
	write("notes", "readme.txt", 10)

	removed, freed, err := a.prune(context.Background(), func(_ context.Context, id int64) (bool, error) {
		return id == 7, nil
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed %d runs, want 1", removed)
	}
	if freed < 4096 {
		t.Errorf("freed %d bytes, want at least 4096", freed)
	}
	if _, err := os.Stat(filepath.Join(dir, "7")); err != nil {
		t.Error("the artifacts of a live run were deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, "8")); !os.IsNotExist(err) {
		t.Error("the artifacts of a pruned run survived")
	}
	if _, err := os.Stat(filepath.Join(dir, "notes")); err != nil {
		t.Error("a directory that is not a run id was deleted")
	}
}

func TestArtifactPruneStopsOnAFailedLookup(t *testing.T) {
	// If the database cannot answer, deleting is a guess. Stop instead — the
	// next cycle is an hour away and the files are not going anywhere.
	dir := t.TempDir()
	a, err := NewArtifactStore(dir, "127.0.0.1:1", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("new artifact store: %v", err)
	}
	t.Cleanup(a.Close)
	if err := os.MkdirAll(filepath.Join(dir, "9"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, _, err := a.prune(context.Background(), func(context.Context, int64) (bool, error) {
		return false, errDB
	}); err == nil {
		t.Fatal("prune swallowed a database error")
	}
	if _, err := os.Stat(filepath.Join(dir, "9")); err != nil {
		t.Fatal("artifacts were deleted on the strength of a failed lookup")
	}
}

var errDB = errors.New("database unavailable")
