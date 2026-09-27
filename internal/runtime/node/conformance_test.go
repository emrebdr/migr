//go:build conformance

package node

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	conformance "github.com/emrebdr/migr/test/conformance/runtime"
)

// The TypeScript SDK held to the fixtures no language owns.
//
// This is the run behind M7-T7's claim, and it lives here rather than in the suite's own package
// because depguard permits only cmd/migr to name a concrete runtime -- the same reason
// internal/runtime/golang carries Go's run.
//
// Neither SDK is compared to the other. Both are compared to the manifests, whose own bytes are
// already held to being exactly what ir.Marshal emits, so a fixture edited to suit one language
// fails the other.
//
// It is behind the conformance tag because Node is a toolchain make test may not have, and under
// that tag a missing node or an unbuilt dist is a failure rather than a skip: asking for this run is
// a statement that the toolchain is present, and a green run that proved nothing is worse than a red
// one. The package's untagged tests keep their announced skips.

func TestTheTypeScriptSDKPassesTheRuntimeConformanceSuite(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conformance.Suite{Runtime: adapter, Project: project(conformanceProject(t))}.Run(t)

	t.Log("Two languages, one set of fixtures, byte for byte: this is the mechanical proof of the")
	t.Log("claim that a migration's meaning is independent of the language it was written in.")
}

func TestAMachineThatCannotRunTheSuiteFailsRatherThanSkipping(t *testing.T) {
	if os.Getenv(reexec) != "" {
		t.Skip("this is the child, which must not recurse")
	}

	run := exec.Command(os.Args[0], "-test.run=^"+suiteTest+"$", "-test.v")
	run.Env = append(os.Environ(), "PATH="+t.TempDir(), reexec+"=1")

	said, err := run.CombinedOutput()

	t.Log("Whether the suite fails or skips is only observable on a machine that cannot run it, so")
	t.Log("this test makes one: the same binary, re-executed with nothing on PATH.")

	if err == nil {
		t.Errorf("the suite passed with no node on PATH:\n%s", said)
	}

	if !strings.Contains(string(said), "this suite cannot run") {
		t.Errorf("the failure does not say why:\n%s", said)
	}

	if strings.Contains(string(said), "SKIP") {
		t.Errorf("the suite skipped, and asking for this run is a statement that node is there:\n%s",
			said)
	}
}

// reexec names the variable the child reads, so the re-executed binary runs the suite and not this.
const reexec = "MIGR_CONFORMANCE_CHILD"

// suiteTest names the one test the child is asked for, which is the run this policy governs.
const suiteTest = "TestTheTypeScriptSDKPassesTheRuntimeConformanceSuite"

// conformanceProject writes the fixture set into a project this repository's own SDK can run.
func conformanceProject(t *testing.T) string {
	t.Helper()

	demandToolchain(t)

	root := t.TempDir()

	linked(t, root)

	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o750); err != nil {
		t.Fatalf("making the migrations directory: %v", err)
	}

	write(t, filepath.Join(root, "package.json"), `{"name":"conformance","type":"module"}`)
	writeSources(t, filepath.Join(root, "migrations"))

	return root
}

// demandToolchain fails rather than skips, since running this suite at all is asking for the toolchain.
func demandToolchain(t *testing.T) {
	t.Helper()

	if missing := missingToolchain(); missing != "" {
		t.Fatalf("this suite cannot run: %s", missing)
	}
}

// writeSources copies every fixture's TypeScript expression into the project's migrations directory.
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

		write(t, filepath.Join(dir, entry.Name()), string(content))
	}
}
