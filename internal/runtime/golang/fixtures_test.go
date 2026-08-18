package golang

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/runtime"
)

// marker is written over a built binary, so a rebuild is observable as the marker being gone.
const marker = "this is not a binary"

// fixture copies the fixture module and the stub SDK it replaces onto a fresh temporary tree.
func fixture(t *testing.T) runtime.Project {
	t.Helper()

	root := t.TempDir()
	copyTree(t, filepath.Join("testdata", "project"), filepath.Join(root, "project"))
	copyTree(t, filepath.Join("testdata", "sdk"), filepath.Join(root, "sdk"))

	return projectAt(filepath.Join(root, "project"))
}

// projectAt describes one copied fixture the way a session would describe it to an adapter.
func projectAt(root string) runtime.Project {
	return runtime.Project{
		Root:     root,
		Dir:      filepath.Join(root, "migrations"),
		Language: Name,
		Database: "default",
		Driver:   "postgres",
	}
}

// copyTree copies one testdata directory, since a build writes into the module it builds.
func copyTree(t *testing.T, from, to string) {
	t.Helper()

	entries, err := os.ReadDir(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}

	if err := os.MkdirAll(to, 0o750); err != nil {
		t.Fatalf("making %s: %v", to, err)
	}

	for _, entry := range entries {
		source, target := filepath.Join(from, entry.Name()), filepath.Join(to, entry.Name())

		if entry.IsDir() {
			copyTree(t, source, target)

			continue
		}

		copyFile(t, source, target)
	}
}

// copyFile copies one fixture file, keeping it writable so a test can edit what it copied.
func copyFile(t *testing.T, from, to string) {
	t.Helper()

	content, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("reading %s: %v", from, err)
	}

	if err := os.WriteFile(to, content, 0o600); err != nil {
		t.Fatalf("writing %s: %v", to, err)
	}
}

// commandFor asks the adapter for one project's runtime command, failing the test when it refuses.
func commandFor(t *testing.T, p runtime.Project) runtime.Command {
	t.Helper()

	command, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	built, err := command.Command(t.Context(), p)
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	return built
}

// runBinary executes what the adapter built and returns the one line the stub runner prints.
func runBinary(t *testing.T, built runtime.Command) string {
	t.Helper()

	cmd := exec.CommandContext(t.Context(), built.Path)
	cmd.Dir = built.Dir

	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("running %s: %v", built.Path, err)
	}

	return strings.TrimSpace(string(out))
}

// spoil overwrites a built binary, so the next Command reveals whether it rebuilt or reused.
func spoil(t *testing.T, built runtime.Command) {
	t.Helper()

	if err := os.WriteFile(built.Path, []byte(marker), 0o600); err != nil {
		t.Fatalf("spoiling %s: %v", built.Path, err)
	}
}

// spoiled reports whether the binary is still the marker, which means nothing rebuilt it.
func spoiled(t *testing.T, built runtime.Command) bool {
	t.Helper()

	content, err := os.ReadFile(built.Path)
	if err != nil {
		t.Fatalf("reading %s: %v", built.Path, err)
	}

	return string(content) == marker
}

// edit rewrites one file inside a copied fixture, which is how a test changes an input.
func edit(t *testing.T, p runtime.Project, relative, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(p.Root, relative), []byte(content), 0o600); err != nil {
		t.Fatalf("editing %s: %v", relative, err)
	}
}

// migrationSource is a second fixture migration, for the case where a file is added rather than edited.
const migrationSource = `package migrations

import "github.com/emrebdr/migr/sdk/go/runner"

func init() { runner.Register("20260727T153100") }
`
