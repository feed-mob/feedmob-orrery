package server

import "testing"

func TestForgeDefaults(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		in                            Forge
		wantURL, wantAPI, wantGraphQL string
	}{
		{
			name:    "empty means github.com, whose API lives on another host",
			in:      Forge{},
			wantURL: "https://github.com", wantAPI: "https://api.github.com",
			wantGraphQL: "https://api.github.com/graphql",
		},
		{
			name:    "a trailing slash must not double up in derived URLs",
			in:      Forge{URL: "https://github.com/"},
			wantURL: "https://github.com", wantAPI: "https://api.github.com",
			wantGraphQL: "https://api.github.com/graphql",
		},
		{
			name:    "another forge follows the GHES convention",
			in:      Forge{URL: "https://git.feedmob.internal"},
			wantURL: "https://git.feedmob.internal", wantAPI: "https://git.feedmob.internal/api/v3",
			wantGraphQL: "https://git.feedmob.internal/api/graphql",
		},
		{
			name:    "an explicit API URL is never second-guessed",
			in:      Forge{URL: "https://gitea.feedmob.internal", APIURL: "https://gitea.feedmob.internal/api/v1"},
			wantURL: "https://gitea.feedmob.internal", wantAPI: "https://gitea.feedmob.internal/api/v1",
			wantGraphQL: "https://gitea.feedmob.internal/api/graphql",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := tc.in
			f.withDefaults()
			if f.URL != tc.wantURL || f.APIURL != tc.wantAPI || f.GraphQLURL != tc.wantGraphQL {
				t.Errorf("got %+v, want %s / %s / %s", f, tc.wantURL, tc.wantAPI, tc.wantGraphQL)
			}
		})
	}
}
