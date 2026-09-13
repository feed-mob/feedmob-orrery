package server

import (
	"fmt"
	"strings"
)

// ActionPolicy decides which `uses:` references a run may pull in.
//
// Without one, any workflow in any repository this server builds can run
// arbitrary third-party code on a runner that holds deploy secrets. That is
// how tj-actions/changed-files went wrong in March 2025: a mutable tag was
// repointed and every workflow following it started leaking secrets on the
// next run.
//
// An empty policy allows everything, which is the right default for a server
// that builds only its own repositories and the wrong one the moment it builds
// a repository someone outside the team can open a pull request against.
type ActionPolicy struct {
	// Allow is a list of patterns: `actions/*`, `feed-mob/*`, or an exact
	// `actions/checkout@v4`. Empty allows everything.
	Allow []string
	// RequireSHA refuses a reference pinned to a tag or branch rather than a
	// commit. A tag is a name someone else can move; a commit is not.
	RequireSHA bool
}

// Check reports the first reference that the policy refuses.
func (p *ActionPolicy) Check(uses []string) error {
	if p == nil || (len(p.Allow) == 0 && !p.RequireSHA) {
		return nil
	}
	for _, u := range uses {
		// Local and container actions are the repository's own code, already
		// covered by whatever review that repository has.
		if strings.HasPrefix(u, "./") || strings.HasPrefix(u, "docker://") {
			continue
		}
		name, ref, _ := strings.Cut(u, "@")
		if len(p.Allow) > 0 && !actionAllowed(p.Allow, name, u) {
			return fmt.Errorf("action %q is not in -allowed-actions", u)
		}
		if p.RequireSHA && !isCommitSHA(ref) {
			return fmt.Errorf("action %q must be pinned to a commit sha, not %q: "+
				"a tag is a name someone else can move", u, ref)
		}
	}
	return nil
}

func actionAllowed(allow []string, name, full string) bool {
	for _, pattern := range allow {
		pattern = strings.TrimSpace(pattern)
		switch {
		case pattern == full, pattern == name:
			return true
		case strings.HasSuffix(pattern, "/*"):
			if owner, _, ok := strings.Cut(name, "/"); ok && owner+"/*" == pattern {
				return true
			}
		case pattern == "*":
			return true
		}
	}
	return false
}

// isCommitSHA reports whether a ref is a full 40-character commit.
//
// Only the full form: an abbreviated sha is ambiguous, and GitHub resolves
// short refs against tags first, which puts the mutable name back.
func isCommitSHA(ref string) bool {
	if len(ref) != 40 {
		return false
	}
	for _, c := range ref {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
