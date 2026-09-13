package workflow

import "testing"

func TestEnvironmentAcceptsBothShapes(t *testing.T) {
	bare := mustParse(t, "name: w\non: [push]\njobs:\n  a:\n    environment: production\n    steps:\n      - run: x\n")
	if e := bare.Jobs["a"].Environment; e == nil || e.Name != "production" {
		t.Fatalf("bare name: %+v", e)
	}
	full := mustParse(t, `
name: w
on: [push]
jobs:
  a:
    environment:
      name: production
      url: https://app.example.com
      auto-rollback: true
      version-from: image_tag
    steps:
      - run: x
`)
	e := full.Jobs["a"].Environment
	if e == nil || e.Name != "production" || e.URL != "https://app.example.com" {
		t.Fatalf("mapping: %+v", e)
	}
	if !e.AutoRollback || e.VersionFrom != "image_tag" {
		t.Errorf("orrery's own keys were dropped: %+v", e)
	}
	if none := mustParse(t, "name: w\non: [push]\njobs:\n  a:\n    steps:\n      - run: x\n"); none.Jobs["a"].Environment != nil {
		t.Error("a job without environment got one")
	}
}

func TestEnvironmentNeedsAName(t *testing.T) {
	src := "name: w\non: [push]\njobs:\n  a:\n    environment:\n      url: https://x\n    steps:\n      - run: x\n"
	if _, err := Parse([]byte(src)); err == nil {
		t.Fatal("an environment with no name was accepted")
	}
}

// An image tag is the thing an operator recognises and the thing a rollback has
// to name; the sha only identifies the source it was built from.
func TestEnvironmentVersion(t *testing.T) {
	e := &Environment{VersionFrom: "image_tag"}
	if got := e.Version(map[string]string{"image_tag": "v1.2.3"}, "abc123"); got != "v1.2.3" {
		t.Errorf("version = %q, want the named output", got)
	}
	// The named output is missing: fall through rather than report nothing.
	if got := e.Version(map[string]string{"version": "v9"}, "abc123"); got != "v9" {
		t.Errorf("version = %q, want the conventional output", got)
	}
	if got := e.Version(nil, "abc123"); got != "abc123" {
		t.Errorf("version = %q, want the sha as the fallback", got)
	}
	// A job with no environment block still deploys something.
	if got := (*Environment)(nil).Version(map[string]string{"image": "v2"}, "abc"); got != "v2" {
		t.Errorf("nil environment: %q", got)
	}
}
