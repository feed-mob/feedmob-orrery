package workflow

import "fmt"

// Concurrency is `concurrency:` on a workflow.
//
// GitHub has this and defaults it off, which is why twelve of our thirteen
// workflows can have two pushes deploying at the same time and nobody noticed.
// Orrery keeps the syntax and changes the default; see the server's
// -default-concurrency.
type Concurrency struct {
	// Group is the key runs serialise on. It is an expression, usually
	// something like `${{ github.workflow }}-${{ github.ref }}`, and is
	// interpolated by the scheduler before it means anything.
	Group string
	// CancelInProgress throws away the run that is already going. False means
	// the new run waits, which loses no work and is the safer default for
	// anything that touches a server.
	CancelInProgress bool
}

// Concurrency reads the workflow-level `concurrency:` key in both shapes:
// a bare string is the group, a mapping carries cancel-in-progress too.
func (wf *Workflow) Concurrency() (*Concurrency, error) {
	switch v := wf.RawConcurrency.(type) {
	case nil:
		return nil, nil
	case string:
		return &Concurrency{Group: v}, nil
	case map[string]any:
		c := &Concurrency{}
		if g, ok := v["group"].(string); ok {
			c.Group = g
		}
		switch cip := v["cancel-in-progress"].(type) {
		case bool:
			c.CancelInProgress = cip
		case string:
			// An expression here is legal on GitHub. We do not evaluate it, and
			// refusing is better than guessing: guessing false would silently
			// queue what the author wanted cancelled.
			return nil, fmt.Errorf("concurrency.cancel-in-progress must be a boolean; expressions are not supported yet (got %q)", cip)
		}
		if c.Group == "" {
			return nil, fmt.Errorf("concurrency: needs a group")
		}
		return c, nil
	default:
		return nil, fmt.Errorf("concurrency: must be a string or a mapping, got %T", v)
	}
}
