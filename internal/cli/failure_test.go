package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// coded is one way a command ends, and the exit code CLAUDE.md §5 documents for that ending.
type coded struct {
	// name says what happened, since a code on its own does not.
	name string
	// command is the subcommand it runs, which is what this matrix is held complete against.
	command string
	// args is the rest of the command line, the subcommand excluded.
	args []string
	// stage puts a project into the state this case needs and hands back what runs against it.
	stage func(t *testing.T) (string, *fake, *driver.Registry)
	// want is the exit code §5 documents for this ending.
	want int
}

// codes is every ending this build can produce, keyed by the command that produces it.
//
// It is one table rather than one test per case because §5 documents the codes as a set: the
// question is not whether up exits 4, it is whether every command agrees about what each code
// means. TestEveryCommandIsInTheExitCodeMatrix keeps it from going stale.
var codes = []coded{
	{name: "version", command: "version", stage: staged, want: ExitOK},
	{name: "config", command: "config", stage: staged, want: ExitOK},
	{
		name: "a config file that is not there", command: "config",
		args: []string{"--config", "absent.yaml"}, stage: staged, want: ExitValidation,
	},
	{name: "init in an empty directory", command: "init", stage: stagedEmpty, want: ExitOK},
	{name: "init over a config already there", command: "init", stage: staged, want: ExitRuntime},
	{
		name: "create", command: "create", args: []string{"add_repos"},
		stage: stagedFresh, want: ExitOK,
	},
	{
		name: "a name the grammar refuses", command: "create", args: []string{"add-repos"},
		stage: stagedFresh, want: ExitValidation,
	},
	{
		name: "a version another file already claims", command: "create",
		args: []string{"add_users"}, stage: staged, want: ExitRuntime,
	},
	{name: "up", command: "up", stage: staged, want: ExitOK},
	{name: "a step count naming nothing", command: "up", args: []string{"--step", "-1"},
		stage: staged, want: ExitValidation},
	{name: "a version to stop at that nothing carries", command: "up",
		args: []string{"--to", "20990101T000000"}, stage: staged, want: ExitValidation},
	{name: "--step with --to", command: "up", args: []string{"--step", "1", "--to", versionAt(0)},
		stage: staged, want: ExitValidation},
	{name: "a runtime that will not start", command: "up", stage: stagedGo, want: ExitRuntime},
	{name: "a migration nothing here reads", command: "up", stage: stagedUnroutable,
		want: ExitValidation},
	{name: "a filename the grammar refuses", command: "up", stage: stagedUnnamed,
		want: ExitValidation},
	{name: "a migrations directory that is not there", command: "up", stage: stagedNoDirectory,
		want: ExitRuntime},
	{name: "a migration file that will not open", command: "up", stage: stagedUnreadable,
		want: ExitRuntime},
	{name: "a database left between states", command: "up", stage: stagedDirty, want: ExitRuntime},
	{name: "the migration lock", command: "up", stage: stagedLocked, want: ExitLock},
	{name: "the migration lock, on a dry run", command: "up", args: []string{"--dry-run"},
		stage: stagedLocked, want: ExitLock},
	{name: "a step count naming nothing, on a dry run", command: "up",
		args: []string{"--dry-run", "--step", "-1"}, stage: staged, want: ExitValidation},
	{name: "a migration edited after it was applied", command: "up", stage: stagedDrifted,
		want: ExitDrift},
	{name: "down", command: "down", stage: stagedApplied, want: ExitOK},
	{name: "a version this database does not carry", command: "down",
		args: []string{"--to", versionAt(0)}, stage: staged, want: ExitRuntime},
	{name: "a migration declared irreversible", command: "down", stage: stagedIrreversible,
		want: ExitRuntime},
	{name: "the migration lock", command: "down", stage: stagedLocked, want: ExitLock},
	{name: "status over a database in every state", command: "status", stage: everyState,
		want: ExitOK},
	{name: "a migrations directory that is not there", command: "status", stage: stagedNoDirectory,
		want: ExitRuntime},
	{name: "history over a database in every state", command: "history", stage: everyState,
		want: ExitOK},
	{name: "plan", command: "plan", stage: staged, want: ExitOK},
	{name: "--step with --to", command: "plan", args: []string{"--step", "1", "--to", versionAt(0)},
		stage: staged, want: ExitValidation},
	{name: "a step count naming nothing", command: "plan", args: []string{"--step", "-1"},
		stage: staged, want: ExitValidation},
	{name: "the migration lock", command: "plan", stage: stagedLocked, want: ExitLock},
	{name: "validate", command: "validate", stage: staged, want: ExitOK},
	{name: "a migration nothing can undo", command: "validate", stage: stagedOneWay,
		want: ExitValidation},
	{name: "resolve over a database with nothing between states", command: "resolve",
		args: []string{"--resume"}, stage: stagedApplied, want: ExitOK},
	{name: "a database with no migration history at all", command: "resolve",
		args: []string{"--resume"}, stage: staged, want: ExitRuntime},
	{name: "no repair at all", command: "resolve", stage: stagedDirty, want: ExitValidation},
	{name: "two repairs at once", command: "resolve",
		args: []string{"--resume", "--applied"}, stage: stagedDirty, want: ExitValidation},
	{name: "an asserted repair with no reason", command: "resolve", args: []string{"--applied"},
		stage: stagedDirty, want: ExitValidation},
	{name: "the migration lock", command: "resolve", args: []string{"--resume"},
		stage: stagedLocked, want: ExitLock},
	{name: "a migration edited since the run that stopped", command: "resolve",
		args: []string{"--resume"}, stage: stoppedAndEdited, want: ExitDrift},
}

func TestEveryCommandEndsWithTheCodeSection5Documents(t *testing.T) {
	for _, c := range codes {
		t.Run(c.command+": "+c.name, func(t *testing.T) {
			dir, _, drivers := c.stage(t)

			_, _, err := runWith(t, dir, noon, drivers, append([]string{c.command}, c.args...)...)

			if got := ExitCode(err); got != c.want {
				t.Errorf("migr %s %v exited %d, want %d (%v)", c.command, c.args, got, c.want, err)
			}
		})
	}
}

func TestEveryCommandIsInTheExitCodeMatrix(t *testing.T) {
	covered := make(map[string]struct{}, len(codes))
	for _, c := range codes {
		covered[c.command] = struct{}{}
	}

	for _, cmd := range newRootCmd().Commands() {
		if _, found := covered[cmd.Name()]; !found {
			t.Errorf("migr %s produces no documented exit code in this matrix", cmd.Name())
			t.Log("An eleventh command is where a code stops meaning one thing across the CLI, so")
			t.Log("adding one costs saying which of the five endings it can reach.")
		}
	}
}

func TestAReportOverABrokenDatabaseStillSucceeds(t *testing.T) {
	dir, _, drivers := everyState(t)

	for _, command := range []string{"status", "history"} {
		if _, _, err := runWith(t, dir, noon, drivers, command); err != nil {
			t.Errorf("migr %s over a drifted and dirty database failed: %v", command, err)
		}
	}

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if got := ExitCode(err); got != ExitDrift {
		t.Errorf("migr up over that same database exited %d, want %d", got, ExitDrift)
		t.Log("The five states are a report's output rather than its failure, and migr up is the")
		t.Log("gate. Two commands answering different questions is not an inconsistency between")
		t.Log("them (ADR 0027, E).")
	}
}

func TestAReportNeverReachesTheCodeForTheMigrationLock(t *testing.T) {
	for _, command := range []string{"status", "history", "validate"} {
		dir, f, drivers := project(t, twoTables()...)
		f.lockErr = driver.ErrLockTimeout

		if _, _, err := runWith(t, dir, noon, drivers, command); err != nil {
			t.Errorf("migr %s failed against a database whose lock is held: %v", command, err)
		}

		if f.asked("lock") {
			t.Errorf("migr %s took the migration lock, which no report may", command)
		}
	}
}

func TestABareInvocationPrintsHelpAndSucceeds(t *testing.T) {
	dir, _, drivers := staged(t)

	stdout, _, err := runWith(t, dir, noon, drivers)
	if err != nil {
		t.Fatalf("migr with no arguments: %v", err)
	}

	if !strings.Contains(stdout, "Available Commands") {
		t.Errorf("migr with no arguments printed no help:\n%s", stdout)
		t.Log("The root carries an Args check so that `migr nonsense` is refused rather than")
		t.Log("answered with help, and cobra runs no Args check on a command with nothing to run.")
	}
}

func TestAFailureIsWrittenToStderrAndNothingElse(t *testing.T) {
	dir, _, drivers := stagedOneWay(t)

	stdout, stderr, err := runWith(t, dir, noon, drivers, "down")
	if err == nil {
		t.Fatal("down succeeded over a migration nothing can undo")
	}

	if !strings.Contains(stderr, "migr: ") {
		t.Errorf("the failure did not reach stderr:\n%s", stderr)
	}

	if strings.Contains(stdout, "migr: ") {
		t.Errorf("the failure reached stdout, where a result belongs:\n%s", stdout)
	}
}

func TestAFailureIsOneJSONObjectOnStderrWhenJSONWasAskedFor(t *testing.T) {
	dir, _, drivers := stagedOneWay(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "--json", "down")
	if err == nil {
		t.Fatal("down succeeded over a migration nothing can undo")
	}

	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if len(lines) != 1 {
		t.Fatalf("--json wrote %d lines to stderr, want one object:\n%s", len(lines), stderr)
	}

	got := decodeFailure(t, lines[0])

	if got.Code != ExitRuntime || got.Migration != fileAt(0, "one_way") {
		t.Errorf("the failure reads %+v, want the code and the migration it happened in", got)
	}
}

func TestARunThatCommittedSomethingLeavesOneDocumentOnEachStream(t *testing.T) {
	dir, f, drivers := project(t, twoTables()...)
	f.refuse = "CREATE TABLE teams (id bigint);"

	stdout, stderr, err := runWith(t, dir, noon, drivers, "--json", "up")
	if err == nil {
		t.Fatal("up reported success for a refused statement")
	}

	var summary map[string]any
	if decErr := json.Unmarshal([]byte(stdout), &summary); decErr != nil {
		t.Errorf("stdout is not one document: %v\n%s", decErr, stdout)
		t.Log("A run that committed the first migration and failed on the second owes both")
		t.Log("answers, and putting the failure beside the summary would make stdout two")
		t.Log("documents, which nothing can parse (ADR 0029, A).")
	}

	got := decodeFailure(t, strings.TrimSpace(stderr))

	if got.Migration != fileAt(1, "add_teams") || got.Operation != ir.KindRaw {
		t.Errorf("the failure reads %+v, want the migration and operation it happened in", got)
	}

	if got.Statement != 1 {
		t.Errorf("the failure sits at statement %d, want the one that was refused", got.Statement)
	}
}

func TestAnEditedMigrationIsReportedAsFieldsRatherThanAsAParagraph(t *testing.T) {
	moved := &engine.DriftError{
		Migration: migrationAt(0, "add_users"),
		Version:   versionAt(0),
		Direction: ir.Up,
		Recorded:  checksum.Checksum{Source: checksum.OfSource([]byte("as applied"))},
		Source:    checksum.OfSource([]byte("as edited")),
		AppliedAt: noon,
		Batch:     3,
		Ordinal:   1,
	}

	got := failureOf(moved)

	if strings.Contains(got.Error, "\n") {
		t.Errorf("the error field is a paragraph:\n%s", got.Error)
		t.Log("A DriftError renders four paragraphs of remediation, which is right for a terminal")
		t.Log("and unreadable as a JSON string. --json carries the values it is built from.")
	}

	if got.Migration != fileAt(0, "add_users") {
		t.Errorf("the failure names migration %q, want the file that was edited", got.Migration)
		t.Log("A drift refusal is not wrapped in an engine.MigrationError -- it is raised while")
		t.Log("the queue is being classified, before anything has a migration to fail in -- so it")
		t.Log("is the only failure that has to name its own.")
	}

	if got.Drift == nil {
		t.Fatalf("the failure carries no drift document: %+v", got)
	}

	if got.Drift.RecordedSource == got.Drift.CurrentSource || got.Drift.RecordedSource == "" {
		t.Errorf("the drift document reads %+v, want the two digests it moved between", got.Drift)
	}

	if got.Drift.Batch != 3 || got.Drift.Ordinal != 1 || got.Code != ExitDrift {
		t.Errorf("the drift document reads %+v (code %d), want the run that recorded it",
			got.Drift, got.Code)
	}
}

func TestAHalfRunMigrationIsReportedAsFieldsRatherThanAsAParagraph(t *testing.T) {
	stopped := &engine.DirtyError{
		Migration:     migrationAt(0, "add_users"),
		Direction:     ir.Up,
		LastStatement: 2,
		Statements:    5,
		Batch:         1,
		Ordinal:       1,
		Err:           errRefused,
	}

	got := failureOf(stopped)

	if strings.Contains(got.Error, "\n") {
		t.Errorf("the error field is a paragraph:\n%s", got.Error)
	}

	if got.Dirty == nil {
		t.Fatalf("the failure carries no dirty document: %+v", got)
	}

	if got.Dirty.LastStatement != 2 || got.Dirty.Statements != 5 {
		t.Errorf("the dirty document reads %+v, want how far the run got", got.Dirty)
	}

	if got.Dirty.Cause != errRefused.Error() {
		t.Errorf("the dirty document gives %q as the cause, want what stopped the run",
			got.Dirty.Cause)
	}
}

func TestAFailureAboutNoMigrationNamesNone(t *testing.T) {
	got := failureOf(errBothSelectors)

	if got.Migration != "" || got.Operation != "" || got.Drift != nil || got.Dirty != nil {
		t.Errorf("a refusal about two flags reads %+v, want the message alone", got)
	}

	if got.Error != errBothSelectors.Error() || got.Code != ExitRuntime {
		t.Errorf("the failure reads %+v, want the error itself", got)
	}
}

// named is one way a command fails about a migration, which every message must say which.
var named = []struct {
	name  string
	stage func(t *testing.T) (string, *fake, *driver.Registry)
	args  []string
	file  string
}{
	{name: "a migration nothing can undo", stage: stagedOneWay, args: []string{"down"},
		file: fileAt(0, "one_way")},
	{name: "a migration declared irreversible", stage: stagedIrreversible, args: []string{"down"},
		file: fileAt(0, "drop_sessions")},
	{name: "a statement the server refused", stage: stagedRefused, args: []string{"up"},
		file: fileAt(0, "add_users")},
	{name: "an operation the driver will not compile", stage: stagedUnsupported,
		args: []string{"up"}, file: fileAt(0, "add_users")},
	{name: "a migration edited after it was applied", stage: stagedDrifted, args: []string{"up"},
		file: fileAt(0, "add_users")},
	{name: "a database left between states", stage: stagedDirty, args: []string{"up"},
		file: fileAt(0, "add_users")},
	{name: "a version the database carries and no file describes", stage: stagedMissing,
		args: []string{"down"}, file: versionAt(0)},
	{name: "a migration this format cannot read", stage: stagedUnparsable, args: []string{"up"},
		file: fileAt(0, "add_users")},
}

func TestEveryFailureNamesTheMigrationItHappenedIn(t *testing.T) {
	for _, c := range named {
		t.Run(c.name, func(t *testing.T) {
			dir, _, drivers := c.stage(t)

			_, _, err := runWith(t, dir, noon, drivers, c.args...)
			if err == nil {
				t.Fatalf("migr %v succeeded", c.args)
			}

			if !strings.Contains(err.Error(), c.file) {
				t.Errorf("the failure does not name %s: %v", c.file, err)
				t.Log("Invariant #9: every user-facing error names the migration ID and the")
				t.Log("offending operation. An operator holding a message that names neither has")
				t.Log("to guess which of their migrations to open.")
			}
		})
	}
}

func TestAFailureFromAnOperationNamesIt(t *testing.T) {
	dir, _, drivers := stagedUnsupported(t)

	_, _, err := runWith(t, dir, noon, drivers, "up")
	if err == nil {
		t.Fatal("up succeeded over an operation the driver refuses")
	}

	if got := failureOf(err); got.Operation != ir.KindRaw {
		t.Errorf("the failure names operation %q, want the one that was refused", got.Operation)
	}

	if !strings.Contains(err.Error(), ir.KindRaw.String()) {
		t.Errorf("the message does not name the operation either: %v", err)
	}
}

// decodeFailure reads the object --json promised on stderr, failing the test rather than the command.
func decodeFailure(t *testing.T, line string) failure {
	t.Helper()

	var got failure
	if err := json.Unmarshal([]byte(line), &got); err != nil {
		t.Fatalf("the failure line is not JSON: %v\n%s", err, line)
	}

	return got
}

// migrationAt returns the migration the nth staged file describes, for an error built by hand.
func migrationAt(n int, name string) migration.Migration {
	parsed, err := migration.Parse(fileAt(n, name))
	if err != nil {
		panic(err)
	}

	return parsed
}

// staged is the project most cases run against: two migrations, both reversible.
func staged(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return project(t, twoTables()...)
}

// stagedFresh is the project migr init writes: a config, a migrations directory, and no migrations.
func stagedFresh(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return project(t)
}

// stagedEmpty is a directory with nothing in it, for the commands that create a project.
func stagedEmpty(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	f := &fake{}

	return t.TempDir(), f, f.registry(t)
}

// stagedApplied is a project whose migrations have all run, for the cases that undo them.
func stagedApplied(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("staging an applied project: %v", err)
	}

	return dir, f, drivers
}

// stagedLocked is a project whose database will not hand over the migration lock.
func stagedLocked(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.lockErr = driver.ErrLockTimeout

	return dir, f, drivers
}

// stagedDrifted is a project whose first migration was edited after it was applied.
func stagedDrifted(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)

	if _, _, err := runWith(t, dir, noon, drivers, "up", "--step", "1"); err != nil {
		t.Fatalf("staging an applied migration to edit: %v", err)
	}

	write(t, filepath.Join(dir, scaffoldDir, fileAt(0, "add_users")),
		twoTables()[0].body()+"\n-- edited after it was applied\n")

	return dir, f, drivers
}

// stagedDirty is a project whose database carries a run that stopped between states.
func stagedDirty(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)

	f.record(t, history.Record{
		Batch:         1,
		Ordinal:       1,
		Version:       versionAt(0),
		Name:          "add_users",
		Direction:     ir.Up,
		Outcome:       history.Dirty,
		Checksum:      checksum.Checksum{Source: checksum.OfSource(nil), IR: checksum.OfSource(nil)},
		Duration:      tick,
		LastStatement: 1,
		Error:         errRefused.Error(),
	})

	return dir, f, drivers
}

// stagedMissing is a project whose database carries a run for a file nobody kept.
func stagedMissing(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stagedApplied(t)
	remove(t, dir, fileAt(0, "add_users"))

	return dir, f, drivers
}

// stagedIrreversible is a project whose one migration says in the file that it cannot be undone.
func stagedIrreversible(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t,
		declaring("drop_sessions", "DROP TABLE legacy_sessions;", "the rows are gone"))

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("staging a declared migration: %v", err)
	}

	return dir, f, drivers
}

// stagedOneWay is a project whose one migration authors no undo and declares none either.
func stagedOneWay(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, stagedMigration{name: "one_way", up: "DROP TABLE legacy;"})

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("staging a one-way migration: %v", err)
	}

	return dir, f, drivers
}

// stagedRefused is a project whose first statement the database rejects.
func stagedRefused(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.refuse = "CREATE TABLE users (id bigint);"

	return dir, f, drivers
}

// stagedUnsupported is a project holding an operation this driver will not compile at all.
func stagedUnsupported(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	f.unsupported = "CREATE TABLE users (id bigint);"

	return dir, f, drivers
}

// stagedUnparsable is a project holding a file this format cannot tell the halves of.
func stagedUnparsable(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	write(t, filepath.Join(dir, scaffoldDir, fileAt(0, "add_users")), "CREATE TABLE t (id bigint);\n")

	return dir, f, drivers
}

// stagedUnnamed is a project holding a file the migration grammar refuses to name.
func stagedUnnamed(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)
	write(t, filepath.Join(dir, scaffoldDir, "not-a-migration.sql"), "-- migr:up\nSELECT 1;\n")

	return dir, f, drivers
}

// stagedNoDirectory is a project whose migrations directory the config names and nothing holds.
func stagedNoDirectory(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)

	if err := os.RemoveAll(filepath.Join(dir, scaffoldDir)); err != nil {
		t.Fatalf("removing the migrations directory: %v", err)
	}

	return dir, f, drivers
}

// stagedUnreadable is a project holding a migration discovery names and nothing can open.
func stagedUnreadable(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, twoTables()...)

	link := filepath.Join(dir, scaffoldDir, fileAt(2, "add_repos"))
	if err := os.Symlink(filepath.Join(dir, scaffoldDir, "gone.sql"), link); err != nil {
		t.Fatalf("staging an unreadable migration: %v", err)
	}

	return dir, f, drivers
}

// stagedGo is a project whose language migr runs, over an adapter that will not start a process.
func stagedGo(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	return goProject(t, "add_users", "add_teams")
}

// stagedUnroutable is a project holding a migration written in a language nothing here reads.
func stagedUnroutable(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := staged(t)
	write(t, filepath.Join(dir, scaffoldDir, versionAt(1)+"_add_other.py"), "def up(s):\n    pass\n")

	return dir, f, drivers
}
