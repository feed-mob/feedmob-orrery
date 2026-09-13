package workflow

import (
	"fmt"
	"strconv"
	"strings"
)

// DispatchInput is one `workflow_dispatch` input.
type DispatchInput struct {
	Description string
	Required    bool
	Default     string
	// Type is one of string, boolean, number, choice, environment. GitHub
	// defaults it to string.
	Type    string
	Options []string
}

// DispatchInputs reads `on.workflow_dispatch.inputs`.
//
// Declaring the inputs is what makes a manual run a form rather than a shell:
// the operator is offered named, typed parameters, and anything else is
// refused before a job starts.
func (wf *Workflow) DispatchInputs() (map[string]DispatchInput, bool, error) {
	m, ok := wf.On.(map[string]any)
	if !ok {
		// `on: workflow_dispatch` or `on: [push, workflow_dispatch]`: declared,
		// with no inputs.
		triggers, err := wf.Triggers()
		if err != nil {
			return nil, false, err
		}
		for _, t := range triggers {
			if t.Event == "workflow_dispatch" {
				return nil, true, nil
			}
		}
		return nil, false, nil
	}
	raw, declared := m["workflow_dispatch"]
	if !declared {
		return nil, false, nil
	}
	spec, ok := raw.(map[string]any)
	if !ok {
		return nil, true, nil
	}
	inputs, ok := spec["inputs"].(map[string]any)
	if !ok {
		return nil, true, nil
	}

	out := make(map[string]DispatchInput, len(inputs))
	for name, v := range inputs {
		def, ok := v.(map[string]any)
		if !ok {
			return nil, true, fmt.Errorf("workflow_dispatch input %q is not a mapping", name)
		}
		in := DispatchInput{Type: "string"}
		if s, ok := def["description"].(string); ok {
			in.Description = s
		}
		if b, ok := def["required"].(bool); ok {
			in.Required = b
		}
		if t, ok := def["type"].(string); ok {
			in.Type = t
		}
		in.Default = scalar(def["default"])
		in.Options = stringsOf(def["options"])
		out[name] = in
	}
	return out, true, nil
}

// ValidateDispatch checks what an operator supplied against what the workflow
// declared, and fills in the defaults.
//
// Refusing an unknown input matters more than it looks: a typo'd `enviroment`
// would otherwise be accepted, the real `environment` would take its default,
// and a deploy would go to the wrong place with nothing in the log to say why.
func ValidateDispatch(declared map[string]DispatchInput, given map[string]string) (map[string]any, error) {
	out := map[string]any{}
	for name := range given {
		if _, ok := declared[name]; !ok {
			return nil, fmt.Errorf("input %q is not declared by this workflow", name)
		}
	}
	for name, in := range declared {
		v, supplied := given[name]
		if !supplied {
			if in.Required {
				return nil, fmt.Errorf("input %q is required", name)
			}
			v = in.Default
		}
		switch in.Type {
		case "boolean":
			b, err := strconv.ParseBool(orFalse(v))
			if err != nil {
				return nil, fmt.Errorf("input %q must be a boolean, got %q", name, v)
			}
			out[name] = b
		case "number":
			if v == "" {
				out[name] = ""
				continue
			}
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return nil, fmt.Errorf("input %q must be a number, got %q", name, v)
			}
			out[name] = n
		case "choice":
			if len(in.Options) > 0 && !contains(in.Options, v) {
				return nil, fmt.Errorf("input %q must be one of %s, got %q",
					name, strings.Join(in.Options, ", "), v)
			}
			out[name] = v
		default:
			out[name] = v
		}
	}
	return out, nil
}

func scalar(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case bool:
		return strconv.FormatBool(x)
	case int:
		return strconv.Itoa(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func orFalse(v string) string {
	if v == "" {
		return "false"
	}
	return v
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
