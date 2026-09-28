package cli

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestUpRunsAMigrationOnlyARuntimeCanRead(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	t.Log("A .go file carries no SQL, so every statement here crossed the wire as IR and was")
	t.Log("compiled by the driver. That is the whole stack for a language migr does not read.")

	if !slices.Contains(f.statements, "CREATE TABLE scripted (id bigint)") {
		t.Errorf("the statements this run executed are %v, want the one the runtime emitted",
			f.statements)
	}
}

func TestDownRunsWhatTheRuntimeEmitsForThatDirection(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	if _, _, err := runScripted(t, dir, actEmits, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if _, _, err := runScripted(t, dir, actEmits, drivers, "down"); err != nil {
		t.Fatalf("down: %v", err)
	}

	if !slices.Contains(f.statements, "DROP TABLE scripted") {
		t.Errorf("the statements these runs executed are %v, want the undo the runtime emitted",
			f.statements)
	}
}

func TestARollbackDerivedFromARuntimesOwnUpReportsWhatCannotBeInverted(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	if _, _, err := runScripted(t, dir, actDerives, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	_, _, err := runScripted(t, dir, actDerives, drivers, "down")
	if err == nil {
		t.Fatal("down inverted a raw operation, which has no inverse to derive")
	}

	t.Log("The runtime answered no_down_authored, so the engine asked its up as a question and")
	t.Log("tried to invert it -- which is the derive path reached through a live runtime.")

	if !strings.Contains(err.Error(), "opaque") {
		t.Errorf("the refusal does not carry why a raw operation has no inverse: %v", err)
	}
}

func TestValidateChecksARuntimeProjectWithoutOpeningADatabase(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	stdout, _, err := runScripted(t, dir, actEmits, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	if !strings.Contains(stdout, "no problems found") {
		t.Errorf("validate found something in a migration that compiles:\n%s", stdout)
	}

	t.Log("ADR 0028's promise is the command's point, and a language needing a runtime does not")
	t.Log("cost it: the process starts, and every database callback it could make refuses.")

	if slices.Contains(f.calls, "open") {
		t.Errorf("validate opened a database: %v", f.calls)
	}
}

func TestValidateReportsAHalfItCouldNotCheckRatherThanPassingIt(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	stdout, _, err := runScripted(t, dir, actReads, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	t.Log("A migration that reads cannot be checked without a database, and reporting it as")
	t.Log("passing would be the one wrong answer: silence about the half nothing judged.")

	for _, want := range []string{
		"not checked",
		"20260727T153000_add_scripted.go",
		noDatabase + ", so 1 check could not be made.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("validate does not report %q:\n%s", want, stdout)
		}
	}

	t.Log("The summary line has to say it too. A run whose only mention of an unchecked half is")
	t.Log("a block above the total reads, at a glance, as a clean run.")
}

func TestAnUncheckableDownIsReportedWhereTheUpWasFine(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	stdout, _, err := runScripted(t, dir, actReadsDown, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	t.Log("A migration may read in only one of its halves, and the half that read nothing is")
	t.Log("still checkable. Routing only the up would report this one as a format problem.")

	if !strings.Contains(stdout, "not checked") || !strings.Contains(stdout, "down") {
		t.Errorf("the down that reached a database is not reported unchecked:\n%s", stdout)
	}

	if !strings.Contains(stdout, "no problems found") {
		t.Errorf("a half nothing could judge was reported as a problem:\n%s", stdout)
	}
}

func TestAHalfNothingCouldCheckIsNotAValidationFailure(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted")

	_, _, err := runScripted(t, dir, actReads, drivers, "validate")
	if err != nil {
		t.Errorf("validate failed over a migration it merely could not check: %v", err)
		t.Log("Unchecked is not a problem: failing here would make a CI gate reject a migration")
		t.Log("for reading, which is a thing migrations are allowed to do.")
	}
}

func TestAConnectionIsGivenBackWhenTheSourceWillNotStart(t *testing.T) {
	dir, f, drivers := goProject(t, "add_scripted")

	_, _, err := runOptions(t, dir, &options{clock: ticking(noon), drivers: drivers,
		runtimes: runtimeRegistry(t, "go")}, "up")
	if err == nil {
		t.Fatal("up ran against an adapter that starts nothing")
	}

	t.Log("The source is built after the connection, so a source that will not start leaves one")
	t.Log("open. A failed migr up must not leak a backend.")

	if !slices.Contains(f.calls, "close") {
		t.Errorf("the connection was never closed: %v", f.calls)
	}
}

func TestASessionEndsItsRuntimeBeforeGivingTheConnectionBack(t *testing.T) {
	var order []string

	s := &session{
		conn: &recordingConn{closed: func() { order = append(order, "conn") }},
		stop: func(context.Context) error {
			order = append(order, "runtime")

			return nil
		},
	}

	if err := s.close(t.Context()); err != nil {
		t.Fatalf("close: %v", err)
	}

	t.Log("A callback in flight reaches that connection, so the process has to be gone before")
	t.Log("the connection is. The two defers are not interchangeable.")

	if !slices.Equal(order, []string{"runtime", "conn"}) {
		t.Errorf("close ran %v, want the runtime ended first", order)
	}
}

func TestASessionOverAFileClosesTheConnectionAndNothingElse(t *testing.T) {
	s := &session{conn: &recordingConn{closed: func() {}}}

	if err := s.close(t.Context()); err != nil {
		t.Errorf("close over a source that started no process: %v", err)
	}
}

func TestACloseReportsBothHalvesRatherThanTheFirst(t *testing.T) {
	failedStop := errors.New("the runtime would not stop")
	failedClose := errors.New("the connection would not close")

	s := &session{
		conn: &recordingConn{closed: func() {}, err: failedClose},
		stop: func(context.Context) error { return failedStop },
	}

	err := s.close(t.Context())
	for _, want := range []error{failedStop, failedClose} {
		if !errors.Is(err, want) {
			t.Errorf("close reported %v, which does not carry %v", err, want)
		}
	}
}

func TestOneMigrationReachingTheDatabaseDoesNotMakeTheNextUncheckable(t *testing.T) {
	dir, _, drivers := goProject(t, "add_scripted", "add_later")

	stdout, _, err := runScripted(t, dir, actReadsFirst, drivers, "validate")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}

	t.Log("What the offline session remembers is per question, not per run. Forgetting nothing")
	t.Log("would spread the first migration's read over every migration after it.")

	if !strings.Contains(stdout, "1 check") {
		t.Errorf("validate reports more than the one half that reached a database:\n%s", stdout)
	}

	if strings.Contains(stdout, "20260727T153001_add_later.go") {
		t.Errorf("the migration that read nothing was reported unchecked:\n%s", stdout)
	}
}

// recordingConn is a session that notes when it was closed, for the ordering the close ladder owes.
type recordingConn struct {
	driver.Conn

	// closed is called when this session is given back.
	closed func()
	// err is what Close reports, nil for one that closes cleanly.
	err error
}

// Close notes that this session was given back, and reports whatever it was built to report.
func (c *recordingConn) Close(context.Context) error {
	c.closed()

	return c.err
}

func TestALanguageMigrDoesNotKnowIsRefusedNamingTheOnesItDoes(t *testing.T) {
	_, err := runnable("cobol", scriptedRegistry(t, actEmits))
	if err == nil {
		t.Fatal("runnable returned an adapter for a language migr does not know")
	}

	t.Log("Config validation refuses this before a command reaches here, so nothing else can")
	t.Log("exercise it -- and a guard nothing exercises is one that stops being true quietly.")

	for _, want := range []string{"cobol", "sql", "go", "typescript"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
}
