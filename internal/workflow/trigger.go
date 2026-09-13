package workflow

import (
	"fmt"
	"strings"
)

// Event is something that happened, offered to a workflow's `on:` for matching.
type Event struct {
	// Name is the webhook event: push, pull_request, schedule, …
	Name string
	// Ref is refs/heads/<branch> or refs/tags/<tag>. Empty for events with no
	// ref of their own.
	Ref string
	// BaseRef is the branch a pull request targets, which is what `branches:`
	// filters against for pull_request — not the PR's own head.
	BaseRef string
	// Action is the activity type: opened, synchronize, closed, …
	Action string
	// Paths are the files the event touched, for paths/paths-ignore.
	Paths []string
}

// Trigger is one entry under `on:`, with its filters.
type Trigger struct {
	Event string

	Branches       []string
	BranchesIgnore []string
	Tags           []string
	TagsIgnore     []string
	Paths          []string
	PathsIgnore    []string
	Types          []string
	Cron           []string
}

// defaultTypes are the activity types GitHub assumes when `types:` is absent.
// Getting these wrong is the difference between a workflow that runs on every
// label change and one that does not run at all.
var defaultTypes = map[string][]string{
	"pull_request":        {"opened", "synchronize", "reopened"},
	"pull_request_target": {"opened", "synchronize", "reopened"},
	"issues":              {"opened", "edited", "deleted", "transferred", "pinned", "unpinned", "closed", "reopened", "assigned", "unassigned", "labeled", "unlabeled", "locked", "unlocked", "milestoned", "demilestoned"},
	"issue_comment":       {"created", "edited", "deleted"},
	"release":             {"published", "unpublished", "created", "edited", "deleted", "prereleased", "released"},
}

// Triggers reads `on:` in all three shapes GitHub accepts: a bare string, a
// list of strings, or a mapping from event to filters.
func (wf *Workflow) Triggers() ([]Trigger, error) {
	switch v := wf.On.(type) {
	case nil:
		return nil, nil
	case string:
		return []Trigger{{Event: v}}, nil
	case []any:
		out := make([]Trigger, 0, len(v))
		for _, item := range v {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("on: list entry %v is not an event name", item)
			}
			out = append(out, Trigger{Event: name})
		}
		return out, nil
	case map[string]any:
		return triggersFromMap(v)
	default:
		return nil, fmt.Errorf("on: must be a string, a list or a mapping, got %T", wf.On)
	}
}

func triggersFromMap(m map[string]any) ([]Trigger, error) {
	out := make([]Trigger, 0, len(m))
	for name, raw := range m {
		t := Trigger{Event: name}
		switch f := raw.(type) {
		case nil:
			// `on: {push:}` — the event with no filters.
		case map[string]any:
			t.Branches = stringsOf(f["branches"])
			t.BranchesIgnore = stringsOf(f["branches-ignore"])
			t.Tags = stringsOf(f["tags"])
			t.TagsIgnore = stringsOf(f["tags-ignore"])
			t.Paths = stringsOf(f["paths"])
			t.PathsIgnore = stringsOf(f["paths-ignore"])
			t.Types = stringsOf(f["types"])
		case []any:
			// `on: {schedule: [{cron: …}]}`
			for _, item := range f {
				if entry, ok := item.(map[string]any); ok {
					if c, ok := entry["cron"].(string); ok {
						t.Cron = append(t.Cron, c)
					}
				}
			}
		default:
			return nil, fmt.Errorf("on.%s: unexpected %T", name, raw)
		}
		if err := t.validate(); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// validate rejects the filter pairs GitHub rejects. Silently honouring one and
// dropping the other is how a workflow ends up running on refs its author
// believed were excluded.
func (t *Trigger) validate() error {
	for _, pair := range []struct {
		a, b   []string
		an, bn string
	}{
		{t.Branches, t.BranchesIgnore, "branches", "branches-ignore"},
		{t.Tags, t.TagsIgnore, "tags", "tags-ignore"},
		{t.Paths, t.PathsIgnore, "paths", "paths-ignore"},
	} {
		if len(pair.a) > 0 && len(pair.b) > 0 {
			return fmt.Errorf("on.%s: %s and %s cannot both be used", t.Event, pair.an, pair.bn)
		}
	}
	return nil
}

func stringsOf(v any) []string {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return []string{x}
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// Matches reports whether any of the workflow's triggers accepts this event,
// and which one.
func (wf *Workflow) Matches(ev Event) (*Trigger, bool, error) {
	triggers, err := wf.Triggers()
	if err != nil {
		return nil, false, err
	}
	for i := range triggers {
		if triggers[i].Matches(ev) {
			return &triggers[i], true, nil
		}
	}
	return nil, false, nil
}

// Matches applies one trigger's filters to an event.
func (t *Trigger) Matches(ev Event) bool {
	if t.Event != ev.Name {
		return false
	}
	if !t.typeMatches(ev) {
		return false
	}
	if !t.refMatches(ev) {
		return false
	}
	return t.pathMatches(ev)
}

func (t *Trigger) typeMatches(ev Event) bool {
	if ev.Action == "" {
		return true
	}
	want := t.Types
	if len(want) == 0 {
		want = defaultTypes[t.Event]
	}
	if len(want) == 0 {
		return true
	}
	return matchAny(want, ev.Action)
}

// refMatches applies branches/tags filters.
//
// The rule that catches people out: declaring `branches` without `tags` does
// not merely filter tag pushes, it excludes them entirely, and the reverse
// holds too. A workflow with `branches: [main]` does not run on `v1.0.0`.
func (t *Trigger) refMatches(ev Event) bool {
	// pull_request filters on the branch being merged into, not the head.
	ref := ev.Ref
	if strings.HasPrefix(t.Event, "pull_request") && ev.BaseRef != "" {
		ref = "refs/heads/" + strings.TrimPrefix(ev.BaseRef, "refs/heads/")
	}
	if ref == "" {
		return true
	}
	isTag := strings.HasPrefix(ref, "refs/tags/")
	name := strings.TrimPrefix(strings.TrimPrefix(ref, "refs/heads/"), "refs/tags/")

	hasBranchFilter := len(t.Branches) > 0 || len(t.BranchesIgnore) > 0
	hasTagFilter := len(t.Tags) > 0 || len(t.TagsIgnore) > 0
	if !hasBranchFilter && !hasTagFilter {
		return true
	}

	if isTag {
		if !hasTagFilter {
			return false
		}
		if len(t.Tags) > 0 {
			return matchAny(t.Tags, name)
		}
		return !matchAny(t.TagsIgnore, name)
	}
	if !hasBranchFilter {
		return false
	}
	if len(t.Branches) > 0 {
		return matchAny(t.Branches, name)
	}
	return !matchAny(t.BranchesIgnore, name)
}

func (t *Trigger) pathMatches(ev Event) bool {
	switch {
	case len(t.Paths) > 0:
		// An event that reports no paths cannot satisfy a paths filter. Saying
		// "no information, so run it" would make a paths-filtered workflow fire
		// on every event whose diff we failed to read.
		for _, p := range ev.Paths {
			if matchAny(t.Paths, p) {
				return true
			}
		}
		return false
	case len(t.PathsIgnore) > 0:
		// Runs if any changed file is outside the ignore list.
		for _, p := range ev.Paths {
			if !matchAny(t.PathsIgnore, p) {
				return true
			}
		}
		return len(ev.Paths) == 0
	}
	return true
}

func matchAny(patterns []string, s string) bool {
	matched := false
	for _, p := range patterns {
		// A leading ! removes what an earlier pattern added, so order matters
		// and the last word wins.
		if neg, ok := strings.CutPrefix(p, "!"); ok {
			if globMatch(neg, s) {
				matched = false
			}
			continue
		}
		if globMatch(p, s) {
			matched = true
		}
	}
	return matched
}

// globMatch implements GitHub's filter pattern subset: `*` matches within a
// path segment, `**` matches across segments, `?` matches one character.
//
// Written as a backtracking matcher rather than by translating to a regexp,
// because the `*` / `**` distinction is not expressible as a single character
// class and the translation is where subtle escaping bugs live.
func globMatch(pattern, s string) bool {
	return globHere([]rune(pattern), []rune(s))
}

func globHere(p, s []rune) bool {
	for len(p) > 0 {
		switch p[0] {
		case '*':
			if len(p) > 1 && p[1] == '*' {
				rest := p[2:]
				// `**/` should also match zero segments, so a/**/b matches a/b.
				if len(rest) > 0 && rest[0] == '/' {
					if globHere(rest[1:], s) {
						return true
					}
				}
				for i := 0; i <= len(s); i++ {
					if globHere(rest, s[i:]) {
						return true
					}
				}
				return false
			}
			rest := p[1:]
			for i := 0; i <= len(s); i++ {
				if i > 0 && s[i-1] == '/' {
					break // a single * does not cross a separator
				}
				if globHere(rest, s[i:]) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 || s[0] == '/' {
				return false
			}
			p, s = p[1:], s[1:]
		default:
			if len(s) == 0 || s[0] != p[0] {
				return false
			}
			p, s = p[1:], s[1:]
		}
	}
	return len(s) == 0
}
