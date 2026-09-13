package runner

import (
	"fmt"
	"strings"
)

// Label is one capability a runner advertises, in Gitea's
// `label[:schema[:args]]` form:
//
//	ubuntu-latest:docker://node:20-bookworm   run jobs in that image
//	self-hosted:host                          run jobs directly on this machine
//	self-hosted                               shorthand for :host
//
// Keeping the schema on the label is what lets one runner serve both container
// and host jobs without the server having to know anything about images.
type Label struct {
	Name   string
	Schema string
	Arg    string
}

// Label schemas.
const (
	SchemaDocker = "docker"
	SchemaHost   = "host"
)

// ParseLabel reads one `name[:schema[:arg]]` spec.
func ParseLabel(raw string) (*Label, error) {
	parts := strings.SplitN(raw, ":", 3)
	l := &Label{Name: parts[0]}
	switch len(parts) {
	case 1:
		l.Schema = SchemaHost
	case 2:
		l.Schema = parts[1]
	case 3:
		// docker://image — strip the scheme separator here so Arg always holds
		// a usable image reference rather than something callers must clean.
		l.Schema, l.Arg = parts[1], strings.TrimPrefix(parts[2], "//")
	}
	if l.Name == "" {
		return nil, fmt.Errorf("label %q has no name", raw)
	}
	switch l.Schema {
	case SchemaDocker:
		if l.Arg == "" {
			return nil, fmt.Errorf("label %q uses the docker schema but names no image", raw)
		}
	case SchemaHost:
	default:
		return nil, fmt.Errorf("label %q uses unknown schema %q (want docker or host)", raw, l.Schema)
	}
	return l, nil
}

// Labels is a runner's full set.
type Labels []*Label

// ParseLabels reads a whole set, failing on the first bad spec so a
// misconfigured runner is rejected at startup rather than at dispatch time.
func ParseLabels(raw []string) (Labels, error) {
	out := make(Labels, 0, len(raw))
	for _, r := range raw {
		l, err := ParseLabel(r)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, nil
}

// Names is what the runner advertises to the server; the schema and image stay
// local, because matching `runs-on` is the server's job and choosing how to run
// is ours.
func (ls Labels) Names() []string {
	out := make([]string, 0, len(ls))
	for _, l := range ls {
		out = append(out, l.Name)
	}
	return out
}

// PickPlatform maps a job's runs-on to an execution target: a container image,
// or the sentinel "-self-hosted" meaning run directly on this machine.
func (ls Labels) PickPlatform(runsOn []string) string {
	platforms := make(map[string]string, len(ls))
	for _, l := range ls {
		switch l.Schema {
		case SchemaDocker:
			platforms[l.Name] = l.Arg
		case SchemaHost:
			platforms[l.Name] = HostPlatform
		}
	}
	for _, r := range runsOn {
		if p, ok := platforms[r]; ok {
			return p
		}
	}
	// A job asking for labels we do not carry should never have reached us —
	// the server matches before dispatch. Falling back to host keeps a
	// mismatch from hanging the job.
	return HostPlatform
}

// HostPlatform is act's sentinel for "no container, run on the host".
const HostPlatform = "-self-hosted"
