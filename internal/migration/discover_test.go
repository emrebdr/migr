package migration

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/emrebdr/migr/internal/remedy"
)

// dir builds a migrations directory holding one empty file per name.
func dir(names ...string) fstest.MapFS {
	fsys := make(fstest.MapFS, len(names))
	for _, name := range names {
		fsys[name] = &fstest.MapFile{}
	}

	return fsys
}

// discover runs discovery over a directory that is expected to hold only migrations.
func discover(t *testing.T, fsys fs.FS) []Migration {
	t.Helper()

	found, err := Discover(fsys)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	return found
}

// filenames renders what discovery returned, in the order it returned them.
func filenames(found []Migration) []string {
	names := make([]string, len(found))
	for i, m := range found {
		names[i] = m.Filename()
	}

	return names
}

func TestMigrationsComeBackInTheOrderTheyMustRun(t *testing.T) {
	fsys := dir(
		"20260727T153000_add_users.sql",
		"20250101T000000_create_schema.sql",
		"20260727T152959_one_second_earlier.sql",
		"20261231T235959_last.sql",
		"20260101T000000_new_year.sql",
	)

	want := []string{
		"20250101T000000_create_schema.sql",
		"20260101T000000_new_year.sql",
		"20260727T152959_one_second_earlier.sql",
		"20260727T153000_add_users.sql",
		"20261231T235959_last.sql",
	}

	if got := filenames(discover(t, fsys)); !slices.Equal(got, want) {
		t.Errorf("Discover returned\n  %v\nwant\n  %v", got, want)
		t.Log("The timestamp is the version and it defines the default order (CLAUDE.md §9).")
		t.Log("A directory listing is alphabetical, which agrees here only by accident of the")
		t.Log("format being fixed width -- the ordering has to be asserted, not inherited.")
	}
}

// reversedFS hands its entries back last-first, which fs.ReadDir permits a ReadDirFS to do.
type reversedFS struct {
	fs.FS
}

// ReadDir returns what the wrapped filesystem holds, in the opposite order.
func (r reversedFS) ReadDir(name string) ([]fs.DirEntry, error) {
	entries, err := fs.ReadDir(r.FS, name)
	if err != nil {
		return nil, err
	}

	slices.Reverse(entries)

	return entries, nil
}

func TestOrderingIsEstablishedRatherThanInherited(t *testing.T) {
	fsys := dir(
		"20250101T000000_create_schema.sql",
		"20260101T000000_new_year.sql",
		"20260727T153000_add_users.sql",
	)

	want := []string{
		"20250101T000000_create_schema.sql",
		"20260101T000000_new_year.sql",
		"20260727T153000_add_users.sql",
	}

	if got := filenames(discover(t, reversedFS{fsys})); !slices.Equal(got, want) {
		t.Errorf("Discover returned\n  %v\nwant\n  %v", got, want)
		t.Log("Handed a directory listing in the wrong order, Discover has to put it right.")
		t.Log("Every other test here would pass with no sort at all: a version is its filename's")
		t.Log("prefix and fs.ReadDir sorts by filename, so the two agree by accident. And that")
		t.Log("sort only runs when fs.ReadDir opens the directory itself -- a filesystem that")
		t.Log("implements ReadDirFS, as MapFS and os.DirFS both do, has its result passed back")
		t.Log("untouched, so the guarantee is the implementation's rather than the contract's.")
	}
}

func TestDiscoveryIsDeterministic(t *testing.T) {
	fsys := dir(
		"20260727T153000_add_users.sql",
		"20250101T000000_create_schema.ts",
		"20260101T000000_new_year.go",
	)

	first := filenames(discover(t, fsys))

	for i := range 8 {
		if again := filenames(discover(t, fsys)); !slices.Equal(again, first) {
			t.Fatalf("run %d returned %v, want %v", i, again, first)
		}
	}

	t.Log("Invariant #8: nothing that produces a plan may depend on map iteration order.")
	t.Log("A MapFS is a Go map, so fs.ReadDir's sorted contract is what this rests on.")
}

func TestTwoMigrationsCannotClaimOneVersion(t *testing.T) {
	fsys := dir(
		"20250101T000000_create_schema.sql",
		"20260727T153000_add_users.sql",
		"20260727T153000_add_orders.sql",
	)

	_, err := Discover(fsys)
	if err == nil {
		t.Fatal("two migrations sharing a version were accepted")
	}

	if !errors.Is(err, ErrDuplicateVersion) {
		t.Fatalf("Discover failed with %v, want ErrDuplicateVersion", err)
	}

	for _, want := range []string{
		"20260727T153000_add_orders.sql",
		"20260727T153000_add_users.sql",
		"20260727T153000",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q:\n  %v", want, err)
		}
	}

	if strings.Contains(err.Error(), "create_schema") {
		t.Errorf("the refusal names a migration that is not part of the collision:\n  %v", err)
	}
}

func TestTheCollisionIsReportedInAStableOrder(t *testing.T) {
	first := dir("20260727T153000_bbb.sql", "20260727T153000_aaa.sql")
	second := dir("20260727T153000_aaa.sql", "20260727T153000_bbb.sql")

	_, firstErr := Discover(first)
	_, secondErr := Discover(second)

	if firstErr == nil || secondErr == nil {
		t.Fatal("a collision went unreported")
	}

	if firstErr.Error() != secondErr.Error() {
		t.Errorf("the same two files are reported two ways:\n  %v\n  %v", firstErr, secondErr)
		t.Log("A MapFS is a map, so the only reason these agree is fs.ReadDir sorting and the")
		t.Log("sort being stable. An unstable sort makes this message flap between runs.")
	}
}

func TestOnlyDotfilesArePassedOver(t *testing.T) {
	fsys := dir(
		".gitkeep",
		".DS_Store",
		".hidden_notes.md",
		"20260727T153000_add_users.sql",
	)

	found := discover(t, fsys)

	if len(found) != 1 {
		t.Fatalf("Discover returned %v, want only the one migration", filenames(found))
	}

	t.Log("A .gitkeep is how an empty migrations directory survives a commit, and a .DS_Store")
	t.Log("appears whether or not anyone asked. Neither was ever meant to be a migration.")
}

func TestAFileThatIsNotAMigrationStopsTheCommand(t *testing.T) {
	for _, stray := range []string{"README.md", "notes.txt", "20260727T153000-add-users.ts"} {
		t.Run(stray, func(t *testing.T) {
			fsys := dir("20260727T153000_add_users.sql", stray)

			found, err := Discover(fsys)
			if err == nil {
				t.Fatalf("Discover returned %v, want a refusal naming %q", filenames(found), stray)
			}

			if !errors.Is(err, ErrMalformed) {
				t.Fatalf("Discover failed with %v, want ErrMalformed", err)
			}

			checkNamesTheFile(t, err, stray)
		})
	}

	t.Log("The last of these is the case that decides the policy. Hyphens where the grammar")
	t.Log("wants underscores is a migration someone wrote and expects to run; skipped quietly,")
	t.Log("`migr up` reports nothing to do and the change is found missing in production.")
	t.Log("A README failing loudly is by a wide margin the cheaper of the two mistakes.")
}

func TestASubdirectoryIsRefusedRatherThanSkipped(t *testing.T) {
	fsys := fstest.MapFS{
		"20260727T153000_add_users.sql": &fstest.MapFile{},
		"2026/20260101T000000_old.sql":  &fstest.MapFile{},
	}

	found, err := Discover(fsys)
	if err == nil {
		t.Fatalf("Discover returned %v, want a refusal naming the subdirectory", filenames(found))
	}

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("Discover failed with %v, want ErrMalformed", err)
	}

	if !strings.Contains(err.Error(), "2026") || !strings.Contains(err.Error(), "it is a directory") {
		t.Errorf("the refusal does not say a directory is what it found:\n  %v", err)
		t.Log("Matching \"directory\" alone passes on the remedy every refusal carries, which")
		t.Log("ends \"move it out of the migrations directory\" -- so the assertion would hold")
		t.Log("for a subdirectory refused as a file with no extension.")
	}

	t.Log("Discovery is flat. Skipped rather than refused, migrations/2026/ would be a whole")
	t.Log("tree of migrations that silently does not exist -- the same failure as a near-miss")
	t.Log("filename, one level up and holding more of them.")
}

func TestAnEmptyDirectoryIsNoMigrationsAndNoError(t *testing.T) {
	found, err := Discover(fstest.MapFS{})
	if err != nil {
		t.Fatalf("Discover over an empty directory: %v", err)
	}

	if len(found) != 0 {
		t.Errorf("Discover returned %v, want nothing", filenames(found))
	}
}

func TestADirectoryOfNothingButDotfilesIsAlsoNoMigrations(t *testing.T) {
	found, err := Discover(dir(".gitkeep"))
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	if len(found) != 0 {
		t.Errorf("Discover returned %v, want nothing", filenames(found))
	}
}

func TestAMissingDirectoryIsNotAnEmptyOne(t *testing.T) {
	_, err := Discover(os.DirFS(filepath.Join(t.TempDir(), "no_such_directory")))
	if err == nil {
		t.Fatal("a migrations directory that does not exist read as one with nothing in it")
	}

	if !errors.Is(err, ErrNoDirectory) {
		t.Fatalf("Discover failed with %v, want ErrNoDirectory", err)
	}

	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the refusal does not unwrap to the cause: %v", err)
	}

	if !strings.Contains(remedy.Of(err), "migr init") {
		t.Errorf("the refusal does not say what to do about it:\n  %v\n  next: %s", err, remedy.Of(err))
	}

	t.Log("Zero migrations is a fresh project. A directory that is not there is one nobody")
	t.Log("initialized, or a migrations.dir pointing somewhere no one meant -- and reading the")
	t.Log("second as the first makes a fully migrated database look like it has nothing to do.")
}

func TestDiscoveryWorksAgainstARealDirectory(t *testing.T) {
	root := t.TempDir()

	for _, name := range []string{
		"20260727T153000_add_users.sql",
		"20250101T000000_create_schema.sql",
		".gitkeep",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("-- migration\n"), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}

	found := discover(t, os.DirFS(root))

	want := []string{"20250101T000000_create_schema.sql", "20260727T153000_add_users.sql"}
	if got := filenames(found); !slices.Equal(got, want) {
		t.Fatalf("Discover returned %v, want %v", got, want)
	}

	source, err := os.ReadFile(filepath.Join(root, found[0].Filename()))
	if err != nil {
		t.Fatalf("reading back what discovery named: %v", err)
	}

	if string(source) != "-- migration\n" {
		t.Errorf("Filename() named a file holding %q", source)
	}

	t.Log("Everything else here runs on a MapFS. This is the one test that proves os.DirFS")
	t.Log("agrees with it, and that Filename() names a file M4-T7 can actually hash.")
}
