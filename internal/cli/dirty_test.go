package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
)

// threeStatements is the migration this file drives: one statement lands, one fails, one never runs.
func threeStatements() []stagedMigration {
	return []stagedMigration{{
		name: "add_users",
		up: "CREATE TABLE users (id bigint);\n" +
			"ALTER TABLE nope ADD COLUMN orphan int;\n" +
			"CREATE INDEX idx_users ON users (id);",
		down: "DROP TABLE users;",
	}}
}

// goingDirty is a project whose database auto-commits DDL and refuses the second of three statements.
func goingDirty(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := project(t, threeStatements()...)
	f.autoCommits = true
	f.refuse = "ALTER TABLE nope ADD COLUMN orphan int;"

	return dir, f, drivers
}

// onlyRecorded returns the single run a failed invocation left, failing on any other number.
func onlyRecorded(t *testing.T, f *fake) history.Record {
	t.Helper()

	if len(f.runs) != 1 {
		t.Fatalf("the history holds %d rows, want the one the failed run is entitled to", len(f.runs))
	}

	return f.runs[0]
}

func TestTheRowARunWroteIsTheRowTheNextRunIsRefusedWith(t *testing.T) {
	dir, f, drivers := goingDirty(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "up")
	if ExitCode(err) != ExitRuntime {
		t.Fatalf("the first up exited %d (%v), want a dirty failure", ExitCode(err), err)
	}

	if !errors.As(err, new(*engine.DirtyError)) {
		t.Fatalf("the first up failed with %T, want the run that became dirty", err)
	}

	rec := onlyRecorded(t, f)
	if rec.Outcome != history.Dirty || rec.LastStatement != 1 {
		t.Fatalf("the run was recorded %q at statement %d, want dirty at 1",
			rec.Outcome, rec.LastStatement)
	}

	if !strings.Contains(stderr, "statements 1 through 1 of\n3 succeeded") {
		t.Errorf("the first up does not say how far it got:\n%s", stderr)
	}

	_, stderr, err = runWith(t, dir, noon, drivers, "up")

	var between *engine.DirtyStateError
	if !errors.As(err, &between) {
		t.Fatalf("the next up failed with %T, want the refusal a dirty database gets", err)
	}

	if ExitCode(err) != ExitRuntime {
		t.Errorf("the next up exited %d, want %d", ExitCode(err), ExitRuntime)
	}

	if between.LastStatement != rec.LastStatement || between.Direction != rec.Direction {
		t.Errorf("the refusal reads %d of the %s run, want the row's %d and %s",
			between.LastStatement, between.Direction, rec.LastStatement, rec.Direction)
	}

	if between.Cause != rec.Error {
		t.Errorf("the refusal gives %q as the cause, want the row's %q", between.Cause, rec.Error)
	}

	for _, want := range []string{
		fileAt(0, "add_users"),
		"statement 1 of the up run",
		"not applied",
		"batch 1, ordinal 1",
		errRefused.Error(),
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, stderr)
		}
	}

	t.Log("Every dirty refusal tested before this one read a hand-written fixture. This one reads")
	t.Log("the row the run on the line above wrote, which is the only way the two halves of")
	t.Log("ADR 0024 are held to each other.")
}

func TestTheRowARunWroteNamesTheStatementRatherThanTheWindow(t *testing.T) {
	dir, f, drivers := goingDirty(t)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil {
		t.Fatal("up succeeded over a statement the database refuses")
	}

	rec := onlyRecorded(t, f)

	if strings.Contains(rec.Error, "\n") {
		t.Errorf("the row's error spans %d lines:\n%s", strings.Count(rec.Error, "\n")+1, rec.Error)
		t.Log("Measured against MySQL 8.0 and MariaDB 11.4 before this task: the column held the")
		t.Log("whole window paragraph, 638 bytes over 10 lines, advice included -- and the")
		t.Log("refusal that reads it renders it inline.")
	}

	if !strings.Contains(rec.Error, "statement 2") {
		t.Errorf("the row does not name the statement that failed: %q", rec.Error)
	}

	if strings.Contains(rec.Error, "resolved explicitly") {
		t.Errorf("the row records the advice a refusal gives rather than the cause: %q", rec.Error)
	}
}

func TestEveryRunAgainstADirtyDatabaseIsRefusedTheSameWay(t *testing.T) {
	for _, args := range [][]string{{"up"}, {"down"}, {"plan"}, {"plan", "--down"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			dir, _, drivers := goingDirty(t)

			if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil {
				t.Fatal("the first up succeeded")
			}

			_, stderr, err := runWith(t, dir, noon, drivers, args...)
			if !errors.As(err, new(*engine.DirtyStateError)) {
				t.Fatalf("migr %v failed with %T, want the refusal a dirty database gets", args, err)
			}

			if !strings.Contains(stderr, "resolved explicitly") {
				t.Errorf("migr %v does not say the state must be resolved:\n%s", args, stderr)
			}
		})
	}

	t.Log("A dirty migration refuses before any selector narrows the run, so no --step or --to")
	t.Log("steps around one (ADR 0024), and a preview classifies the same way a run does.")
}

func TestAReportOverTheDatabaseARunLeftDirtyStillSucceeds(t *testing.T) {
	dir, _, drivers := goingDirty(t)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err == nil {
		t.Fatal("the first up succeeded")
	}

	for _, command := range []string{"status", "history"} {
		stdout, _, err := runWith(t, dir, noon, drivers, command)
		if err != nil {
			t.Fatalf("migr %s over a dirty database failed: %v", command, err)
		}

		if !strings.Contains(stdout, "dirty") {
			t.Errorf("migr %s does not report the state:\n%s", command, stdout)
		}
	}

	stdout, _, err := runWith(t, dir, noon, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(stdout, "stopped after statement 1 of the up run") {
		t.Errorf("status does not say how far the run got:\n%s", stdout)
	}
}

func TestTheTwoSensesOfDirtyAreNamedInTheOneJSONDocument(t *testing.T) {
	dir, _, drivers := goingDirty(t)

	_, stderr, err := runWith(t, dir, noon, drivers, "--json", "up")
	if err == nil {
		t.Fatal("the first up succeeded")
	}

	became := decodeFailure(t, strings.TrimSpace(stderr))
	if became.Dirty == nil {
		t.Fatalf("the failure carries no dirty document: %+v", became)
	}

	if became.Dirty.Sense != senseBecame {
		t.Errorf("the run that became dirty reports sense %q, want %q",
			became.Dirty.Sense, senseBecame)
	}

	if became.Dirty.Statements != 3 || became.Dirty.TransactionalDDL == nil ||
		*became.Dirty.TransactionalDDL {
		t.Errorf("the document reads %s, want 3 statements on a session that auto-commits",
			rendered(t, became.Dirty))
	}

	if became.Dirty.RecordedAt != nil {
		t.Errorf("a run reporting its own failure carries a recorded instant: %s",
			rendered(t, became.Dirty))
	}

	_, stderr, err = runWith(t, dir, noon, drivers, "--json", "up")
	if err == nil {
		t.Fatal("the next up succeeded")
	}

	already := decodeFailure(t, strings.TrimSpace(stderr))
	if already.Dirty == nil {
		t.Fatalf("the refusal carries no dirty document: %+v", already)
	}

	if already.Dirty.Sense != senseAlready {
		t.Errorf("the refusal reports sense %q, want %q", already.Dirty.Sense, senseAlready)
	}

	if already.Migration != fileAt(0, "add_users") || already.Dirty.Version != versionAt(0) {
		t.Errorf("the refusal names %q / %q, want the migration it is about",
			already.Migration, already.Dirty.Version)
	}

	if already.Dirty.RecordedAt == nil || already.Dirty.RecordedAt.IsZero() {
		t.Errorf("the refusal carries no recorded instant: %s", rendered(t, already.Dirty))
	}

	if already.Dirty.Statements != 0 || already.Dirty.TransactionalDDL != nil {
		t.Errorf("the refusal claims %s, which the row it read does not record",
			rendered(t, already.Dirty))
		t.Log("The row records how far the run got, not how far it had to go, and the session")
		t.Log("the refusal runs on is not the session that went dirty.")
	}

	if already.Error != engine.ErrDirty.Error() || strings.Contains(already.Error, "\n") {
		t.Errorf("the --json error is not one line: %q", already.Error)
	}
}

// rendered renders a dirty document the way --json wrote it, so a failure message shows the fields.
func rendered(t *testing.T, document *halted) string {
	t.Helper()

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("rendering the dirty document: %v", err)
	}

	return string(encoded)
}

func TestTheFieldsOnlyOneSenseKnowsAreAbsentRatherThanFalse(t *testing.T) {
	document := rendered(t, failureOf(&engine.DirtyStateError{
		Migration:     migrationAt(0, "add_users"),
		Version:       versionAt(0),
		Direction:     "up",
		LastStatement: 1,
		Batch:         1,
		Ordinal:       1,
		Cause:         errRefused.Error(),
	}).Dirty)

	for _, absent := range []string{"transactional_ddl", "statements"} {
		if strings.Contains(document, absent) {
			t.Errorf("the refusal's document carries %q: %s", absent, document)
			t.Log("A false transactional_ddl reads as a declaration this refusal never asked for,")
			t.Log("and a zero statements reads as a migration that compiled to nothing.")
		}
	}

	if !strings.Contains(document, `"sense":"already"`) {
		t.Errorf("the document does not name its sense: %s", document)
	}
}

func TestAnInstantAFailureDocumentCarriesIsRenderedInUTC(t *testing.T) {
	zoned := time.Date(2026, 9, 27, 17, 1, 37, 0, time.FixedZone("CEST", 2*60*60))

	between := failureOf(&engine.DirtyStateError{
		Migration: migrationAt(0, "add_users"), Version: versionAt(0), Direction: "up",
		LastStatement: 1, Batch: 1, Ordinal: 1, RecordedAt: zoned, Cause: errRefused.Error(),
	})

	moved := failureOf(&engine.DriftError{
		Migration: migrationAt(0, "add_users"), Version: versionAt(0), Direction: "up",
		Recorded: checksum.Checksum{Source: checksum.OfSource(nil), IR: checksum.OfSource(nil)},
		Source:   checksum.OfSource([]byte("edited")), AppliedAt: zoned,
	})

	for field, carried := range map[string]time.Time{
		"dirty.recorded_at": *between.Dirty.RecordedAt,
		"drift.applied_at":  moved.Drift.AppliedAt,
	} {
		if carried.Location() != time.UTC {
			t.Errorf("%s is rendered in %v, not UTC", field, carried.Location())
			t.Log("A history row is read anywhere, so an instant it carries has one spelling. Two")
			t.Log("documents of the same run from two machines would otherwise differ by an offset")
			t.Log("while naming the same moment, and a consumer comparing strings would see a change.")
		}

		if !carried.Equal(zoned) {
			t.Errorf("%s names %v rather than the instant recorded", field, carried)
		}
	}
}
