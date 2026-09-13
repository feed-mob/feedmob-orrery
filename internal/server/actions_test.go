package server

import "testing"

// tj-actions/changed-files, March 2025: a mutable tag was repointed and every
// workflow following it started leaking secrets on the next run.
func TestActionPolicy(t *testing.T) {
	none := &ActionPolicy{}
	if err := none.Check([]string{"anyone/anything@v1"}); err != nil {
		t.Errorf("an empty policy should allow everything: %v", err)
	}
	// A nil policy is the common case and must answer, not panic.
	if err := (*ActionPolicy)(nil).Check([]string{"x/y@v1"}); err != nil {
		t.Errorf("nil policy: %v", err)
	}

	p := &ActionPolicy{Allow: []string{"actions/*", "feed-mob/deploy-action@v2"}}
	for _, ok := range []string{
		"actions/checkout@v4",
		"actions/setup-node@v4",
		"feed-mob/deploy-action@v2",
		"./.github/actions/local", // the repository's own code
		"docker://alpine:3.20",    // a container, not third-party action code
	} {
		if err := p.Check([]string{ok}); err != nil {
			t.Errorf("%s was refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		"tj-actions/changed-files@v35",
		"feed-mob/deploy-action@v3", // the exact pin does not cover other refs
		"actionsx/checkout@v4",      // a lookalike owner
	} {
		if err := p.Check([]string{bad}); err == nil {
			t.Errorf("%s was allowed", bad)
		}
	}
}

// A tag is a name someone else can move; a commit is not.
func TestRequireSHA(t *testing.T) {
	p := &ActionPolicy{RequireSHA: true}
	const sha = "11bd71901bbe5b1630ceea73d27597364c9af683"
	if err := p.Check([]string{"actions/checkout@" + sha}); err != nil {
		t.Errorf("a sha-pinned action was refused: %v", err)
	}
	for _, bad := range []string{"actions/checkout@v4", "actions/checkout@main"} {
		if err := p.Check([]string{bad}); err == nil {
			t.Errorf("%s was allowed under require-sha", bad)
		}
	}
	// An abbreviated sha resolves against tags first on GitHub, which puts the
	// mutable name straight back.
	if err := p.Check([]string{"actions/checkout@11bd719"}); err == nil {
		t.Error("an abbreviated sha was accepted")
	}
	// Local and container references have no ref to pin.
	if err := p.Check([]string{"./.github/actions/x", "docker://alpine:3"}); err != nil {
		t.Errorf("local/container refs under require-sha: %v", err)
	}
}
