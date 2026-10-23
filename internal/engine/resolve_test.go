package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// resumable stages a session a run left between states, with the statements it ran already behind it.
func resumable(t *testing.T, at string, specs ...string) (*recorder, Options, *source) {
	t.Helper()

	conn := stopping(t, false, at)
	opts, src := staged(t, specs...)

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("the run that was supposed to stop partway returned %v", err)
	}

	conn.calls = nil
	conn.fail = ""

	return conn, opts, src
}

// executed lists the statements a session was asked to run, in the order it was asked.
func executed(conn *recorder) []string {
	var ran []string

	for _, call := range conn.calls {
		if _, found := strings.CutPrefix(call, "exec "); found {
			ran = append(ran, strings.TrimPrefix(call, "exec "))
		}
	}

	return ran
}

// resume asks for the repair that runs what is left, which is every test here but the asserted ones.
func resume(t *testing.T, conn *recorder, opts Options) (Resolution, error) {
	t.Helper()

	return Resolve(t.Context(), conn, opts, Repair{Mode: RepairResume})
}

func TestAResumeRunsOnlyTheStatementsTheStoppedRunNeverReached(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")

	settled, err := resume(t, conn, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if ran := executed(conn); !slices.Equal(ran, []string{"d3", "e4"}) {
		t.Errorf("the resume ran %v, want only the statements after the recorded index", ran)
		t.Log("LastStatement is the last statement that *succeeded*, so the next one to run is")
		t.Log("the one that failed -- off by one either way replays a statement that already ran")
		t.Log("or skips one that never did.")
	}

	if settled.Stopped.LastStatement != 3 || settled.Statements != 5 || settled.Pending() != 2 {
		t.Errorf("the resolution reports %d of %d with %d pending, want 3 of 5 with 2",
			settled.Stopped.LastStatement, settled.Statements, settled.Pending())
	}

	if !settled.Ran || settled.Run == nil || settled.Run.Statements != 2 {
		t.Errorf("the resolution reports %+v, want a run of the 2 statements it executed", settled.Run)
	}
}

func TestAResumeRecordsTheMigrationAsAppliedRatherThanAsAnAssertion(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")

	if _, err := resume(t, conn, opts); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	rows := conn.store.runs
	if len(rows) != 2 {
		t.Fatalf("the history holds %d rows, want the dirty one and the resume", len(rows))
	}

	if rows[1].Outcome != history.Applied || rows[1].LastStatement != 0 {
		t.Errorf("the resume was recorded %q at statement %d, want applied at none",
			rows[1].Outcome, rows[1].LastStatement)
		t.Log("migr ran those statements, so the row is not an assertion -- and the dirty row")
		t.Log("above it is what records where this one picked up.")
	}

	if rows[1].Batch <= rows[0].Batch {
		t.Errorf("the resume recorded under batch %d, want one past the dirty run's %d",
			rows[1].Batch, rows[0].Batch)
	}
}

func TestAResumeThatStopsAgainRecordsAnAbsoluteIndex(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")
	conn.fail = "e4"

	_, err := resume(t, conn, opts)

	var dirty *DirtyError
	if !errors.As(err, &dirty) {
		t.Fatalf("Resolve returned %T, want a run that went dirty again", err)
	}

	if dirty.LastStatement != 4 || dirty.Statements != 5 {
		t.Errorf("the second failure reports %d of %d, want 4 of 5",
			dirty.LastStatement, dirty.Statements)
		t.Log("The counter continues the whole migration's numbering rather than restarting at")
		t.Log("the resume, or the next resume would skip back to a statement that already ran.")
	}

	rows := conn.store.runs
	if last := rows[len(rows)-1]; last.LastStatement != 4 || last.Outcome != history.Dirty {
		t.Errorf("the row reads %q at statement %d, want dirty at 4", last.Outcome, last.LastStatement)
	}
}

func TestAResumeOfARunThatHadReachedEveryStatementExecutesNothing(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")
	conn.store.runs[0].LastStatement = 5

	settled, err := resume(t, conn, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if ran := executed(conn); len(ran) != 0 {
		t.Errorf("the resume ran %v, want nothing", ran)
	}

	if settled.Pending() != 0 || !settled.Ran {
		t.Errorf("the resolution reports %d pending and ran %t, want none and recorded",
			settled.Pending(), settled.Ran)
	}

	if last := conn.store.runs[len(conn.store.runs)-1]; last.Outcome != history.Applied {
		t.Errorf("the run was recorded %q, want applied", last.Outcome)
		t.Log("A dirty row naming every statement means the commit or the history write failed")
		t.Log("after all of them took effect, so the schema change is complete and")
		t.Log("what is missing is the row. Recording it is not an assertion about anything.")
	}
}

func TestAResumeOfARollbackCarriesTheRollbackOn(t *testing.T) {
	conn := stopping(t, false, "")
	opts, _ := staged(t, "abc")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	conn.fail = "b1"

	if _, err := Rollback(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("the rollback that was supposed to stop partway returned %v", err)
	}

	conn.calls = nil
	conn.fail = ""

	settled, err := resume(t, conn, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if settled.Direction != ir.Down {
		t.Errorf("the resume went %q, want the direction the run that stopped was going", settled.Direction)
	}

	if ran := executed(conn); !slices.Equal(ran, []string{"b1", "a0"}) {
		t.Errorf("the resume ran %v, want the rest of the down run", ran)
		t.Log("A derived undo reverses the order as well as the operations, so the rest of a down")
		t.Log("run counts on through that order rather than through the migration's own.")
	}

	if state := foldedState(t, conn, opts); state != history.StatePending {
		t.Errorf("the migration reconciled to %q, want pending", state)
		t.Log("A completed rollback leaves the migration pending, and the fold reads an applied")
		t.Log("down row the same way whether a run or a resume wrote it.")
	}
}

// foldedState reports what the one staged migration reconciles to now, through the store contract.
func foldedState(t *testing.T, conn *recorder, opts Options) history.State {
	t.Helper()

	runs, err := conn.store.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	statuses, present := history.Reconcile(opts.Migrations, history.NewLog(runs)).Resolved()
	if !present || len(statuses) == 0 {
		t.Fatalf("the report holds %d statuses (present %t)", len(statuses), present)
	}

	return statuses[0].State
}

func TestAResumeIsRefusedWhenTheMigrationEmitsADifferentListOfOperations(t *testing.T) {
	conn, opts, src := resumable(t, "d3", "abcde")
	src.ops[versionAt(0)] = operations("abcdef")

	_, err := resume(t, conn, opts)
	if !errors.Is(err, ErrNotResumable) {
		t.Fatalf("Resolve returned %v, want a refusal reporting ErrNotResumable", err)
	}

	if ran := executed(conn); len(ran) != 0 {
		t.Errorf("the refused resume ran %v, want nothing", ran)
	}

	for _, want := range []string{"different list of operations", "--applied", "branch on live query results"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not say %q:\n%v", want, err)
		}
	}

	t.Log("The index names a statement in the list that run emitted. A migration may branch on")
	t.Log("live query results, so a second ask may legitimately emit another list -- and then")
	t.Log("statement 4 of it is not the statement statement 4 named.")
}

func TestAResumeIsRefusedWhenTheFileMovedSinceTheRunThatStopped(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")
	opts.Migrations = []history.File{history.NewFile(migrationAt(t, 0), []byte("edited"))}

	_, err := resume(t, conn, opts)
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("Resolve returned %v, want a refusal reporting ErrDrifted", err)
	}

	var drift *DriftError
	if !errors.As(err, &drift) || !drift.Resuming {
		t.Fatalf("errors.As found %+v, want a drift refusal that knows it refused a resume", drift)
	}

	if !strings.Contains(err.Error(), "stopped partway") ||
		!strings.Contains(err.Error(), "--rolled-back") {
		t.Errorf("the refusal reads as one about an applied migration:\n%v", err)
		t.Log("Both refusals are drift and both exit 4, but only one of the two can be resumed,")
		t.Log("so the advice they give an operator is not the same advice.")
	}
}

func TestAnAssertedRepairRecordsTheOperatorsReasonAndRunsNothing(t *testing.T) {
	conn, opts, src := resumable(t, "d3", "abcde")

	settled, err := Resolve(t.Context(), conn, opts, Repair{
		Mode: RepairApplied, Reason: "finished the index by hand",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if ran := executed(conn); len(ran) != 0 {
		t.Errorf("the assertion ran %v, want nothing", ran)
	}

	if asked := len(src.asked); asked != 1 {
		t.Errorf("the migration was asked what it does %d times, want only the run that stopped", asked)
		t.Log("An assertion compiles nothing and starts no runtime: a migration that no longer")
		t.Log("builds is exactly when an operator needs this.")
	}

	row := conn.store.runs[len(conn.store.runs)-1]
	if row.Outcome != history.Resolved || row.Error != "finished the index by hand" {
		t.Errorf("the row reads %q / %q, want resolved and the reason", row.Outcome, row.Error)
	}

	if settled.Statements != 0 || settled.Reason == "" {
		t.Errorf("the resolution reports %d statements, want none compiled", settled.Statements)
	}

	if state := foldedState(t, conn, opts); state != history.StateApplied {
		t.Errorf("the migration reconciled to %q, want applied", state)
	}
}

func TestAnAssertedRollbackLeavesTheMigrationPending(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")

	if _, err := Resolve(t.Context(), conn, opts, Repair{
		Mode: RepairRolledBack, Reason: "undid the two statements by hand",
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	row := conn.store.runs[len(conn.store.runs)-1]
	if row.Direction != ir.Down {
		t.Errorf("the row records a %q run, want the direction the operator asserted", row.Direction)
	}

	if state := foldedState(t, conn, opts); state != history.StatePending {
		t.Errorf("the migration reconciled to %q, want pending", state)
	}
}

func TestAnAssertedRepairCopiesTheRecordedDigestRatherThanTheFilesOwnNow(t *testing.T) {
	conn, opts, _ := resumable(t, "d3", "abcde")

	recorded := conn.store.runs[0].Checksum
	opts.Migrations = []history.File{history.NewFile(migrationAt(t, 0), []byte("edited since"))}

	if _, err := Resolve(t.Context(), conn, opts, Repair{
		Mode: RepairApplied, Reason: "finished it by hand, then fixed the file",
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if row := conn.store.runs[len(conn.store.runs)-1]; row.Checksum != recorded {
		t.Errorf("the row records %v, want the checksum the run that stopped recorded", row.Checksum)
	}

	if state := foldedState(t, conn, opts); state != history.StateDrifted {
		t.Errorf("the migration reconciled to %q, want drifted", state)
		t.Log("Recording the file's current digest would launder an edit into an applied state.")
		t.Log("Copying the recorded one settles the dirty state and reports the edit as the")
		t.Log("separate problem it is, which invariant #6 already has a code for.")
	}
}

func TestADryRunChangesNothingAndLeavesTheMigrationBetweenStates(t *testing.T) {
	for _, ask := range []Repair{
		{Mode: RepairResume, DryRun: true},
		{Mode: RepairApplied, Reason: "by hand", DryRun: true},
	} {
		t.Run(ask.Mode.String(), func(t *testing.T) {
			conn, opts, _ := resumable(t, "d3", "abcde")

			settled, err := Resolve(t.Context(), conn, opts, ask)
			if err != nil {
				t.Fatalf("Resolve: %v", err)
			}

			if settled.Ran {
				t.Error("a dry run reports that it changed something")
			}

			if ran := executed(conn); len(ran) != 0 {
				t.Errorf("a dry run ran %v", ran)
			}

			if len(conn.store.runs) != 1 {
				t.Errorf("a dry run left %d rows, want only the dirty one", len(conn.store.runs))
			}

			if state := foldedState(t, conn, opts); state != history.StateDirty {
				t.Errorf("the migration reconciled to %q, want dirty still", state)
			}
		})
	}
}

func TestADatabaseWithNothingBetweenStatesResolvesToNothingAtAll(t *testing.T) {
	conn := newRecorder(t, false)
	conn.store.table = true
	opts, _ := staged(t, "abc")

	settled, err := resume(t, conn, opts)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if settled.Version != "" || settled.Ran {
		t.Errorf("the resolution reports %+v, want nothing found and nothing changed", settled)
	}

	if len(conn.store.runs) != 0 {
		t.Errorf("a resolution that found nothing wrote %d rows", len(conn.store.runs))
	}
}

func TestADatabaseWithNoHistoryTableIsNotResolvedIntoHavingOne(t *testing.T) {
	conn := newRecorder(t, false)
	opts, _ := staged(t, "abc")

	_, err := resume(t, conn, opts)
	if !errors.Is(err, history.ErrNoTable) {
		t.Fatalf("Resolve returned %v, want a refusal reporting ErrNoTable", err)
	}

	if slices.Contains(conn.calls, "ensure") {
		t.Error("a resolution created the history table it was asked to repair a row in")
		t.Log("A command whose whole job is repair may not be the one that invents the record")
		t.Log("it repairs. migr up is what makes a history table.")
	}
}

func TestTwoMigrationsBetweenStatesAreRefusedRatherThanOnePickedFor(t *testing.T) {
	conn, opts, _ := resumable(t, "b1", "ab", "cd")

	second := conn.store.runs[0]
	second.Batch = 9
	second.Version = versionAt(1)
	second.Name = migrationAt(t, 1).Name

	if err := conn.store.MarkDirty(t.Context(), second, 1, errRefused); err != nil {
		t.Fatalf("MarkDirty: %v", err)
	}

	_, err := resume(t, conn, opts)
	if !errors.Is(err, ErrSeveralDirty) {
		t.Fatalf("Resolve returned %v, want a refusal reporting ErrSeveralDirty", err)
	}

	for _, want := range []string{migrationAt(t, 0).Filename(), migrationAt(t, 1).Filename()} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %s:\n%v", want, err)
		}
	}
}

func TestARepairRefusesWhatItCannotActOnBeforeTakingTheLock(t *testing.T) {
	cases := []struct {
		name string
		ask  Repair
		want error
	}{
		{name: "no repair at all", ask: Repair{}, want: ErrUnknownRepair},
		{name: "a repair this build has no rule for", ask: Repair{Mode: "tidy"},
			want: ErrUnknownRepair},
		{name: "an assertion with no reason", ask: Repair{Mode: RepairApplied}, want: ErrNoReason},
		{name: "a blank reason", ask: Repair{Mode: RepairRolledBack, Reason: "  \t "},
			want: ErrNoReason},
		{name: "a reason on a resume", ask: Repair{Mode: RepairResume, Reason: "by hand"},
			want: ErrNoReason},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			conn, opts, _ := resumable(t, "d3", "abcde")
			conn.calls = nil

			if _, err := Resolve(t.Context(), conn, opts, c.ask); !errors.Is(err, c.want) {
				t.Fatalf("Resolve returned %v, want %v", err, c.want)
			}

			if slices.Contains(conn.calls, "lock") {
				t.Error("a repair nothing can act on took the migration lock first")
			}
		})
	}
}

func TestEveryRepairIsNamedAndTellsAssertionFromRunning(t *testing.T) {
	if len(Repairs()) != 3 {
		t.Fatalf("Repairs() returned %d, want the three an operator chooses between", len(Repairs()))
	}

	asserts := map[RepairMode]bool{
		RepairResume: false, RepairApplied: true, RepairRolledBack: true,
	}

	for _, mode := range Repairs() {
		if !mode.Valid() {
			t.Errorf("%q is listed and not valid", mode)
		}

		if mode.Asserts() != asserts[mode] {
			t.Errorf("%s.Asserts() = %v, want %v", mode, mode.Asserts(), asserts[mode])
		}
	}

	first := Repairs()
	first[0] = "tampered"

	if slices.Contains(Repairs(), "tampered") {
		t.Error("a caller mutated the package's own list of repairs")
	}
}
