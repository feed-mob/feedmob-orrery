package server

import (
	"crypto/subtle"
	"net/http"
	"strings"
)

// operator guards the endpoints a person drives.
//
// These are not read-only conveniences: POST /api/runs takes a workflow file
// and runs it on a runner with whatever secrets this server injects, and
// /api/jobs/{id}/logs returns everything a job printed. Left open, the port is
// arbitrary code execution plus a secrets oracle for anyone who can reach it.
//
// The runner RPCs have their own credentials and the webhook has its signature;
// this covers the rest.
func (s *Server) operator(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.cfg.APIToken == "" {
			// No token configured means the operator passed -insecure-no-auth;
			// main refuses to start otherwise.
			h(w, r)
			return
		}
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="orrery"`)
			writeErr(w, http.StatusUnauthorized, "a valid API token is required")
			return
		}
		h(w, r)
	}
}

// authorized accepts the token from a header or a cookie. The cookie is what
// lets the dashboard work after one sign-in; the header is what a script uses.
func (s *Server) authorized(r *http.Request) bool {
	if tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && tokenEqual(tok, s.cfg.APIToken) {
		return true
	}
	if tokenEqual(r.Header.Get("X-Orrery-Token"), s.cfg.APIToken) {
		return true
	}
	if c, err := r.Cookie(sessionCookie); err == nil && tokenEqual(c.Value, s.cfg.APIToken) {
		return true
	}
	return false
}

const sessionCookie = "orrery_token"

// tokenEqual compares in constant time. A fast reject on the first wrong byte
// hands the token back one character at a time to anyone willing to measure.
func tokenEqual(got, want string) bool {
	if got == "" || want == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// handleSession trades a token for a cookie, so the dashboard asks once instead
// of keeping the token in browser storage where every script on the page can
// read it.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	req, err := decode[struct {
		Token string `json:"token"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if s.cfg.APIToken != "" && !tokenEqual(req.Token, s.cfg.APIToken) {
		writeErr(w, http.StatusUnauthorized, "wrong token")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    req.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		// Secure is set when the request arrived over TLS. Forcing it on a
		// plain-HTTP deployment would silently drop the cookie and leave the
		// dashboard looking broken rather than insecure.
		Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https",
		MaxAge: 30 * 24 * 3600,
	})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// handleWhoAmI lets the dashboard find out whether it needs to ask for a token
// before it renders anything.
func (s *Server) handleWhoAmI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{
		"auth_required": s.cfg.APIToken != "",
		"authorized":    s.cfg.APIToken == "" || s.authorized(r),
	})
}
