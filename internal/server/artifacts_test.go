package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
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
