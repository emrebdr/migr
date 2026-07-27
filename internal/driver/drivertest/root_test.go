package drivertest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestTheRootIsTheModuleNotTheNestedSDK(t *testing.T) {
	repo, err := repositoryRoot(mustGetwd(t))
	if err != nil {
		t.Fatalf("locating the repository root: %v", err)
	}

	nested := filepath.Join(repo, "sdk", "go")
	if _, err := os.Stat(filepath.Join(nested, "go.mod")); err != nil {
		t.Skipf("no nested module to walk out of: %v", err)
	}

	got, err := repositoryRoot(nested)
	if err != nil {
		t.Fatalf("walking up from the nested module: %v", err)
	}

	if got != repo {
		t.Errorf("walking up from %s reached %s, want %s", nested, got, repo)
		t.Log("This repository has two modules. Stopping at the first go.mod would put the")
		t.Log("golden root inside sdk/go, where it would be created on the first -update and")
		t.Log("never noticed, because a fresh directory reports every operation as missing.")
	}
}

func TestAWorkingDirectoryOutsideTheRepositoryIsAnError(t *testing.T) {
	if _, err := repositoryRoot(t.TempDir()); !errors.Is(err, ErrNoRepository) {
		t.Errorf("walking up from a temporary directory returned %v, want ErrNoRepository", err)
		t.Log("The walk stops when a directory is its own parent. Without that it loops at the")
		t.Log("filesystem root instead of failing.")
	}
}

func TestASuiteWithNoRootUsesTheCommittedTree(t *testing.T) {
	s := Suite{Driver: "no-such-driver", Compiler: &fakeCompiler{}}

	root, err := s.root()
	if err != nil {
		t.Fatalf("resolving the default root: %v", err)
	}

	want, err := defaultRoot()
	if err != nil {
		t.Fatalf("locating the golden root: %v", err)
	}

	if root != want {
		t.Errorf("Suite.root() = %q, want %q", root, want)
		t.Log("Leaving Root empty is how every real driver's suite is written, so this one line")
		t.Log("is the path that ships; the temporary roots every other test uses never reach it.")
	}

	t.Log("This deliberately stops at resolving the root and never calls check against it. An")
	t.Log("earlier version compiled the whole operation set here, and when a mutation broke the")
	t.Log("write guard the fake wrote twenty golden files into the repository. A test that can")
	t.Log("damage the tree it is checking is not worth the coverage.")
}

func TestTheGoldenRootHoldsOneDirectoryPerDriver(t *testing.T) {
	root, err := defaultRoot()
	if err != nil {
		t.Fatalf("locating the golden root: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("reading the golden root: %v", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		t.Errorf("%s sits directly in the golden root, outside any driver's directory", entry.Name())
	}

	if len(ir.Kinds()) == 0 {
		t.Error("there are no operations to record")
	}
}
