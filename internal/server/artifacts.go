package server

import (
	gocontext "context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"gitea.com/gitea/runner/act/artifacts"
)

// ArtifactStore is a shared artifact server, so `download-artifact` finds what
// `upload-artifact` put there even when the two jobs ran on different runners.
//
// Runner-local storage is act's design and Gitea's deployment, and it is
// invisible with one runner. With two it fails in the worst way available: the
// downstream job does not error, it finds nothing — or it errors about a file
// that exists, on another machine.
//
// act's own implementation of the v3 protocol is reused rather than rewritten.
// It is nine routes of wire format that has to match what actions/upload-artifact
// and actions/download-artifact expect, and a second implementation would drift
// from the first bug fix onward. What act's version does not have is any
// authentication whatsoever — on a runner's loopback that is survivable, on a
// control plane reachable by every job container it is not. So act's server
// binds to loopback here and every request arrives through the proxy below,
// which authenticates first.
//
// The credential is per *runner*, not per job, because that is the only thing
// act can express: it reads ACTIONS_RUNTIME_TOKEN from the process environment
// (act/runner/run_context.go:1147), so a runner executing two jobs at once has
// one value to give them both. Authorization makes up the difference — the run
// id appears in every route of the protocol, and a request is allowed only if
// the runner presenting the token currently holds a job in that run.
type ArtifactStore struct {
	proxy *httputil.ReverseProxy
	dir   string
	addr  string
	stop  func()
	log   *slog.Logger

	// holds answers "does this runner have a job running in this run" and is
	// the store, injected to keep this file free of SQL.
	holds func(ctx gocontext.Context, runnerID, runID int64) (bool, error)

	mu sync.RWMutex
	// tokens maps a runner's artifact credential to its runner id. In memory
	// only: a credential that does not survive a restart is one that cannot
	// leak from a backup, and runners re-register theirs on reconnect.
	tokens map[string]int64
}

// NewArtifactStore starts act's artifact server on loopback and returns the
// authenticating proxy in front of it. addr is what job containers will be told
// to talk to, and it must be an address they can actually route to — the same
// constraint the runner-local servers have, moved one hop.
func NewArtifactStore(dir, addr string, log *slog.Logger) (*ArtifactStore, error) {
	// Port 0 would be better but act's Serve takes the port as a string and
	// reports nothing back, so we pick one and hand it the same number.
	port, err := freePort()
	if err != nil {
		return nil, err
	}
	stop := artifacts.Serve(gocontext.Background(), dir, "127.0.0.1", strconv.Itoa(port))
	target, err := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	if err != nil {
		stop()
		return nil, err
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	// act builds the URLs it hands back to the client out of req.Host
	// (artifacts/server.go:102). The default director leaves req.Host alone, so
	// those URLs name this control plane and not the loopback server — which is
	// the whole reason a path-prefixed mount would not work and this listens on
	// its own port.
	return &ArtifactStore{
		proxy:  proxy,
		dir:    dir,
		addr:   addr,
		stop:   func() { stop() },
		log:    log,
		tokens: map[string]int64{},
	}, nil
}

// URL is what a job's steps get as ACTIONS_RUNTIME_URL. The trailing slash is
// load-bearing for the same reason it is on the cache URL: the action
// concatenates rather than resolves.
func (a *ArtifactStore) URL() string {
	if a == nil {
		return ""
	}
	return "http://" + a.addr + "/"
}

// register binds a runner's artifact credential. Called by the runner over the
// authenticated API, so the token itself never travels unauthenticated.
func (a *ArtifactStore) register(runnerID int64, token string) {
	if a == nil || token == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// One credential per runner: re-registering replaces the old one rather
	// than accumulating, so a restarted runner does not leave a live token
	// behind it for the life of the process.
	for t, id := range a.tokens {
		if id == runnerID {
			delete(a.tokens, t)
		}
	}
	a.tokens[token] = runnerID
}

// Handler serves the v3 artifact protocol to job containers.
func (a *ArtifactStore) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runnerID, ok := a.authenticate(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", `Bearer realm="orrery-artifacts"`)
			http.Error(w, "artifact credential required", http.StatusUnauthorized)
			return
		}
		runID, ok := runFromPath(r.URL.Path)
		if !ok {
			// Every route of the protocol names a run. A request that does not
			// is not one we know how to authorize, so it does not get served.
			http.Error(w, "unrecognised artifact path", http.StatusNotFound)
			return
		}
		held, err := a.holds(r.Context(), runnerID, runID)
		if err != nil {
			http.Error(w, "artifact authorization failed", http.StatusInternalServerError)
			return
		}
		if !held {
			// A runner may read and write the artifacts of runs it is actually
			// executing, and nothing else. Without this the credential handed
			// to a job container would be a key to every artifact on the
			// system, including other repositories'.
			a.log.Warn("artifact request outside the runner's own runs",
				"runner", runnerID, "run", runID, "path", r.URL.Path)
			http.Error(w, "not your run", http.StatusForbidden)
			return
		}
		a.proxy.ServeHTTP(w, r)
	})
}

func (a *ArtifactStore) authenticate(r *http.Request) (int64, bool) {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return 0, false
	}
	tok = strings.TrimSpace(tok)
	if tok == "" {
		return 0, false
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	// Constant-time over the whole table: a map lookup on a secret leaks
	// nothing useful by timing in practice, but the comparison is cheap and
	// the habit is what keeps the next credential check honest.
	var found int64
	var hit bool
	for candidate, id := range a.tokens {
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(tok)) == 1 {
			found, hit = id, true
		}
	}
	return found, hit
}

// runFromPath pulls the run id out of the v3 protocol's routes. All six carry
// it, which is what makes run-scoped authorization possible at all:
//
//	POST   /_apis/pipelines/workflows/{run}/artifacts
//	PATCH  /_apis/pipelines/workflows/{run}/artifacts
//	GET    /_apis/pipelines/workflows/{run}/artifacts
//	PUT    /upload/{run}
//	GET    /download/{run}
//	GET    /artifact/{run}/{name}/...
func runFromPath(p string) (int64, bool) {
	parts := strings.Split(strings.Trim(p, "/"), "/")
	at := -1
	switch {
	case len(parts) >= 4 && parts[0] == "_apis" && parts[1] == "pipelines" && parts[2] == "workflows":
		at = 3
	case len(parts) >= 2 && (parts[0] == "upload" || parts[0] == "download" || parts[0] == "artifact"):
		at = 1
	}
	if at < 0 {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[at], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Close stops the loopback server. The files stay; they are the point.
func (a *ArtifactStore) Close() {
	if a != nil && a.stop != nil {
		a.stop()
	}
}

// freePort asks the OS for an unused port and gives it straight back. There is
// a race between the close and act's bind; on a machine where something else
// takes that port in the gap act fails loudly at startup rather than silently
// serving nothing.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := l.Addr().(*net.TCPAddr).Port
	return port, l.Close()
}

// handleArtifactSession takes a runner's artifact credential and returns the
// URL its job containers should use. A server with no shared store configured
// answers with an empty URL and the runner keeps serving artifacts itself,
// which is what makes the shared store an upgrade rather than a flag day.
func (s *Server) handleArtifactSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "malformed artifact session request")
		return
	}
	if s.artifacts == nil {
		writeJSON(w, http.StatusOK, map[string]string{"url": ""})
		return
	}
	if len(req.Token) < 32 {
		// A short credential is either a bug or an attempt to register a
		// guessable one. Both are worth refusing out loud.
		writeErr(w, http.StatusBadRequest, "artifact credential must be at least 32 characters")
		return
	}
	runner := runnerFrom(r.Context())
	s.artifacts.register(runner.ID, req.Token)
	s.log.Info("artifact credential registered", "runner", runner.ID, "url", s.artifacts.URL())
	writeJSON(w, http.StatusOK, map[string]string{"url": s.artifacts.URL()})
}
