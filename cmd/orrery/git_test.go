package main

import "testing"

// The github context is built from these, and actions read it literally:
// actions/checkout rejects anything that is not owner/repo.
func TestRepoFromRemote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"git@github.com:feed-mob/feedmob-orrery.git", "feed-mob/feedmob-orrery"},
		{"https://github.com/feed-mob/feedmob-orrery.git", "feed-mob/feedmob-orrery"},
		{"https://github.com/feed-mob/feedmob-orrery", "feed-mob/feedmob-orrery"},
		{"ssh://git@github.com/feed-mob/feedmob-orrery.git", "feed-mob/feedmob-orrery"},
		{"https://git.feedmob.internal:3000/team/app.git", "team/app"},
		{"not-a-remote", ""},
	} {
		if got := repoFromRemote(tc.in); got != tc.want {
			t.Errorf("repoFromRemote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
