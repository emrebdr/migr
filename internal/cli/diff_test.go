package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/schema"
)

// A desired schema is every migration folded forward, so the fixtures here are a Go project a
// scripted runtime answers for -- actTypes emits a create_table, which is foldable, where every
// .sql migration is ir.Raw and is not.

func TestADiffOverADatabaseTheMigrationsAlreadyDescribeWritesNothing(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = projectedSchema(t)

	stdout, _, err := runDiffing(t, dir, actTypes, drivers, "diff")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if !strings.Contains(stdout, "nothing to write") {
		t.Errorf("a diff over an up-to-date database does not say so:\n%s", stdout)
	}

	if written := generatedFiles(t, dir); len(written) != 0 {
		t.Errorf("it wrote %v over a database that already matches", written)
	}

	t.Log("An empty migration would record \"nothing to do\" forever, and migr up refuses a")
	t.Log("migration that compiles to no statements anyway. So the answer is a sentence, not a file.")
}

func TestADiffWritesTheMigrationThatClosesTheGap(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = driftedSchema(t)

	stdout, _, err := runDiffing(t, dir, actTypes, drivers, "diff", "close_the_gap")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	written := generatedFiles(t, dir)
	if len(written) != 1 {
		t.Fatalf("it wrote %v, want one migration:\n%s", written, stdout)
	}

	if !strings.HasSuffix(written[0], "_close_the_gap.go") {
		t.Errorf("it wrote %q, which is not the name it was given", written[0])
	}

	body, err := os.ReadFile(filepath.Join(dir, scaffoldDir, written[0]))
	if err != nil {
		t.Fatalf("reading what it wrote: %v", err)
	}

	for _, want := range []string{`AddColumn("note", migr.Text())`, "Apply(ctx)"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("what it wrote does not contain %q:\n%s", want, body)
		}
	}

	if !strings.Contains(stdout, "add_column") {
		t.Errorf("it does not name the operations it wrote:\n%s", stdout)
	}
}

func TestADiffGoesThroughTheSameWriterMigrCreateDoes(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = driftedSchema(t)

	_, _, err := runScriptedAt(t, dir, actTypes, drivers, noon, "diff")
	if err == nil {
		t.Fatal("a diff wrote a file under a version another migration already holds")
	}

	if !strings.Contains(err.Error(), "already claims version") {
		t.Errorf("the refusal is not checkUnclaimed's: %v", err)
	}

	if written := generatedFiles(t, dir); len(written) != 0 {
		t.Errorf("a refused write left %v behind", written)
	}

	t.Log("One file format and one writer (ADR 0025, C): migration.New names it, checkUnclaimed")
	t.Log("refuses a version another file holds, and writeNew is O_EXCL -- so nothing migr writes")
	t.Log("can overwrite anything, and this command inherits all three rather than spelling them")
	t.Log("a second time.")
	t.Log("A diff is on a clock with second resolution, so one run a second after another over a")
	t.Log("still-drifted database is what reaches this -- and a second diff in the same second is")
	t.Log("refused by the writer rather than by luck.")
}

func TestADiffRefusesADatabaseTheMigrationsDoNotAlreadyDescribe(t *testing.T) {
	cases := []struct {
		name  string
		stage func(*testing.T) (string, *fake, *driver.Registry)
		says  string
	}{
		{"a database with migrations still to run", pendingGoProject, "Run `migr up` first"},
		{"a database with no history table at all", pendingGoProject, "no migration history"},
	}

	for _, one := range cases {
		t.Run(one.name, func(t *testing.T) {
			dir, f, drivers := one.stage(t)
			f.live = projectedSchema(t)

			_, _, err := runDiffing(t, dir, actTypes, drivers, "diff")
			if err == nil {
				t.Fatal("a diff over an unsettled database reported success")
			}

			if got := ExitCode(err); got != ExitRuntime {
				t.Errorf("it exited %d, want %d (%v)", got, ExitRuntime, err)
				t.Log("ADR 0029's rule: the same command line succeeds once migr up has run, so")
				t.Log("it is the world that is not ready rather than the argument being wrong.")
			}

			if written := generatedFiles(t, dir); len(written) != 0 {
				t.Errorf("a refused diff wrote %v", written)
			}
		})
	}

	t.Log("A desired schema folded from every migration is the schema the database should have")
	t.Log("once they have all run, so a diff over a half-migrated project would propose everything")
	t.Log("the pending ones do and then duplicate them.")
}

func TestADiffRefusesAProjectWhoseMigrationsCannotBeFolded(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.live = projectedSchema(t)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("applying the sql project: %v", err)
	}

	_, _, err := runWith(t, dir, later, drivers, "diff")
	if err == nil {
		t.Fatal("a diff over a .sql project reported success")
	}

	for _, want := range []string{"nothing here writes a generated migration",
		"cannot be read off them at all"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	if written := generatedFiles(t, dir); len(written) != 0 {
		t.Errorf("a refused diff wrote %v", written)
	}

	t.Log("Every statement in a .sql migration is ir.Raw, whose effect on the schema nothing can")
	t.Log("read -- so the project has no desired schema to compare against, and a partial fold")
	t.Log("would propose dropping whatever the SQL built. Refused whole, naming the file.")
}

func TestADiffRefusesAMixedProjectOverTheOneMigrationItCannotFold(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = projectedSchema(t)

	write(t, filepath.Join(dir, scaffoldDir, versionAt(5)+"_by_hand.sql"),
		"-- migr:up\nCREATE TABLE by_hand (id bigint);\n-- migr:down\nDROP TABLE by_hand;\n")

	if _, _, err := runDiffing(t, dir, actTypes, drivers, "up"); err != nil {
		t.Fatalf("applying the mixed project: %v", err)
	}

	_, _, err := runDiffing(t, dir, actTypes, drivers, "diff")
	if err == nil {
		t.Fatal("a diff over a project holding one unfoldable migration reported success")
	}

	for _, want := range []string{"by_hand.sql", "cannot be read off them",
		"Every .sql migration is one of these"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q: %v", want, err)
		}
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("it exited %d, want %d", got, ExitValidation)
	}

	if generated := generatedFiles(t, dir); len(generated) != 0 {
		t.Errorf("a refused diff wrote %v", generated)
	}

	t.Log("A .sql-only project is refused by the language check, which fires before anything")
	t.Log("connects -- so the fold's own refusal is reachable only from a mixed directory, which")
	t.Log("M7-T10 made legal. One raw operation leaves every schema after it unknowable, so the")
	t.Log("whole command is refused rather than diffed against a partial reading that would")
	t.Log("propose dropping whatever that migration built.")
}

func TestADiffTakesTheLockToReadAndReleasesItBeforeAskingAnything(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = driftedSchema(t)

	if _, _, err := runDiffing(t, dir, actTypes, drivers, "diff"); err != nil {
		t.Fatalf("diff: %v", err)
	}

	if !f.asked("lock") || !f.asked("unlock") {
		t.Fatalf("it did not take and release the migration lock: %v", f.calls)
	}

	order := f.calls
	unlocked, introspected := indexOf(order, "unlock"), indexOf(order, "introspect")

	if introspected < 0 || unlocked < 0 || introspected > unlocked {
		t.Errorf("it read the schema outside the lock: %v", order)
	}

	t.Log("The lock covers reading and not asking. A rename prompt waits on a person, and holding")
	t.Log("the migration lock across that would block every other migr run on the database for as")
	t.Log("long as they took to answer -- so this is the one command that locks, reads, and lets go")
	t.Log("before it does the rest of its work.")
}

func TestADiffDoesNotProposeReplacingAViewOrACheckThatIsAlreadyThere(t *testing.T) {
	dir, f, drivers := appliedGoProjectWith(t, actWords)
	f.live = engineWorded(t)

	stdout, _, err := runDiffing(t, dir, actWords, drivers, "diff")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	if !strings.Contains(stdout, "nothing to write") {
		t.Errorf("a view and a check the engine reworded were proposed again:\n%s", stdout)
	}

	if generated := generatedFiles(t, dir); len(generated) != 0 {
		t.Errorf("it wrote %v over a database that already matches", generated)
	}

	t.Log("Four of the five engines rewrite a view's body and a check's expression, so comparing")
	t.Log("them as written would have every migr diff over a database that is exactly up to date")
	t.Log("propose replacing every one of them. fold.Comparable carries the live text across for")
	t.Log("every element both schemas hold by name (ADR 0073, C).")
}

func TestADiffRendersWhatTheDifferWouldNotPropose(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = unexpressableSchema(t)

	stdout, _, err := runDiffing(t, dir, actTypes, drivers, "diff")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	for _, want := range []string{"does not close the gap", "schema.Unrepresentable", "×"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("it does not report what it would not propose (%q):\n%s", want, stdout)
		}
	}

	t.Log("The differ returns two things and a command that printed only the operations would")
	t.Log("write a migration that silently does not close the gap (ADR 0070, H).")
}

func TestADiffUnderJSONAsksNothingAndSaysWhatNothingConfirmed(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = renamedSchema(t)

	stdout, _, err := runDiffing(t, dir, actTypes, drivers, "--json", "diff")
	if err != nil {
		t.Fatalf("diff --json: %v", err)
	}

	var document generated
	if err := json.Unmarshal([]byte(stdout), &document); err != nil {
		t.Fatalf("decoding the document: %v\n%s", err, stdout)
	}

	if document.Renames != 0 {
		t.Errorf("a --json run confirmed %d renames with nowhere to ask", document.Renames)
	}

	if len(document.Unmatched) == 0 {
		t.Errorf("it does not report the rename nothing confirmed:\n%s", stdout)
	}

	if strings.Contains(stdout, "rename? [y/N/q]") {
		t.Errorf("it prompted into the document:\n%s", stdout)
	}

	t.Log("A document on stdout and a prompt on stdout cannot both be stdout, and --json puts JSON")
	t.Log("logs on stderr too -- so there is nowhere to ask. It runs and confirms nothing, which")
	t.Log("makes every candidate a drop and a create that invariant #7 gates: the safe side, and")
	t.Log("what ConfirmRenames's own default already is. Refusing outright would make this command")
	t.Log("unusable from the one place a JSON document is for.")
}

func TestADiffAsksAboutARenameAndWritesTheRenameInstead(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = renamedSchema(t)

	stdout, _, err := runScriptedInput(t, dir, actTypes, drivers, "y\n", "diff")
	if err != nil {
		t.Fatalf("diff: %v", err)
	}

	written := generatedFiles(t, dir)
	if len(written) != 1 {
		t.Fatalf("it wrote %v, want one migration:\n%s", written, stdout)
	}

	body, err := os.ReadFile(filepath.Join(dir, scaffoldDir, written[0]))
	if err != nil {
		t.Fatalf("reading what it wrote: %v", err)
	}

	if !strings.Contains(string(body), "RenameColumn") {
		t.Errorf("a confirmed rename did not reach the file:\n%s", body)
	}

	if strings.Contains(string(body), "DropColumn") {
		t.Errorf("a confirmed rename still wrote the drop:\n%s", body)
	}

	if !strings.Contains(stdout, "drops that did not happen") {
		t.Errorf("it does not say what confirming bought:\n%s", stdout)
	}
}

func TestADiffRefusesANameTheGrammarDoesNot(t *testing.T) {
	dir, f, drivers := appliedGoProject(t)
	f.live = driftedSchema(t)

	_, _, err := runDiffing(t, dir, actTypes, drivers, "diff", "Not-A-Name")
	if err == nil {
		t.Fatal("a name discovery would refuse was accepted")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("it exited %d, want %d (%v)", got, ExitValidation, err)
	}
}

// indexOf finds where a call appears in what a fake recorded, and reports -1 for one it never saw.
func indexOf(calls []string, call string) int {
	for i, one := range calls {
		if one == call {
			return i
		}
	}

	return -1
}

// generatedFiles names every migration in a project beyond the ones its fixture staged.
func generatedFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(filepath.Join(dir, scaffoldDir))
	if err != nil {
		t.Fatalf("reading the migrations directory: %v", err)
	}

	var written []string

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".go") && !strings.Contains(entry.Name(), "_first") {
			written = append(written, entry.Name())
		}
	}

	return written
}

// appliedGoProject stages a Go project whose one migration has already been applied.
func appliedGoProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return appliedGoProjectWith(t, actTypes)
}

// appliedGoProjectWith stages the same project over a runtime playing the script it is given.
func appliedGoProjectWith(t *testing.T, act string) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goProject(t, "first")

	if _, _, err := runScripted(t, dir, act, drivers, "up"); err != nil {
		t.Fatalf("staging an applied Go project: %v", err)
	}

	return dir, f, drivers
}

// pendingGoProject stages a Go project nothing has run, which is what a diff refuses.
func pendingGoProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goProject(t, "first")
	f.live = projectedSchema(t)

	return dir, f, drivers
}

// The migration here sits one second past noon, so the version the matrix's own clock produces is
// one no file claims -- which is what lets the run reach the lock it is about rather than stopping
// at the writer first.

// lockedGoProject stages an applied Go project whose database will not give up the migration lock.
func lockedGoProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t)
	write(t, filepath.Join(dir, config.DefaultFilename),
		strings.Replace(projectConfig, "language: sql", "language: go", 1))
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_second.go"), "package migrations\n")

	if _, _, err := runScriptedAt(t, dir, actTypes, drivers, later, "up"); err != nil {
		t.Fatalf("staging an applied Go project: %v", err)
	}

	f.live = projectedSchema(t)
	f.lockErr = driver.ErrLockTimeout

	return dir, f, drivers
}

// projectedSchema is the schema folding the scripted migration forward describes exactly.
func projectedSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	return &schema.Snapshot{Tables: []schema.Table{{
		Name: scriptedTable,
		Columns: []schema.Column{
			{Name: "id", Type: liveType(t, "int64")},
			{Name: "note", Type: liveType(t, "text")},
		},
	}}}
}

// driftedSchema is that schema with one column missing, which is the gap a diff closes.
func driftedSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	held := projectedSchema(t)
	held.Tables[0].Columns = held.Tables[0].Columns[:1]

	return held
}

// renamedSchema is that schema with one column under another name, which is a likely rename.
func renamedSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	held := projectedSchema(t)
	held.Tables[0].Columns[1].Name = "memo"

	return held
}

// engineWorded is the projected schema plus a view and a check the server reworded, as each does.
func engineWorded(t *testing.T) *schema.Snapshot {
	t.Helper()

	held := projectedSchema(t)
	held.Tables[0].Checks = []schema.Check{{Name: "scripted_positive", Expression: "((id > 0))"}}
	held.Views = []schema.View{{Name: "seen", Query: " SELECT id\n   FROM scripted;"}}

	return held
}

// unexpressableSchema carries something no operation expresses, so the diff reports a refusal.
func unexpressableSchema(t *testing.T) *schema.Snapshot {
	t.Helper()

	held := projectedSchema(t)
	held.Tables = append(held.Tables, schema.Table{
		Name:    "legacy",
		Columns: []schema.Column{{Name: "body", Type: opaqueType(t, "tsvector")}},
	})

	return held
}

// liveType is a column type a catalog answered precisely, which is what a live snapshot holds.
func liveType(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(parsed, spelling)
	if err != nil {
		t.Fatalf("ExactType(%q): %v", spelling, err)
	}

	return built
}

// opaqueType is a column type naming no logical type at all, which a diff refuses to touch.
func opaqueType(t *testing.T, native string) schema.ColumnType {
	t.Helper()

	built, err := schema.UnknownType(native)
	if err != nil {
		t.Fatalf("UnknownType(%q): %v", native, err)
	}

	return built
}

// later is a clock an hour past the one the fixtures stage with, since a version has second
// resolution and the generated migration must not claim the staged one's.
var later = noon.Add(time.Hour)

// runDiffing runs this command against a project a runtime reads, on a clock past the fixture's.
func runDiffing(t *testing.T, dir, act string, drivers *driver.Registry,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runScriptedAt(t, dir, act, drivers, later, args...)
}

// runScriptedAt runs this command on a clock the caller names, which is what a version comes from.
func runScriptedAt(t *testing.T, dir, act string, drivers *driver.Registry, at time.Time,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runScriptedTyped(t, dir, act, drivers, at, "", args...)
}

// runScriptedInput runs the CLI against a project a runtime reads, with something on its stdin.
func runScriptedInput(t *testing.T, dir, act string, drivers *driver.Registry, typed string,
	args ...string) (stdout, stderr string, err error) {
	t.Helper()

	return runScriptedTyped(t, dir, act, drivers, later, typed, args...)
}

// runScriptedTyped runs this command on one clock with one answer waiting on its stdin.
func runScriptedTyped(t *testing.T, dir, act string, drivers *driver.Registry, at time.Time,
	typed string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	t.Chdir(dir)

	var out, errOut bytes.Buffer

	opts := &options{clock: ticking(at), drivers: drivers, runtimes: scriptedRegistry(t, act)}

	cmd := rootCmd(opts)
	cmd.SetArgs(args)
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetIn(strings.NewReader(typed))

	err = execute(context.Background(), cmd, opts)

	return out.String(), errOut.String(), err
}
