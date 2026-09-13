package workflow

import "fmt"

// Environment is `environment:` on a job — GitHub's key, with one addition.
//
// Declaring it is what turns an ordinary job into a deployment: the run is
// recorded against that environment, so "which version is on production right
// now" becomes a question with an answer. GitHub records the same thing and
// then gives you almost nothing to do with it; the ledger here is what rollback
// reads.
type Environment struct {
	Name string `yaml:"name"`
	// URL is where the deployed thing lives, shown next to the deployment.
	URL string `yaml:"url"`
	// AutoRollback is Orrery's addition: when this job fails, redeploy the last
	// version that worked. Off by default — an automatic rollback is a second
	// deployment nobody asked for, and on a first-ever deploy there is nothing
	// to go back to.
	AutoRollback bool `yaml:"auto-rollback"`
	// VersionFrom names the job output that carries what was actually deployed
	// — an image tag, usually. Without it the commit sha stands in, which is
	// right for code and wrong for anything built and tagged separately.
	VersionFrom string `yaml:"version-from"`
}

// UnmarshalYAML accepts both shapes GitHub does: a bare name, or a mapping.
func (e *Environment) UnmarshalYAML(unmarshal func(any) error) error {
	var name string
	if err := unmarshal(&name); err == nil {
		e.Name = name
		return nil
	}
	type raw Environment
	var r raw
	if err := unmarshal(&r); err != nil {
		return fmt.Errorf("environment: must be a name or a mapping: %w", err)
	}
	*e = Environment(r)
	if e.Name == "" {
		return fmt.Errorf("environment: needs a name")
	}
	return nil
}

// DefaultVersionOutputs are the job outputs checked for a deployed version when
// the workflow does not say which one to read. These are the names people
// already use.
var DefaultVersionOutputs = []string{"version", "image", "image_tag", "tag"}

// Version picks what was deployed out of a job's outputs, falling back to the
// commit. An image tag is the thing an operator recognises and the thing a
// rollback has to name; the sha only identifies the source.
func (e *Environment) Version(outputs map[string]string, sha string) string {
	if e != nil && e.VersionFrom != "" {
		if v := outputs[e.VersionFrom]; v != "" {
			return v
		}
	}
	for _, key := range DefaultVersionOutputs {
		if v := outputs[key]; v != "" {
			return v
		}
	}
	return sha
}
