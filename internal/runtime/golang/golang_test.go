package golang

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/runtime"
)

func TestTheGeneratedRunnerLoadsTheProjectsMigrationsAndRuns(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	if loaded := discovered(t, built); !slices.Equal(loaded, []string{"20260727T153000"}) {
		t.Log("Nothing else proves the blank import linked the user's package into the binary:")
		t.Log("a runner that compiled but loaded nothing would answer with an empty list and pass.")
		t.Fatalf("the built runner loaded %v, want the migration the fixture registers", loaded)
	}

	if built.Dir != p.Root {
		t.Errorf("the runtime would start in %q, want the project root %q (§9)", built.Dir, p.Root)
	}

	if len(built.Args) != 0 {
		t.Errorf("the command carries %v; §9 says a runtime must not depend on argv", built.Args)
	}
}

func TestASecondCommandReusesTheBuiltRunner(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	spoil(t, built)

	if again := commandFor(t, p); !spoiled(t, again) {
		t.Fatal("the runner was rebuilt when nothing had changed")
	}
}

func TestTwoDirectoriesInOneModuleNeverRebuildEachOther(t *testing.T) {
	p := fixture(t)
	seeds := seedsBeside(t, p)

	migrations := commandFor(t, p)
	spoil(t, migrations)

	seeded := commandFor(t, seeds)
	if seeded.Path == migrations.Path {
		t.Fatalf("both directories build into %s, so each command replaces the other's runner",
			seeded.Path)
	}

	if loaded := discovered(t, seeded); !slices.Equal(loaded, []string{"20260801T000000"}) {
		t.Errorf("the seeds runner loaded %v, want the one seed", loaded)
	}

	if again := commandFor(t, p); !spoiled(t, again) {
		t.Error("building the seeds runner made the migrations runner rebuild")
		t.Log("A seeds directory and a migrations directory are two Go packages in one module, so")
		t.Log("each needs a runner of its own -- one shared directory turns every alternate")
		t.Log("migr up and migr seed into a full rebuild, and two concurrent ones into a race.")
	}
}

func TestEditingAMigrationRebuilds(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	spoil(t, built)
	edit(t, p, filepath.Join("migrations", "20260727T153000_add_users.go"), editedUsers)

	if again := commandFor(t, p); spoiled(t, again) {
		t.Fatal("a migration changed and the cached runner was handed back anyway")
	}

	if loaded := discovered(t, commandFor(t, p)); !slices.Equal(loaded, []string{"20260727T153000"}) {
		t.Errorf("the rebuilt runner loaded %v", loaded)
	}
}

func TestAddingAMigrationRebuilds(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	spoil(t, built)
	edit(t, p, filepath.Join("migrations", "20260727T153100_add_teams.go"), addTeams)

	if again := commandFor(t, p); spoiled(t, again) {
		t.Fatal("a migration was added and the cached runner was handed back anyway")
	}

	want := []string{"20260727T153000", "20260727T153100"}
	if loaded := discovered(t, commandFor(t, p)); !slices.Equal(loaded, want) {
		t.Errorf("the rebuilt runner loaded %v, want %v", loaded, want)
	}
}

func TestEditingTheModuleManifestRebuilds(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	spoil(t, built)

	manifest := filepath.Join(p.Root, "go.mod")

	content, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("reading go.mod: %v", err)
	}

	edit(t, p, "go.mod", string(content)+"\n// a dependency moved\n")

	if again := commandFor(t, p); spoiled(t, again) {
		t.Log("go.mod and go.sum are where every dependency version is pinned, so a change to")
		t.Log("either builds a different binary out of unchanged migrations.")
		t.Fatal("go.mod changed and the cached runner was handed back anyway")
	}
}

func TestAPackageOutsideTheMigrationsDirectoryIsNotInTheStamp(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	spoil(t, built)
	edit(t, p, filepath.Join("helpers", "helpers.go"), strings.Replace(
		helpersSource, `Suffix = ""`, `Suffix = "-edited"`, 1))

	if again := commandFor(t, p); !spoiled(t, again) {
		t.Log("That is better than what is documented -- but doc.go and ADR 0035 state this hole,")
		t.Log("so widening the stamp means updating both rather than leaving them wrong.")
		t.Fatal("the stamp now covers a package outside the migrations directory")
	}
}

func TestTheGeneratedSourceIsIdenticalForIdenticalInputs(t *testing.T) {
	first := generate("example.com/app/migrations")
	second := generate("example.com/app/migrations")

	if !bytes.Equal(first, second) {
		t.Fatal("generating the same runner twice produced different bytes (invariant #8)")
	}

	if !bytes.Contains(first, []byte(`_ "example.com/app/migrations"`)) {
		t.Errorf("the runner does not blank-import the migrations package:\n%s", first)
	}

	if !bytes.Contains(first, []byte(SDKPath)) {
		t.Errorf("the runner does not import %s:\n%s", SDKPath, first)
	}
}

func TestTheBuildDirectoryIgnoresItselfAndIsNotOverwritten(t *testing.T) {
	p := fixture(t)
	commandFor(t, p)

	ignore := filepath.Join(p.Root, workDir, ignoreFile)

	content, err := os.ReadFile(ignore)
	if err != nil {
		t.Fatalf("reading %s: %v", ignore, err)
	}

	if string(content) != ignoreAll {
		t.Errorf("%s holds %q, want %q", ignore, content, ignoreAll)
	}

	edit(t, p, filepath.Join(workDir, ignoreFile), "!keep-this\n")
	edit(t, p, filepath.Join("migrations", "20260727T153000_add_users.go"), editedUsers)
	commandFor(t, p)

	if again, _ := os.ReadFile(ignore); string(again) != "!keep-this\n" {
		t.Errorf("a rebuild overwrote an ignore file somebody had edited: %q", again)
	}
}

func TestAMigrationsDirectoryOutsideTheModuleIsRefused(t *testing.T) {
	p := fixture(t)
	p.Dir = filepath.Join(filepath.Dir(p.Root), "elsewhere")

	adapter, _ := New()

	_, err := adapter.Command(t.Context(), p)
	if !errors.Is(err, ErrOutsideModule) {
		t.Log("An import path computed anyway would name a package go build cannot resolve,")
		t.Log("so the failure would arrive as a compile error about generated code.")
		t.Fatalf("Command returned %v, want %v", err, ErrOutsideModule)
	}
}

func TestAProjectOutsideAnyModuleIsRefused(t *testing.T) {
	root := t.TempDir()
	p := projectAt(root)

	if err := os.MkdirAll(p.Dir, 0o750); err != nil {
		t.Fatalf("making the migrations directory: %v", err)
	}

	adapter, _ := New()

	if _, err := adapter.Command(t.Context(), p); !errors.Is(err, ErrNoModule) {
		t.Fatalf("Command returned %v, want %v", err, ErrNoModule)
	}
}

func TestAMigrationThatWillNotCompileIsReportedWithWhatTheCompilerSaid(t *testing.T) {
	p := fixture(t)
	edit(t, p, filepath.Join("migrations", "20260727T153000_add_users.go"),
		"package migrations\n\nfunc init() { thisDoesNotExist() }\n")

	adapter, _ := New()

	_, err := adapter.Command(t.Context(), p)

	var refused *BuildError
	if !errors.As(err, &refused) {
		t.Fatalf("Command returned %v, want a BuildError", err)
	}

	if !strings.Contains(refused.Output, "thisDoesNotExist") {
		t.Errorf("the failure does not carry the compiler's own words: %v", refused)
	}

	if refused.Path != filepath.Join(cacheIn(p.Root, "migrations").dir, sourceFile) {
		t.Errorf("the failure names %q, want the generated runner's stable path", refused.Path)
	}
}

func TestAFailedBuildLeavesNoStampToBeReadAsAHit(t *testing.T) {
	p := fixture(t)
	commandFor(t, p)

	edit(t, p, filepath.Join("migrations", "20260727T153000_add_users.go"),
		"package migrations\n\nfunc init() { thisDoesNotExist() }\n")

	adapter, _ := New()

	if _, err := adapter.Command(t.Context(), p); err == nil {
		t.Fatal("Command built a migrations package that does not compile")
	}

	if _, err := os.Stat(filepath.Join(cacheIn(p.Root, "migrations").dir, stampFile)); err == nil {
		t.Error("a failed build left a stamp behind, so the next run would reuse a stale binary")
	}
}

func TestTheAdapterAnswersToTheNameItIsRegisteredUnder(t *testing.T) {
	adapter, err := New()
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if adapter.Name() != Name {
		t.Errorf("the adapter reports %q, want %q", adapter.Name(), Name)
	}
}

func TestDiscoverFindsTheToolchainAndSaysSoWhenThereIsNone(t *testing.T) {
	adapter, _ := New()

	if err := adapter.Discover(t.Context()); err != nil {
		t.Fatalf("Discover: %v -- go is on PATH, since go test is what is running", err)
	}

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

// helpersSource is the fixture helper package as testdata holds it, for a test that edits it.
const helpersSource = `package helpers

const Suffix = ""
`

func TestADeletedBinaryRebuildsEvenWithItsStampStillThere(t *testing.T) {
	p := fixture(t)
	built := commandFor(t, p)

	if err := os.Remove(built.Path); err != nil {
		t.Fatalf("removing the built runner: %v", err)
	}

	t.Log("The stamp says what a binary was built from, not that one is there. A hit that")
	t.Log("trusted the stamp alone would hand back a path with nothing on the end of it.")

	if loaded := discovered(t, commandFor(t, p)); !slices.Equal(loaded, []string{"20260727T153000"}) {
		t.Errorf("the rebuilt runner loaded %v", loaded)
	}
}
