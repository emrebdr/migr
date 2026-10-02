package cli

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// stoppedPartway is a project a real run left between states, so a resume has a row it can act on.
func stoppedPartway(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goingDirty(t)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil {
		t.Fatal("the run that was supposed to stop partway succeeded")
	}

	return dir, f, drivers
}

// readyToResume is that project with the cause of the failure gone, as fixing a server outside migr is.
func readyToResume(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stoppedPartway(t)
	f.refuse = ""

	return dir, f, drivers
}

// stoppedAndEdited is that same project with the migration rewritten after the run that stopped.
func stoppedAndEdited(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := stoppedPartway(t)

	write(t, filepath.Join(dir, scaffoldDir, fileAt(0, "add_users")),
		threeStatements()[0].body()+"\n-- edited after the run that stopped\n")

	return dir, f, drivers
}

// resolved runs the resolve command and returns what it printed, failing the test on a refusal.
func resolved(t *testing.T, dir string, drivers *driver.Registry, args ...string) string {
	t.Helper()

	stdout, _, err := runWith(t, dir, noon, drivers, append([]string{"resolve"}, args...)...)
	if err != nil {
		t.Fatalf("migr resolve %v: %v", args, err)
	}

	return stdout
}

func TestResolveRunsWhatTheStoppedRunNeverReachedAndClearsTheState(t *testing.T) {
	dir, f, drivers := readyToResume(t)

	before := onlyRecorded(t, f)
	stdout := resolved(t, dir, drivers, "--resume")

	for _, want := range []string{
		fileAt(0, "add_users"),
		"stopped after statement 1 of the up run",
		"will run statements 2 through 3 of 3",
		"ALTER TABLE nope ADD COLUMN orphan int;",
		"CREATE INDEX idx_users ON users (id);",
		"recorded as applied in batch",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the resolution does not say %q:\n%s", want, stdout)
		}
	}

	if len(f.runs) != 2 {
		t.Fatalf("the history holds %d rows, want the dirty one and the resume", len(f.runs))
	}

	if f.runs[1].Outcome != history.Applied || f.runs[1].Batch <= before.Batch {
		t.Errorf("the resume was recorded %+v, want applied in a later batch", f.runs[1])
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "up")
	if err != nil {
		t.Fatalf("migr up over the resolved database failed: %v", err)
	}

	if !strings.Contains(stdout, "nothing to do") {
		t.Errorf("migr up does not report the migration settled:\n%s", stdout)
	}

	t.Log("The whole point of the command: a database nothing could run against is one migr up")
	t.Log("now reports as having nothing to do.")
}

func TestResolveRunsTheStatementTheStoppedRunFailedOnFirst(t *testing.T) {
	dir, f, drivers := readyToResume(t)

	resolved(t, dir, drivers, "--resume")

	ran := f.statements[len(f.statements)-2:]
	want := []string{
		"ALTER TABLE nope ADD COLUMN orphan int;",
		"CREATE INDEX idx_users ON users (id);",
	}

	if !slices.Equal(ran, want) {
		t.Errorf("the resume ran %v, want %v", ran, want)
		t.Log("LastStatement is the last statement that *succeeded*, so a resume starts with the")
		t.Log("one that failed. Starting after it would skip a statement that never ran.")
	}
}

func TestResolveRecordsWhatAnOperatorDidAndRunsNothing(t *testing.T) {
	for _, c := range []struct {
		mode  string
		state string
	}{
		{mode: "--applied", state: "applied"},
		{mode: "--rolled-back", state: "pending"},
	} {
		t.Run(c.mode, func(t *testing.T) {
			dir, f, drivers := stoppedPartway(t)

			ran := len(f.statements)
			stdout := resolved(t, dir, drivers, c.mode, "--reason", "fixed it during the incident")

			if len(f.statements) != ran {
				t.Errorf("an assertion ran %v", f.statements[ran:])
			}

			if !strings.Contains(stdout, "running nothing: fixed it during the incident") {
				t.Errorf("the resolution does not record the reason:\n%s", stdout)
			}

			row := f.runs[len(f.runs)-1]
			if row.Outcome != history.Resolved || row.Error != "fixed it during the incident" {
				t.Errorf("the row reads %q / %q, want resolved and the reason", row.Outcome, row.Error)
			}

			report, _, err := runWith(t, dir, noon, drivers, "status")
			if err != nil {
				t.Fatalf("status: %v", err)
			}

			if !strings.Contains(report, c.state) ||
				!strings.Contains(report, "resolved by hand rather than run") {
				t.Errorf("status does not say the state was asserted:\n%s", report)
				t.Log("A state reached by assertion is not one reached by running, and a rollback")
				t.Log("over it behaves differently -- so a report that hid it would be the one")
				t.Log("place an operator could not find out.")
			}

			log, _, err := runWith(t, dir, noon, drivers, "history")
			if err != nil {
				t.Fatalf("history: %v", err)
			}

			if !strings.Contains(log, "resolved by hand, running nothing -- "+
				"fixed it during the incident") {
				t.Errorf("history does not write out the resolution and its reason:\n%s", log)
				t.Log("Every outcome that did not simply complete is a sentence under its run, and")
				t.Log("this one's sentence is the only record of why somebody settled it.")
			}
		})
	}
}

func TestAnAssertionsDocumentSaysItWasAssertedRatherThanApplied(t *testing.T) {
	dir, _, drivers := stoppedPartway(t)

	stdout, _, err := runWith(t, dir, noon, drivers,
		"--json", "resolve", "--applied", "--reason", "finished it by hand")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var got resolution
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("the result is not JSON: %v\n%s", err, stdout)
	}

	if got.Outcome != history.Resolved {
		t.Errorf("the document reports outcome %q, want %q", got.Outcome, history.Resolved)
		t.Log("A consumer reading applied here would believe migr ran the statements, which is")
		t.Log("the one thing this repair does not do.")
	}

	if got.Reason != "finished it by hand" || got.Direction != ir.Up {
		t.Errorf("the document reads %+v, want the reason and the asserted direction", got)
	}

	if len(got.Resuming) != 0 || got.Statements != 0 {
		t.Errorf("the document lists statements for a repair that compiled none: %+v", got)
	}
}

func TestADryRunResolvesNothingAndSaysSo(t *testing.T) {
	dir, f, drivers := stoppedPartway(t)

	rows, ran := len(f.runs), len(f.statements)
	stdout := resolved(t, dir, drivers, "--resume", "--dry-run")

	if !strings.Contains(stdout, "nothing was changed and nothing was recorded") {
		t.Errorf("a dry run does not say it changed nothing:\n%s", stdout)
	}

	if !strings.Contains(stdout, "will run statements 2 through 3 of 3") {
		t.Errorf("a dry run does not print what it would change:\n%s", stdout)
	}

	if len(f.runs) != rows || len(f.statements) != ran {
		t.Errorf("a dry run wrote %d rows and ran %d statements",
			len(f.runs)-rows, len(f.statements)-ran)
	}

	if _, _, err := runWith(t, dir, noon, drivers, "up"); !errors.Is(err, engine.ErrDirty) {
		t.Errorf("migr up after a dry run returned %v, want the database still between states", err)
	}
}

func TestResolveOverADatabaseWithNothingBetweenStatesChangesNothing(t *testing.T) {
	dir, f, drivers := stagedApplied(t)

	rows := len(f.runs)
	stdout := resolved(t, dir, drivers, "--resume")

	if !strings.Contains(stdout, "nothing to resolve") {
		t.Errorf("the resolution does not say there was nothing to do:\n%s", stdout)
	}

	if len(f.runs) != rows {
		t.Errorf("a resolution that found nothing wrote %d rows", len(f.runs)-rows)
	}
}

func TestAResumeOverAnEditedFileRefusesAndSaysWhatToDoInstead(t *testing.T) {
	dir, _, drivers := stoppedAndEdited(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "resolve", "--resume")
	if ExitCode(err) != ExitDrift {
		t.Fatalf("the resume exited %d (%v), want %d", ExitCode(err), err, ExitDrift)
	}

	for _, want := range []string{"stopped partway", "--applied or --rolled-back"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, stderr)
		}
	}
}

func TestAnAssertionOverAnEditedFileSettlesTheStateAndReportsTheEdit(t *testing.T) {
	dir, _, drivers := stoppedAndEdited(t)

	resolved(t, dir, drivers, "--applied", "--reason", "finished it, then fixed the file")

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(stdout, "drifted") {
		t.Errorf("status does not report the edit:\n%s", stdout)
		t.Log("The row copies the digest the run that stopped recorded, so settling the dirty")
		t.Log("state does not launder a file edit into an applied one: the edit surfaces as the")
		t.Log("separate problem invariant #6 already has a code for.")
	}
}

func TestTheResolutionDocumentCarriesTheRowEveryOtherCommandRenders(t *testing.T) {
	dir, _, drivers := readyToResume(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "resolve", "--resume")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}

	var got resolution
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("the result is not JSON: %v\n%s", err, stdout)
	}

	if !got.Settled || !got.Changed || got.Outcome != history.Applied {
		t.Errorf("the document reads %+v, want a settled, changed, applied resolution", got)
	}

	if got.Run == nil || got.Run.Outcome != history.Dirty || got.Run.LastStatement != 1 {
		t.Errorf("the document carries %+v as the run that stopped", got.Run)
	}

	if got.Run.Batch == 0 || got.Run.AppliedAt.IsZero() || got.Run.Error == "" {
		t.Errorf("the run document is built rather than read: %+v", got.Run)
		t.Log("It is the same recordedRun migr status and migr history print, so it carries the")
		t.Log("batch, the instant and the cause the row actually holds.")
	}

	if len(got.Resuming) != 2 || got.Resuming[0].Position != 2 {
		t.Errorf("the document lists %+v, want the two statements from position 2", got.Resuming)
	}
}
