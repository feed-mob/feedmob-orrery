package workflow

import "testing"

const dispatchWF = `
name: Deploy
on:
  workflow_dispatch:
    inputs:
      environment:
        description: where to deploy
        required: true
        type: choice
        options: [staging, production]
      version:
        type: string
        default: latest
      dry_run:
        type: boolean
        default: false
      replicas:
        type: number
        default: 2
jobs:
  a:
    steps:
      - run: x
`

func TestDispatchInputs(t *testing.T) {
	declared, ok, err := mustParse(t, dispatchWF).DispatchInputs()
	if err != nil || !ok {
		t.Fatalf("DispatchInputs: ok=%v err=%v", ok, err)
	}
	if len(declared) != 4 {
		t.Fatalf("got %d inputs, want 4", len(declared))
	}
	if e := declared["environment"]; !e.Required || e.Type != "choice" || len(e.Options) != 2 {
		t.Errorf("environment = %+v", e)
	}
	if v := declared["version"]; v.Default != "latest" {
		t.Errorf("version default = %q", v.Default)
	}
	// A YAML boolean and a YAML number must survive as usable defaults.
	if d := declared["dry_run"]; d.Default != "false" {
		t.Errorf("dry_run default = %q", d.Default)
	}
	if r := declared["replicas"]; r.Default != "2" {
		t.Errorf("replicas default = %q", r.Default)
	}
}

func TestDispatchDeclaredWithoutInputs(t *testing.T) {
	for _, src := range []string{
		"name: w\non: workflow_dispatch\njobs:\n  a:\n    steps:\n      - run: x\n",
		"name: w\non: [push, workflow_dispatch]\njobs:\n  a:\n    steps:\n      - run: x\n",
		"name: w\non:\n  workflow_dispatch:\njobs:\n  a:\n    steps:\n      - run: x\n",
	} {
		_, ok, err := mustParse(t, src).DispatchInputs()
		if err != nil || !ok {
			t.Errorf("%q: ok=%v err=%v", src, ok, err)
		}
	}
	_, ok, err := mustParse(t, "name: w\non: push\njobs:\n  a:\n    steps:\n      - run: x\n").DispatchInputs()
	if err != nil || ok {
		t.Errorf("a push-only workflow reported workflow_dispatch: ok=%v err=%v", ok, err)
	}
}

func TestValidateDispatch(t *testing.T) {
	declared, _, _ := mustParse(t, dispatchWF).DispatchInputs()

	got, err := ValidateDispatch(declared, map[string]string{"environment": "production"})
	if err != nil {
		t.Fatalf("ValidateDispatch: %v", err)
	}
	if got["environment"] != "production" || got["version"] != "latest" {
		t.Errorf("defaults not filled in: %+v", got)
	}
	// Strings, not Go types: that is what the forge puts in the event, and act
	// converts a boolean input with `value == "true"`. A real JSON true there
	// compares false, and a dry_run guard does the opposite of what was asked.
	if got["dry_run"] != "false" {
		t.Errorf("dry_run = %#v, want the string \"false\"", got["dry_run"])
	}
	if got["replicas"] != "2" {
		t.Errorf("replicas = %#v, want the string \"2\"", got["replicas"])
	}

	if _, err := ValidateDispatch(declared, nil); err == nil {
		t.Error("a missing required input was accepted")
	}
	if _, err := ValidateDispatch(declared, map[string]string{"environment": "prod"}); err == nil {
		t.Error("a value outside the declared choices was accepted")
	}
	// A typo'd name would otherwise be swallowed, the real input would take its
	// default, and a deploy would go to the wrong place with nothing in the log.
	if _, err := ValidateDispatch(declared, map[string]string{"environment": "staging", "enviroment": "production"}); err == nil {
		t.Error("an undeclared input was accepted")
	}
	if _, err := ValidateDispatch(declared, map[string]string{"environment": "staging", "replicas": "many"}); err == nil {
		t.Error("a non-numeric value for a number input was accepted")
	}

	// ParseBool takes "1", "TRUE" and "T"; act only recognises "true". The
	// normalisation is what keeps those from silently meaning false.
	norm, err := ValidateDispatch(declared, map[string]string{"environment": "staging", "dry_run": "TRUE"})
	if err != nil {
		t.Fatalf("ValidateDispatch: %v", err)
	}
	if norm["dry_run"] != "true" {
		t.Errorf("dry_run = %q, want the normalised \"true\"", norm["dry_run"])
	}
}
