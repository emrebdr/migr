package golang

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/emrebdr/migr/internal/runtime"
	conformance "github.com/emrebdr/migr/test/conformance/runtime"
)

// manifest is the module the conformance fixtures are built inside, written rather than stored.
const manifest = `module example.com/conformance

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => `

func TestTheGoSDKPassesTheRuntimeConformanceSuite(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conformance.Suite{Runtime: adapter, Project: conformanceProject(t)}.Run(t)

	t.Log("This is the first time anything reads what a migration emits off the wire from a real")
	t.Log("source: the runner is generated, built, and started, and every operation it sends is")
	t.Log("compared byte for byte against a file no language owns.")
}

// conformanceProject assembles the fixture sources into a module this repository's SDK can build.
func conformanceProject(t *testing.T) runtime.Project {
	t.Helper()

	root := filepath.Join(t.TempDir(), "conformance")
	dir := filepath.Join(root, "migrations")

	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("making the migrations directory: %v", err)
	}

	writeModule(t, root)
	writeSources(t, dir)

	return runtime.Project{
		Root: root, Dir: dir, Language: Name, Database: "default", Driver: "postgres",
	}
}

// writeModule writes the go.mod, which is a Go fact and so is not kept in the neutral fixture tree.
func writeModule(t *testing.T, root string) {
	t.Helper()

	sdk, err := filepath.Abs(filepath.Join("..", "..", "..", "sdk", "go"))
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte(manifest+sdk+"\n"), 0o600); err != nil {
		t.Fatalf("writing the fixture module: %v", err)
	}
}

// writeSources copies every fixture's Go expression into the module's migrations directory.
func writeSources(t *testing.T, dir string) {
	t.Helper()

	sources, err := conformance.Sources(Name)
	if err != nil {
		t.Fatalf("opening the %s fixtures: %v", Name, err)
	}

	entries, err := fs.ReadDir(sources, ".")
	if err != nil {
		t.Fatalf("reading the %s fixtures: %v", Name, err)
	}

	if len(entries) == 0 {
		t.Fatalf("the %s fixtures are empty, so this run would prove nothing", Name)
	}

	for _, entry := range entries {
		content, err := fs.ReadFile(sources, entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}

		if err := os.WriteFile(filepath.Join(dir, entry.Name()), content, 0o600); err != nil {
			t.Fatalf("writing %s: %v", entry.Name(), err)
		}
	}
}
