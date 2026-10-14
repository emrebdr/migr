package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// applied is when the run a drift fixture reports on started, deliberately not in UTC.
var applied = time.Date(2026, 7, 27, 15, 31, 2, 0, time.FixedZone("CEST", 2*60*60))

// appliedUTC is that instant as a history row is read anywhere, which is the only spelling allowed.
const appliedUTC = "2026-07-27 13:31:02 UTC"

// edited returns a drifted status whose three digests are distinct, so no two can be confused.
func edited(t *testing.T, dir ir.Direction) (*DriftError, history.Status) {
	t.Helper()

	st := history.Status{
		Version:   versionAt(0),
		Migration: migrationAt(t, 0),
		Source:    checksum.OfSource([]byte("what the file holds now")),
		State:     history.StateDrifted,
		Recorded: history.Record{
			Batch:     3,
			Ordinal:   1,
			Version:   versionAt(0),
			Direction: ir.Up,
			Outcome:   history.Applied,
			Checksum: checksum.Checksum{
				Source: checksum.OfSource([]byte("what the file held when it ran")),
				IR:     checksum.OfSource([]byte("what that run emitted")),
			},
			AppliedAt: applied,
		},
	}

	var drift *DriftError
	if err := driftedError(st, dir, false); !errors.As(err, &drift) {
		t.Fatalf("driftedError returned %T, want a *DriftError", err)
	}

	return drift, st
}

func TestADriftMessageShowsBothRecordedDigestsAndTheRecomputedOneInFull(t *testing.T) {
	drift, st := edited(t, ir.Up)
	message := drift.Error()

	if !strings.Contains(message, st.Recorded.Checksum.String()) {
		t.Errorf("the message does not carry the recorded checksum in full:\n%s", message)
		t.Log("ADR 0009: a recorded value carries enough information to say which half moved,")
		t.Log("which it can only do if an operator is shown the whole value rather than a prefix.")
	}

	if !strings.Contains(message, st.Source.String()) {
		t.Errorf("the message does not carry the recomputed source digest in full:\n%s", message)
	}

	if strings.Count(message, st.Recorded.Checksum.Source.String()) != 1 {
		t.Errorf("the recorded source digest should appear once, inside the recorded value:\n%s",
			message)
		t.Log("Rendering the recomputed half from the recorded one makes the two lines agree")
		t.Log("always, which is a message that can never report the difference it exists for.")
	}
}

func TestADriftMessageNamesTheSourceHalfAsTheOneThatMoved(t *testing.T) {
	drift, _ := edited(t, ir.Up)
	message := drift.Error()

	if !strings.Contains(message, "source half of its checksum moved") {
		t.Errorf("the message does not say which half moved:\n%s", message)
		t.Log("ADR 0009: two recorded values invite the assumption that both are compared. The")
		t.Log("message is the only place that assumption gets corrected.")
	}

	if !strings.Contains(message, "only half drift is decided on") {
		t.Errorf("the message does not say that the other half decides nothing:\n%s", message)
	}

	for _, want := range []string{"recorded", "on disk", "recorded IR half", "nothing here"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not say %q:\n%s", want, message)
		}
	}

	if !strings.Contains(message, "flag rewrites a recorded checksum") {
		t.Errorf("the message does not say that nothing repairs this:\n%s", message)
		t.Log("Invariant #6: a checksum mismatch is a hard error and no flag repairs it. An")
		t.Log("operator who is not told that goes looking for the flag.")
	}
}

// labeled returns the one line of a message carrying a label, and fails when none or several do.
func labeled(t *testing.T, message, label string) string {
	t.Helper()

	var found []string

	for _, line := range strings.Split(message, "\n") {
		if strings.Contains(line, label) {
			found = append(found, line)
		}
	}

	if len(found) != 1 {
		t.Fatalf("%d lines carry %q, want exactly one:\n%s", len(found), label, message)
	}

	return found[0]
}

func TestADriftMessagePutsEachValueOnItsOwnLabeledLine(t *testing.T) {
	drift, st := edited(t, ir.Up)
	message := drift.Error()

	recorded := labeled(t, message, "  recorded   ")
	disk := labeled(t, message, "  on disk    ")

	if !strings.Contains(recorded, st.Recorded.Checksum.String()) {
		t.Errorf("the recorded line does not carry the recorded checksum:\n%s", message)
		t.Log("A message whose labels are the wrong way round tells an operator that the file")
		t.Log("on disk holds what the history recorded, which is the reverse of the truth.")
	}

	if !strings.Contains(disk, st.Source.String()) {
		t.Errorf("the on-disk line does not carry the recomputed digest:\n%s", message)
	}

	if strings.Contains(recorded, st.Source.String()) || strings.Contains(disk,
		st.Recorded.Checksum.String()) {
		t.Errorf("the two values share a line, which is what makes them hard to compare:\n%s",
			message)
		t.Log("Comparing two 64-character hex digests is this message's whole job, and it is")
		t.Log("the reason this one is a block where every other refusal here is a sentence.")
	}
}

func TestADriftMessageNamesTheRunThatRecordedIt(t *testing.T) {
	drift, _ := edited(t, ir.Up)
	message := drift.Error()

	for _, want := range []string{migrationAt(t, 0).Filename(), appliedUTC, "batch 3", "ordinal 1"} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not name %q:\n%s", want, message)
		}
	}

	if strings.Contains(message, "15:31:02") {
		t.Error("the applied instant renders in the reader's zone rather than in UTC")
		t.Log("A history row is read from wherever the operator is, and two people comparing")
		t.Log("notes about one run must not be shown two different times for it.")
	}
}

func TestADriftedVersionWithNoFileIsNamedByItsVersion(t *testing.T) {
	drift := &DriftError{Version: versionAt(4)}

	if !strings.Contains(drift.Error(), "version "+versionAt(4)) {
		t.Errorf("a drift with no migration does not name the version:\n%s", drift.Error())
		t.Log("Reconciliation reports missing rather than drifted for a version with no file, so")
		t.Log("this is unreachable today -- and a message rendering an empty filename is worse")
		t.Log("than a total function, since nothing would name the migration at all.")
	}

	if strings.Contains(drift.Error(), migrationAt(t, 4).Filename()) {
		t.Error("a drift with no migration rendered a filename it was never given")
	}
}

func TestARollbackSaysWhyDriftRefusesItForAStrongerReason(t *testing.T) {
	down, _ := edited(t, ir.Down)
	up, _ := edited(t, ir.Up)

	if !strings.Contains(down.Error(), "stronger reason") {
		t.Errorf("a rollback's refusal does not say why it is the stronger one:\n%s", down.Error())
		t.Log("The undo about to run would be authored by bytes that are not the ones that ran,")
		t.Log("which internal/engine's package doc has claimed since M4-T6 and no message said.")
	}

	if strings.Contains(up.Error(), "stronger reason") {
		t.Errorf("an apply's refusal claims a rollback's reason:\n%s", up.Error())
	}
}

func TestADriftErrorReportsTheSentinelAndCarriesItsValues(t *testing.T) {
	err := driftedError(history.Status{
		Version:   versionAt(0),
		Migration: migrationAt(t, 0),
		Source:    checksum.OfSource([]byte("edited")),
		Recorded:  history.Record{Checksum: sumOf(0), AppliedAt: applied},
	}, ir.Up, false)

	if !errors.Is(err, ErrDrifted) {
		t.Fatal("a drift error does not report itself as drift")
	}

	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("errors.As found no *DriftError in %T", err)
	}

	if drift.Recorded != sumOf(0) {
		t.Errorf("the error carries %v, want the checksum the run recorded", drift.Recorded)
	}

	if drift.Source != checksum.OfSource([]byte("edited")) {
		t.Errorf("the error carries %v, want what the file hashes to now", drift.Source)
	}

	if drift.Migration != migrationAt(t, 0) {
		t.Errorf("the error carries %v, want the migration that drifted", drift.Migration)
	}
}

func TestADriftErrorIsNotACorruptRowOrAnyOtherRefusal(t *testing.T) {
	drift, _ := edited(t, ir.Up)

	for _, other := range []error{
		history.ErrCorrupt,
		checksum.ErrMalformed,
		ErrDirty,
		ErrMissing,
		migration.ErrDuplicateVersion,
	} {
		if errors.Is(error(drift), other) {
			t.Errorf("drift also reports %v, and exit code 4 would then cover it", other)
			t.Log("ADR 0009 and internal/history's package doc both turn on this: an unreadable")
			t.Log("row needs manual repair and an unknown scheme needs a newer migr. Reporting")
			t.Log("either as exit code 4 is how operators learn to ignore exit code 4.")
		}
	}
}

// halted returns the refusal a database carrying one dirty run gets, built from the row it reads.
func halted(t *testing.T, m migration.Migration) *DirtyStateError {
	t.Helper()

	st := history.Status{
		Version:   versionAt(0),
		Migration: m,
		State:     history.StateDirty,
		Recorded: history.Record{
			Batch:         3,
			Ordinal:       2,
			Version:       versionAt(0),
			Direction:     ir.Up,
			Outcome:       history.Dirty,
			Checksum:      sumOf(0),
			AppliedAt:     applied,
			LastStatement: 4,
			Error:         "statement 5: ALTER TABLE nope ADD c int: no such table",
		},
	}

	var between *DirtyStateError
	if err := dirtyError(st, nil); !errors.As(err, &between) {
		t.Fatalf("dirtyError returned %T, want a *DirtyStateError", err)
	}

	return between
}

func TestARefusedRunSaysWhatSurvivedAndWhatStoppedIt(t *testing.T) {
	message := halted(t, migrationAt(t, 0)).Error()

	for _, want := range []string{
		migrationAt(t, 0).Filename(),
		"statements 1 through 4 of the up run",
		"nothing rolled them back",
		"statement 5: ALTER TABLE nope ADD c int: no such table",
		"whatever that run had left after statement 4",
		"batch 3, ordinal 2, at " + appliedUTC,
		"resolved explicitly",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not say %q:\n%s", want, message)
		}
	}
}

func TestARefusedRunDoesNotClaimHowManyStatementsThereWere(t *testing.T) {
	message := halted(t, migrationAt(t, 0)).Error()

	if !strings.Contains(message, "how far it got,\n               not how far it had to go") {
		t.Errorf("the refusal does not say what the row cannot:\n%s", message)
		t.Log("The statements were the ones that run emitted and nothing recomputes them: a")
		t.Log("migration may branch on live query results, so re-asking could emit a different")
		t.Log("list than the one that half-ran (ADR 0009).")
	}

	if strings.Contains(message, " of 4 ") || strings.Contains(message, "of 5 statements") {
		t.Errorf("the refusal invents a total the row does not carry:\n%s", message)
	}
}

func TestARefusedRunNamesTheDirectionTheStoppedRunWasPerforming(t *testing.T) {
	st := history.Status{
		Version:   versionAt(0),
		Migration: migrationAt(t, 0),
		Recorded: history.Record{
			Direction: ir.Down, LastStatement: 2, AppliedAt: applied, Error: "refused",
		},
	}

	if message := dirtyError(st, nil).Error(); !strings.Contains(message, "of the down run") {
		t.Errorf("the refusal does not say which run stopped:\n%s", message)
		t.Log("A rollback leaves this state too, and an operator told a down was an up would")
		t.Log("look for the wrong half of the migration.")
	}
}

func TestARefusedRunCountsOneSurvivingStatementAsOne(t *testing.T) {
	st := history.Status{
		Version:   versionAt(0),
		Migration: migrationAt(t, 0),
		Recorded: history.Record{
			Direction: ir.Up, LastStatement: 1, AppliedAt: applied, Error: "refused",
		},
	}

	message := dirtyError(st, nil).Error()
	if !strings.Contains(message, "statement 1 of the up run") {
		t.Errorf("the refusal does not render a single statement as one:\n%s", message)
	}

	if strings.Contains(message, "statements 1 through 1") {
		t.Errorf("the refusal renders one statement as a range:\n%s", message)
	}
}

func TestADirtyVersionWithNoFileIsNamedByItsVersion(t *testing.T) {
	message := halted(t, migration.Migration{}).Error()

	if !strings.Contains(message, "version "+versionAt(0)) {
		t.Errorf("the refusal does not name the version:\n%s", message)
		t.Log("A dirty run whose file nobody kept still refuses both directions (ADR 0019), and")
		t.Log("the version is then the only identity the database carries.")
	}
}

func TestARefusalThatReadARowWithNoCauseSaysSoRatherThanNothing(t *testing.T) {
	st := history.Status{
		Version:   versionAt(0),
		Migration: migrationAt(t, 0),
		Recorded:  history.Record{Direction: ir.Up, LastStatement: 2, AppliedAt: applied},
	}

	if message := dirtyError(st, nil).Error(); !strings.Contains(message, "bug in whatever wrote it") {
		t.Errorf("a row carrying no cause renders an empty line:\n%s", message)
		t.Log("history.Record.Validate refuses a dirty row with no error, so reaching this means")
		t.Log("a row written around the contract rather than a run that stopped silently.")
	}
}

func TestTheTwoSensesOfDirtyStayTellableApart(t *testing.T) {
	between := error(halted(t, migrationAt(t, 0)))

	if !errors.Is(between, ErrDirty) {
		t.Fatal("the refusal a dirty database gets does not report itself as dirty")
	}

	if errors.As(between, new(*DirtyError)) {
		t.Error("errors.As reads the refusal as the run that just became dirty")
		t.Log("ADR 0024: errors.Is(err, ErrDirty) finds both senses and errors.As on *DirtyError")
		t.Log("finds only the run that produced one. A command resolving the state branches on")
		t.Log("that, and a refusal mistaken for a fresh failure would be told a window it never had.")
	}

	became := &DirtyError{LastStatement: 4, Statements: 5, Err: errRefused}
	if errors.As(error(became), new(*DirtyStateError)) {
		t.Error("errors.As reads a run that became dirty as a refusal over a recorded one")
	}
}

func TestARefusalIsNotADriftOrAnyOtherBlockingState(t *testing.T) {
	between := error(halted(t, migrationAt(t, 0)))

	for _, other := range []error{ErrDrifted, ErrMissing, history.ErrCorrupt, ErrUnknownState} {
		if errors.Is(between, other) {
			t.Errorf("the dirty refusal also reports %v", other)
			t.Log("A dirty database exits 1 and a drifted one exits 4 (ADR 0029), so a refusal")
			t.Log("reporting both would resolve to whichever check ExitCode runs first.")
		}
	}
}
