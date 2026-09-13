package runner

import "testing"

func TestParseLabelSchemas(t *testing.T) {
	cases := []struct {
		raw    string
		name   string
		schema string
		arg    string
	}{
		{"self-hosted", "self-hosted", SchemaHost, ""},
		{"self-hosted:host", "self-hosted", SchemaHost, ""},
		{"ubuntu-latest:docker://node:20", "ubuntu-latest", SchemaDocker, "node:20"},
	}
	for _, c := range cases {
		got, err := ParseLabel(c.raw)
		if err != nil {
			t.Fatalf("ParseLabel(%q): %v", c.raw, err)
		}
		if got.Name != c.name || got.Schema != c.schema || got.Arg != c.arg {
			t.Errorf("ParseLabel(%q) = %+v, want %s/%s/%s", c.raw, got, c.name, c.schema, c.arg)
		}
	}
}

func TestParseLabelRejectsBadSpecsAtStartup(t *testing.T) {
	for _, raw := range []string{"", ":host", "x:docker", "x:podman"} {
		if _, err := ParseLabel(raw); err == nil {
			t.Errorf("ParseLabel(%q) accepted a spec it should refuse", raw)
		}
	}
}

func TestPickPlatformMapsRunsOnToATarget(t *testing.T) {
	ls, err := ParseLabels([]string{"self-hosted:host", "ubuntu-latest:docker://node:20"})
	if err != nil {
		t.Fatalf("ParseLabels: %v", err)
	}
	if got := ls.PickPlatform([]string{"ubuntu-latest"}); got != "node:20" {
		t.Errorf("PickPlatform(ubuntu-latest) = %q, want the image", got)
	}
	if got := ls.PickPlatform([]string{"self-hosted"}); got != HostPlatform {
		t.Errorf("PickPlatform(self-hosted) = %q, want host mode", got)
	}
	// A job whose labels we do not carry should never reach us, but falling
	// back to host beats hanging.
	if got := ls.PickPlatform([]string{"windows"}); got != HostPlatform {
		t.Errorf("PickPlatform(unknown) = %q, want the host fallback", got)
	}
}

func TestNamesDropsSchemaAndImage(t *testing.T) {
	ls, _ := ParseLabels([]string{"self-hosted:host", "ubuntu-latest:docker://node:20"})
	names := ls.Names()
	if len(names) != 2 || names[0] != "self-hosted" || names[1] != "ubuntu-latest" {
		t.Fatalf("Names() = %v, want bare names — the image is the runner's business", names)
	}
}
