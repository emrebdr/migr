package node

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/runtime"
)

// The three methods this adapter owes, and the two facts a Command is built out of.
//
// Everything here runs against a node this test wrote and an @migr/sdk this test installed, so the
// failures an operator actually meets -- no node, an old node, no SDK, an SDK that was never built --
// are all reachable on a machine that has exactly one working toolchain.

// installed writes an @migr/sdk into one project and returns the manifest a probe would resolve.
func installed(t *testing.T, root string, bin string, build bool) string {
	t.Helper()

	dir := filepath.Join(root, "node_modules", "@migr", "sdk")

	if err := os.MkdirAll(filepath.Join(dir, "dist", "esm"), 0o750); err != nil {
		t.Fatalf("installing the fake SDK: %v", err)
	}

	write(t, filepath.Join(dir, "package.json"),
		fmt.Sprintf(`{"name":"@migr/sdk","bin":%s,"engines":{"node":">=24"}}`, bin))

	if build {
		write(t, filepath.Join(dir, "dist", "esm", "migr-runtime-node.js"),
			"#!/usr/bin/env node\n")
	}

	return filepath.Join(dir, "package.json")
}

// write puts one file where a test needs it, failing the test rather than the code under it.
func write(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// project is what a command is asked for, which every field of Check requires.
func project(root string) runtime.Project {
	return runtime.Project{
		Root:     root,
		Dir:      filepath.Join(root, "migrations"),
		Language: Name,
		Database: "default",
		Driver:   "postgres",
	}
}

// shipped is the bin field @migr/sdk actually declares, in the shape npm reads it from.
const shipped = `{"migr-runtime-node":"./dist/esm/migr-runtime-node.js"}`

func TestTheAdapterReportsTheLanguageItRuns(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if adapter.Name() != Name {
		t.Errorf("the adapter reports %q, want %q", adapter.Name(), Name)
	}
}

func TestDiscoverOpensNoProcess(t *testing.T) {
	fake(t, "#!/bin/sh\necho 'this node must not be run' >&2\nexit 1\n")

	adapter, _ := New()

	t.Log("Discover answers whether this machine can run the language at all, which a report may")
	t.Log("ask without paying for a toolchain probe (ADR 0035). A node that fails when it is run")
	t.Log("is the only way to tell that apart from one that is merely there.")

	if err := adapter.Discover(t.Context()); err != nil {
		t.Errorf("Discover ran the toolchain it found: %v", err)
	}
}

func TestDiscoverSaysSoWhenThereIsNoNode(t *testing.T) {
	adapter, _ := New()

	t.Setenv("PATH", t.TempDir())

	if err := adapter.Discover(t.Context()); !errors.Is(err, ErrNoToolchain) {
		t.Errorf("Discover returned %v, want %v", err, ErrNoToolchain)
	}
}

func TestACommandIsRefusedForAProjectItCouldNotBeToldAbout(t *testing.T) {
	adapter, _ := New()

	if _, err := adapter.Command(t.Context(), runtime.Project{}); !errors.Is(err, runtime.ErrNoRoot) {
		t.Errorf("Command returned %v, want %v", err, runtime.ErrNoRoot)
	}
}

func TestACommandNamesNodeTheExecutableAndTheProjectRoot(t *testing.T) {
	root := t.TempDir()
	manifest := installed(t, root, shipped, true)
	path := fake(t, answering("24.16.0", manifest))

	adapter, _ := New()

	built, err := adapter.Command(t.Context(), project(root))
	if err != nil {
		t.Fatalf("Command: %v", err)
	}

	if built.Path != path {
		t.Errorf("the command starts %q, want the node on PATH %q", built.Path, path)
	}

	want := filepath.Join(filepath.Dir(manifest), "dist", "esm", "migr-runtime-node.js")
	if len(built.Args) != 1 || built.Args[0] != want {
		t.Log("The path comes from the package's own bin field, so the built file is named once")
		t.Log("in the package that builds it rather than twice.")
		t.Errorf("the command carries %v, want [%s]", built.Args, want)
	}

	if built.Dir != root {
		t.Errorf("the runtime would start in %q, want the project root %q (§9)", built.Dir, root)
	}

	if built.Env != nil {
		t.Errorf("the command replaces the environment with %v, and §9 gives it the engine's own",
			built.Env)
	}
}

func TestAnOldNodeIsReportedBeforeAMissingSDK(t *testing.T) {
	root := t.TempDir()
	fake(t, answering("18.20.8", ""))

	adapter, _ := New()

	t.Log("A node old enough to matter cannot resolve an exports-only package either, so the")
	t.Log("probe comes back with no manifest. Reporting that first would send an operator to")
	t.Log("npm install for a problem npm cannot fix.")

	_, err := adapter.Command(t.Context(), project(root))
	if !errors.Is(err, ErrOldToolchain) {
		t.Errorf("Command returned %v, want %v", err, ErrOldToolchain)
	}
}

func TestAProjectWithNoSDKIsToldHowToInstallOne(t *testing.T) {
	root := t.TempDir()
	fake(t, answering("24.16.0", ""))

	adapter, _ := New()

	_, err := adapter.Command(t.Context(), project(root))
	if !errors.Is(err, ErrNoSDK) {
		t.Fatalf("Command returned %v, want %v", err, ErrNoSDK)
	}

	for _, want := range []string{root, "npm install @migr/sdk"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestAnSDKThatWasNeverBuiltSaysSoRatherThanNamingAMissingFile(t *testing.T) {
	root := t.TempDir()
	manifest := installed(t, root, shipped, false)
	fake(t, answering("24.16.0", manifest))

	adapter, _ := New()

	_, err := adapter.Command(t.Context(), project(root))
	if !errors.Is(err, ErrNoExecutable) {
		t.Fatalf("Command returned %v, want %v", err, ErrNoExecutable)
	}

	for _, want := range []string{filepath.Dir(manifest), "npm run build"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}

func TestAPackageDeclaringNoRuntimeIsRefusedAsThat(t *testing.T) {
	t.Log("Every file these manifests name is written, so the refusal has to come from the name")
	t.Log("this adapter asked for rather than from a stat that happened to fail.")

	for _, bin := range []string{`{}`, `{"something-else":"./x.js"}`, `"./single.js"`, `null`} {
		root := t.TempDir()
		manifest := installed(t, root, bin, true)

		write(t, filepath.Join(filepath.Dir(manifest), "x.js"), "")
		write(t, filepath.Join(filepath.Dir(manifest), "single.js"), "")

		if _, err := executableIn(manifest); !errors.Is(err, ErrNoExecutable) {
			t.Errorf("a bin of %s returned %v, want %v", bin, err, ErrNoExecutable)
		}
	}
}

func TestAManifestThatCannotBeReadIsReportedWithItsPath(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "node_modules", "@migr", "sdk", "package.json")

	_, err := executableIn(missing)
	if err == nil {
		t.Fatal("a package.json that is not there was read anyway")
	}

	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the failure does not name the file it could not read: %v", err)
	}
}

func TestAManifestThatIsNotJSONIsReportedWithItsPath(t *testing.T) {
	root := t.TempDir()
	manifest := installed(t, root, shipped, true)

	write(t, manifest, "{ not json")

	_, err := executableIn(manifest)
	if err == nil {
		t.Fatal("a package.json that is not JSON was read as one")
	}

	if !strings.Contains(err.Error(), manifest) {
		t.Errorf("the failure does not name the file it could not read: %v", err)
	}
}

func TestTheVersionFloorIsWhatTheSDKItselfRequires(t *testing.T) {
	held, err := manifestAt(filepath.Join("..", "..", "..", "sdk", "typescript", "package.json"))
	if err != nil {
		t.Fatalf("reading the SDK's manifest: %v", err)
	}

	want := fmt.Sprintf(">=%d", MinimumNode)

	if held.Engines.Node != want {
		t.Log("Two statements of one requirement: @migr/sdk's engines.node and MinimumNode here.")
		t.Log("Whichever of them moved, the other has to move with it -- or this check has to")
		t.Log("become a range parser, which is a decision rather than an edit.")
		t.Errorf("@migr/sdk requires node %q and this adapter enforces %q",
			held.Engines.Node, want)
	}
}

func TestTheSDKStatesWhereItsOwnRuntimeIs(t *testing.T) {
	sdk := filepath.Join("..", "..", "..", "sdk", "typescript")

	held, err := manifestAt(filepath.Join(sdk, "package.json"))
	if err != nil {
		t.Fatalf("reading the SDK's manifest: %v", err)
	}

	if held.executable() == "" {
		t.Log("This adapter reads the path out of the package rather than carrying a copy of it,")
		t.Log("so a package that stops declaring one fails here rather than at a user's migr up.")
		t.Errorf("@migr/sdk declares no %s in its bin field", executableName)
	}
}
