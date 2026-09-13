package server

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/feed-mob/feedmob-orrery/internal/store"
)

const opToken = "op-t0ken"

func guardedServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "auth.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	srv := New(st, Config{RegistrationToken: regToken, APIToken: opToken},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	hs := httptest.NewServer(srv)
	t.Cleanup(func() { hs.Close(); st.Close() })
	return hs
}

// POST /api/runs takes a workflow file and runs it on a runner with whatever
// secrets the server injects. Unauthenticated, the port is remote code
// execution; every one of these must be 401 without a token.
func TestOperatorEndpointsRefuseAnonymousCallers(t *testing.T) {
	hs := guardedServer(t)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/runs"},
		{"GET", "/api/runs"},
		{"GET", "/api/runs/1"},
		{"POST", "/api/runs/1/rerun"},
		{"POST", "/api/dispatch"},
		{"GET", "/api/jobs/1/logs"},
		{"POST", "/api/jobs/1/stop"},
	} {
		req, err := http.NewRequest(tc.method, hs.URL+tc.path, bytes.NewReader([]byte("{}")))
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.method, tc.path, err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", tc.method, tc.path, res.StatusCode)
		}
	}
}

func TestBearerTokenIsAccepted(t *testing.T) {
	hs := guardedServer(t)
	req, _ := http.NewRequest("GET", hs.URL+"/api/runs", nil)
	req.Header.Set("Authorization", "Bearer "+opToken)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.StatusCode)
	}
}

// The dashboard trades the token for an HttpOnly cookie, so it is not sitting
// in localStorage where every script on the page can read it.
func TestSessionCookieWorksAndIsHttpOnly(t *testing.T) {
	hs := guardedServer(t)
	res, err := http.Post(hs.URL+"/api/session", "application/json",
		bytes.NewReader([]byte(`{"token":"`+opToken+`"}`)))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("session status = %d", res.StatusCode)
	}
	var cookie *http.Cookie
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie was set")
	}
	if !cookie.HttpOnly {
		t.Error("the session cookie is readable by page scripts")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Error("the session cookie is not SameSite=Strict; another site could drive the API")
	}

	req, _ := http.NewRequest("GET", hs.URL+"/api/runs", nil)
	req.AddCookie(cookie)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res2.Body.Close()
	if res2.StatusCode != http.StatusOK {
		t.Errorf("cookie was not accepted: %d", res2.StatusCode)
	}
}

func TestSessionRefusesAWrongToken(t *testing.T) {
	hs := guardedServer(t)
	res, err := http.Post(hs.URL+"/api/session", "application/json",
		bytes.NewReader([]byte(`{"token":"guess"}`)))
	if err != nil {
		t.Fatalf("session: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", res.StatusCode)
	}
	for _, c := range res.Cookies() {
		if c.Name == sessionCookie {
			t.Error("a rejected sign-in still set a session cookie")
		}
	}
}

// The webhook authenticates by signature; requiring a bearer token there would
// simply mean GitHub could never call it.
func TestWebhookIsNotBehindTheOperatorToken(t *testing.T) {
	hs := guardedServer(t)
	res, err := http.Post(hs.URL+"/api/webhooks/github", "application/json",
		bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer res.Body.Close()
	// No webhook secret is configured in this server, so it reports that rather
	// than demanding an operator token.
	if res.StatusCode == http.StatusUnauthorized {
		t.Error("the webhook endpoint is behind the operator token; GitHub cannot present one")
	}
}

func TestHealthzStaysOpen(t *testing.T) {
	hs := guardedServer(t)
	res, err := http.Get(hs.URL + "/healthz")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Errorf("healthz = %d; a load balancer cannot present a token", res.StatusCode)
	}
}

// A signature proves the sender knows the shared secret, not that the
// repository it names is one we build. Every repo pointing at this server
// shares that secret.
func TestRepoAllowlist(t *testing.T) {
	s := &Server{cfg: Config{}}
	if !s.repoAllowed("anyone/anything") {
		t.Error("an empty list should allow everything")
	}
	s.cfg.Repos = []string{"feed-mob/app", " feed-mob/Orrery "}
	if !s.repoAllowed("feed-mob/app") {
		t.Error("a listed repo was refused")
	}
	// Whitespace in a comma-separated flag is the operator's, not a decision.
	if !s.repoAllowed("feed-mob/orrery") {
		t.Error("matching should ignore surrounding space and case, as GitHub does")
	}
	if s.repoAllowed("attacker/evil") {
		t.Error("an unlisted repo was accepted; a leaked webhook secret would run its workflows")
	}
}

// A staging deploy and a production deploy are the same workflow file with a
// different `environment:`. Giving them the same credentials means the staging
// run can reach production.
func TestSecretsAreScopedToTheEnvironment(t *testing.T) {
	s := &Server{cfg: Config{
		Secrets: map[string]string{"GITHUB_TOKEN": "shared", "SHARED": "yes"},
		EnvSecrets: map[string]map[string]string{
			"production": {"DEPLOY_KEY": "prod-key", "GITHUB_TOKEN": "prod-token"},
			"staging":    {"DEPLOY_KEY": "staging-key"},
		},
	}}

	prod := s.secretsFor("production")
	if prod["DEPLOY_KEY"] != "prod-key" {
		t.Errorf("production DEPLOY_KEY = %q", prod["DEPLOY_KEY"])
	}
	if prod["SHARED"] != "yes" {
		t.Errorf("the global set should still come through: %+v", prod)
	}
	// An environment may override a global secret, not merely add to it.
	if prod["GITHUB_TOKEN"] != "prod-token" {
		t.Errorf("production GITHUB_TOKEN = %q, want the environment's own", prod["GITHUB_TOKEN"])
	}
	if got := s.secretsFor("staging")["DEPLOY_KEY"]; got != "staging-key" {
		t.Errorf("staging DEPLOY_KEY = %q; staging can reach production", got)
	}
	// A job with no environment gets only the global set, and the map it gets
	// must not be one a previous overlay mutated.
	plain := s.secretsFor("")
	if _, leaked := plain["DEPLOY_KEY"]; leaked {
		t.Errorf("an environment's secret leaked into an ordinary job: %+v", plain)
	}
	if got := s.forgeToken("production"); got != "prod-token" {
		t.Errorf("github.token for production = %q", got)
	}
	if got := s.forgeToken(""); got != "shared" {
		t.Errorf("github.token with no environment = %q", got)
	}
}
