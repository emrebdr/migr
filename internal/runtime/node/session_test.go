package node

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/runtime"
)

// The adapter composed with the thing that actually starts it.
//
// Nothing else proves Path, Args and Dir are right together: a unit test can assert each field and
// still describe a process that will not run. So this writes a project the way an install leaves one
// -- a package.json, a node_modules/@migr/sdk, and a .ts migration on disk -- and drives a real
// runtime.Session through it.
//
// It needs no database. host.schema.apply is recorded by the host rather than executed, which is why
// migr validate already runs over driver.Offline, and it is what lets the whole path be exercised
// here rather than waiting for M7-T8's PostgreSQL.
//
// Two skips, both announced, because these are the tests in the package that need the real toolchain
// and the built package: CI installs Node on both legs and make build runs before make test, so they
// run there rather than skipping. The same two conditions are a failure rather than a skip in
// conformance_test.go, where asking for the run is itself a statement that the toolchain is there.

// fixture is the one migration this project holds, whose enum is what proves esbuild ran.
const fixture = `import { defineMigration, t } from "@migr/sdk";

enum Width {
  Email = 255,
}

export default defineMigration({
  async up(s) {
    await s.createTable("users").column("email", t.string(Width.Email)).apply();
  },
});
`

// filename is what that migration is called, and the version every message names it by.
const filename = "20260727T153000_add_users.ts"

// emitted is the IR the fixture performs, as ir.Marshal spells it.
const emitted = `[{"op":"create_table","table":"users","columns":` +
	`[{"name":"email","type":"string(255)","nullable":false}]}]`

// sdkDir is this repository's own @migr/sdk, which stands in for an installed one.
var sdkDir = filepath.Join("..", "..", "..", "sdk", "typescript")

// started opens a session over one prepared project, and closes it when the test is done.
func started(t *testing.T, root string) *runtime.Session {
	t.Helper()

	adapter, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := driver.Offline("postgres")
	if err != nil {
		t.Fatalf("opening the offline session: %v", err)
	}

	session, err := runtime.Start(t.Context(), runtime.Options{
		Runtime: adapter,
		Project: project(root),
		Conn:    conn,
		Log:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("starting the node runtime: %v", err)
	}

	t.Cleanup(func() {
		if err := session.Close(t.Context()); err != nil {
			t.Errorf("closing the session: %v", err)
		}
	})

	return session
}

// requireToolchain skips this test on a machine that cannot run one, saying which half is missing.
func requireToolchain(t *testing.T) {
	t.Helper()

	if missing := missingToolchain(); missing != "" {
		t.Skip(missing)
	}
}

// missingToolchain reports which half of the real toolchain is absent, and nothing when both are there.
func missingToolchain() string {
	if _, err := exec.LookPath(program); err != nil {
		return fmt.Sprintf("no %s on PATH, which this adapter's own run needs: %v", program, err)
	}

	built := filepath.Join(sdkDir, "dist", "esm", executableName+".js")

	if _, err := os.Stat(built); err != nil {
		return fmt.Sprintf("%s is not built, so there is nothing to start: run `make build`", built)
	}

	return ""
}

// prepared writes a project with the SDK linked where an install in it would have left one.
func prepared(t *testing.T) string {
	t.Helper()

	requireToolchain(t)

	root := t.TempDir()

	linked(t, root)
	written(t, root)

	return root
}

// linked puts this repository's own package where an npm install under one directory would put it.
func linked(t *testing.T, at string) {
	t.Helper()

	scope := filepath.Join(at, "node_modules", "@migr")

	if err := os.MkdirAll(scope, 0o750); err != nil {
		t.Fatalf("making node_modules: %v", err)
	}

	resolved, err := filepath.Abs(sdkDir)
	if err != nil {
		t.Fatalf("resolving the SDK: %v", err)
	}

	if err := os.Symlink(resolved, filepath.Join(scope, "sdk")); err != nil {
		t.Fatalf("linking the SDK: %v", err)
	}
}

// written puts one project's package.json and its one migration on disk.
func written(t *testing.T, root string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Join(root, "migrations"), 0o750); err != nil {
		t.Fatalf("making the migrations directory: %v", err)
	}

	write(t, filepath.Join(root, "package.json"), `{"name":"fixture","type":"module"}`)
	write(t, filepath.Join(root, "migrations", filename), fixture)
}

func TestBothHalvesOfTheRealToolchainAreReportedByName(t *testing.T) {
	t.Log("These two sentences are a skip here and a failure under the conformance tag, so what")
	t.Log("they say is the only thing an operator has to go on in either case.")

	held := sdkDir

	t.Cleanup(func() { sdkDir = held })

	sdkDir = t.TempDir()

	if missing := missingToolchain(); !strings.Contains(missing, "make build") {
		t.Errorf("an unbuilt package is reported as %q", missing)
	}

	t.Setenv("PATH", t.TempDir())

	if missing := missingToolchain(); !strings.Contains(missing, program) {
		t.Errorf("a machine with no node is reported as %q", missing)
	}
}

func TestTheAdapterStartsARuntimeThatLoadsTheProjectsMigrations(t *testing.T) {
	session := started(t, prepared(t))

	found, err := session.Discovered(t.Context())
	if err != nil {
		t.Fatalf("Discovered: %v", err)
	}

	if len(found) != 1 {
		t.Fatalf("the runtime loaded %d migrations, want the one on disk", len(found))
	}

	if found[0].Version != "20260727T153000" || found[0].File != filename {
		t.Errorf("the runtime reported %+v", found[0])
	}
}

func TestAMigrationOnDiskEmitsItsOperationsThroughTheSession(t *testing.T) {
	session := started(t, prepared(t))

	m, err := migration.Parse(filename)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}

	performed, err := session.Emit(t.Context(), m, ir.Up, engine.ModeApply)
	if err != nil {
		t.Fatalf("Emit: %v", err)
	}

	encoded, err := ir.Marshal(performed.Operations)
	if err != nil {
		t.Fatalf("encoding what it emitted: %v", err)
	}

	t.Log("The width comes from an enum, which Node's own type stripping refuses. Reading it off")
	t.Log("the wire is what proves the SDK transpiled the file rather than merely loading it.")

	if string(encoded) != emitted {
		t.Errorf("the migration emitted %s,\nwant %s", encoded, emitted)
	}

	if len(performed.Direct) != 0 {
		t.Errorf("the migration held %d statements of its own, and it sends none",
			len(performed.Direct))
	}
}

func TestTheSDKIsResolvedFromAWorkspaceRootAboveTheProject(t *testing.T) {
	requireToolchain(t)

	top := t.TempDir()
	root := filepath.Join(top, "packages", "app")

	linked(t, top)
	written(t, root)

	t.Log("npm, pnpm and yarn all hoist a workspace's dependencies to its root, so the project")
	t.Log("migr is pointed at often has no node_modules of its own. Asking node rather than")
	t.Log("walking the tree here is what makes that work rather than something to implement.")

	found, err := started(t, root).Discovered(t.Context())
	if err != nil {
		t.Fatalf("Discovered: %v", err)
	}

	if len(found) != 1 {
		t.Errorf("the runtime loaded %d migrations from a hoisted install", len(found))
	}
}

func TestAProjectWithNoSDKIsRefusedByTheRealProbe(t *testing.T) {
	requireToolchain(t)

	root := t.TempDir()

	written(t, root)

	adapter, _ := New()

	t.Log("Every other test here answers the probe with a script. This one runs it: require.resolve")
	t.Log("throws for a package that is not installed, and the probe is written so that a project")
	t.Log("with no SDK is an empty field rather than a node that exited non-zero.")

	_, err := adapter.Command(t.Context(), project(root))
	if !errors.Is(err, ErrNoSDK) {
		t.Errorf("Command returned %v, want %v", err, ErrNoSDK)
	}
}

func TestAMigrationAuthoringNoDownRefusesTheRollbackRatherThanEmittingNothing(t *testing.T) {
	session := started(t, prepared(t))

	m, err := migration.Parse(filename)
	if err != nil {
		t.Fatalf("parsing %s: %v", filename, err)
	}

	_, err = session.Emit(t.Context(), m, ir.Down, engine.ModeApply)

	if !errors.Is(err, engine.ErrNoDownAuthored) {
		t.Errorf("Emit returned %v, want %v", err, engine.ErrNoDownAuthored)
	}
}
