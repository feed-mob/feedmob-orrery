package server

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The dashboard is JavaScript compiled into a Go binary, so nothing in the Go
// build ever looks at it. A missing parenthesis shipped exactly that way: the
// binary built, the asset served, and the page was blank.
//
// Skipped when node is absent rather than failing — a machine without node can
// still build and run the server.
func TestWebAssetsParse(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping the dashboard syntax check")
	}
	dir := t.TempDir()
	var checked int
	err = fs.WalkDir(webFS, "web", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".js") {
			return err
		}
		src, err := webFS.ReadFile(path)
		if err != nil {
			return err
		}
		// node --check only treats a file as a module when it ends in .mjs.
		out := filepath.Join(dir, strings.TrimSuffix(filepath.Base(path), ".js")+".mjs")
		if err := os.WriteFile(out, src, 0o600); err != nil {
			return err
		}
		if res, err := exec.Command(node, "--check", out).CombinedOutput(); err != nil {
			t.Errorf("%s does not parse:\n%s", path, res)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if checked == 0 {
		t.Fatal("no javascript was checked; the embed is probably empty")
	}
}

// The helpers have their own tests under web/, run by `node --test`. This makes
// `go test ./...` run them too, so the front end cannot rot unnoticed just
// because it is not Go.
func TestWebUnitTests(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping the dashboard unit tests")
	}
	cmd := exec.Command(node, "--test", "web/")
	cmd.Dir = "."
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("node --test web/ failed:\n%s", out)
	}
	if !strings.Contains(string(out), "# fail 0") {
		t.Fatalf("dashboard tests reported failures:\n%s", out)
	}
}
