// Package workflow parses the GitHub Actions workflow syntax.
//
// Parsing here is deliberately shallow: it covers what the scheduler needs to
// build the job graph — jobs, needs, runs-on, timeout-minutes, env — and hands
// the rest to act, which owns expression evaluation and step execution. Two
// parsers disagreeing about the same file is worse than one doing less.
package workflow

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultTimeoutMinutes is applied to any job that does not set its own.
//
// GitHub Actions has no org-level default and its built-in ceiling is 360
// minutes, which is why twelve of our thirteen workflows shipped without a
// timeout: forgetting costs nothing until it costs six hours of runner time.
// Orrery inverts that — the author may lower this, never omit it.
const DefaultTimeoutMinutes = 60

// Workflow is a parsed workflow file.
type Workflow struct {
	Name string            `yaml:"name"`
	On   any               `yaml:"on"`
	Env  map[string]string `yaml:"env"`
	Jobs map[string]*Job   `yaml:"jobs"`

	// JobOrder preserves the order jobs appear in the file, which YAML maps
	// lose. Dispatch does not depend on it, but stable ordering makes runs
	// reproducible to read.
	JobOrder []string `yaml:"-"`
}

// Job is one job in a workflow.
type Job struct {
	Name           string            `yaml:"name"`
	Needs          StringList        `yaml:"needs"`
	RunsOn         StringList        `yaml:"runs-on"`
	TimeoutMinutes int               `yaml:"timeout-minutes"`
	Env            map[string]string `yaml:"env"`
	If             string            `yaml:"if"`
	Steps          []Step            `yaml:"steps"`
	Outputs        map[string]string `yaml:"outputs"`
}

// Step is one step of a job.
type Step struct {
	ID               string            `yaml:"id"`
	Name             string            `yaml:"name"`
	Uses             string            `yaml:"uses"`
	Run              string            `yaml:"run"`
	Shell            string            `yaml:"shell"`
	WorkingDirectory string            `yaml:"working-directory"`
	Env              map[string]string `yaml:"env"`
	If               string            `yaml:"if"`
	ContinueOnError  bool              `yaml:"continue-on-error"`
	With             map[string]any    `yaml:"with"`
}

// Label returns a human-readable name for the step.
func (s Step) Label(index int) string {
	switch {
	case s.Name != "":
		return s.Name
	case s.Uses != "":
		return s.Uses
	case s.Run != "":
		first := strings.SplitN(strings.TrimSpace(s.Run), "\n", 2)[0]
		if len(first) > 60 {
			first = first[:57] + "..."
		}
		return first
	default:
		return fmt.Sprintf("step %d", index+1)
	}
}

// StringList accepts either a bare scalar or a sequence, matching how GitHub
// lets you write `needs: build` or `needs: [build, test]`.
type StringList []string

func (l *StringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var s string
		if err := value.Decode(&s); err != nil {
			return err
		}
		if s != "" {
			*l = StringList{s}
		}
		return nil
	case yaml.SequenceNode:
		var ss []string
		if err := value.Decode(&ss); err != nil {
			return err
		}
		*l = StringList(ss)
		return nil
	case yaml.MappingNode:
		// runs-on: { group: ..., labels: [...] }
		var m struct {
			Labels StringList `yaml:"labels"`
		}
		if err := value.Decode(&m); err != nil {
			return err
		}
		*l = m.Labels
		return nil
	default:
		return nil
	}
}

// Parse reads a workflow file.
func Parse(data []byte) (*Workflow, error) {
	var wf Workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, fmt.Errorf("parse workflow: %w", err)
	}
	if len(wf.Jobs) == 0 {
		return nil, fmt.Errorf("workflow has no jobs")
	}
	wf.JobOrder = jobOrder(data, wf.Jobs)
	for key, job := range wf.Jobs {
		if job == nil {
			return nil, fmt.Errorf("job %q is empty", key)
		}
		if job.Name == "" {
			job.Name = key
		}
		if job.TimeoutMinutes <= 0 {
			job.TimeoutMinutes = DefaultTimeoutMinutes
		}
	}
	if err := wf.validate(); err != nil {
		return nil, err
	}
	return &wf, nil
}

// jobOrder recovers the source order of the `jobs:` mapping keys.
func jobOrder(data []byte, jobs map[string]*Job) []string {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err == nil && len(doc.Content) > 0 {
		root := doc.Content[0]
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value != "jobs" {
				continue
			}
			m := root.Content[i+1]
			var order []string
			for j := 0; j+1 < len(m.Content); j += 2 {
				order = append(order, m.Content[j].Value)
			}
			return order
		}
	}
	keys := make([]string, 0, len(jobs))
	for k := range jobs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// validate rejects workflows the scheduler cannot run: unknown `needs` targets
// and dependency cycles. Catching these at submit time means a broken graph
// never occupies a runner.
func (wf *Workflow) validate() error {
	for key, job := range wf.Jobs {
		for _, need := range job.Needs {
			if _, ok := wf.Jobs[need]; !ok {
				return fmt.Errorf("job %q needs unknown job %q", key, need)
			}
		}
	}
	// Depth-first cycle detection over the needs graph.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(wf.Jobs))
	var stack []string
	var visit func(string) error
	visit = func(key string) error {
		switch color[key] {
		case grey:
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(stack, key), " -> "))
		case black:
			return nil
		}
		color[key] = grey
		stack = append(stack, key)
		for _, need := range wf.Jobs[key].Needs {
			if err := visit(need); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		color[key] = black
		return nil
	}
	for _, key := range wf.JobOrder {
		if err := visit(key); err != nil {
			return err
		}
	}
	return nil
}

// UsedActions lists every `uses:` reference in the workflow.
//
// Nothing rejects these any more — act resolves and runs them — but the server
// records them so an operator can see at a glance which third-party code a run
// pulled in, which is the input to pinning them by SHA behind a mirror.
func (wf *Workflow) UsedActions() []string {
	seen := map[string]bool{}
	var out []string
	for _, key := range wf.JobOrder {
		for _, st := range wf.Jobs[key].Steps {
			if st.Uses == "" || seen[st.Uses] {
				continue
			}
			seen[st.Uses] = true
			out = append(out, st.Uses)
		}
	}
	return out
}

// JobPayload renders the workflow down to a single job, as a workflow file act
// can read directly.
//
// The trimming happens on the YAML node tree rather than by re-serialising our
// parsed structs: this parser models only what the scheduler needs, so
// round-tripping through it would silently drop `strategy`, `container`,
// `services`, `defaults` and anything else act understands but we do not. The
// runner must receive the author's YAML, not our summary of it.
//
// `needs:` survives the trim even though the upstream jobs do not. It is how
// the executor knows which upstreams to graft back in from the task's own needs
// data, which is where `needs.<job>.outputs` comes from — dropping the key here
// would leave those expressions silently unresolvable, and a deploy job would
// get an empty image tag rather than an error.
func (wf *Workflow) JobPayload(source []byte, key string) ([]byte, error) {
	if _, ok := wf.Jobs[key]; !ok {
		return nil, fmt.Errorf("no such job %q", key)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(source, &doc); err != nil {
		return nil, fmt.Errorf("reparse workflow: %w", err)
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("workflow is not a mapping")
	}
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "jobs" {
			continue
		}
		jobs := root.Content[i+1]
		trimmed := &yaml.Node{Kind: yaml.MappingNode, Tag: jobs.Tag}
		for j := 0; j+1 < len(jobs.Content); j += 2 {
			if jobs.Content[j].Value != key {
				continue
			}
			trimmed.Content = append(trimmed.Content, jobs.Content[j], jobs.Content[j+1])
		}
		if len(trimmed.Content) == 0 {
			return nil, fmt.Errorf("job %q vanished while trimming", key)
		}
		root.Content[i+1] = trimmed
		return yaml.Marshal(&doc)
	}
	return nil, fmt.Errorf("workflow has no jobs mapping")
}
