package history

import (
	"reflect"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

func TestAMigrationWithAFileAndNoRunsIsPending(t *testing.T) {
	users := file(30, "add_users", "-- users")

	if got := resolve(t, users).State; got != StatePending {
		t.Errorf("a migration nothing ever ran resolved to %s, want %s", got, StatePending)
	}
}

func TestAnUpThatAppliedLeavesTheMigrationApplied(t *testing.T) {
	users := file(30, "add_users", "-- users")

	if got := resolve(t, users, up(users, 1, Applied)).State; got != StateApplied {
		t.Errorf("an applied forward run resolved to %s, want %s", got, StateApplied)
	}
}

func TestAFailedUpLeavesItPendingBecauseTheSchemaDidNotChange(t *testing.T) {
	users := file(30, "add_users", "-- users")

	if got := resolve(t, users, up(users, 1, Failed)).State; got != StatePending {
		t.Errorf("a failed forward run resolved to %s, want %s", got, StatePending)
		t.Log("MarkFailed records a run that failed without changing the schema, so the next")
		t.Log("up may proceed. Reading it as applied would skip the migration forever.")
	}
}

func TestADownThatAppliedMakesTheMigrationPendingAgain(t *testing.T) {
	users := file(30, "add_users", "-- users")

	got := resolve(t, users, up(users, 1, Applied), down(users, 2, Applied)).State
	if got != StatePending {
		t.Errorf("a migration that was applied and rolled back resolved to %s, want %s", got,
			StatePending)
	}
}

func TestAFailedDownLeavesTheMigrationAppliedBecauseTheRollbackChangedNothing(t *testing.T) {
	users := file(30, "add_users", "-- users")

	got := resolve(t, users, up(users, 1, Applied), down(users, 2, Failed)).State
	if got != StateApplied {
		t.Errorf("a migration whose rollback failed resolved to %s, want %s", got, StateApplied)
		t.Log("A failed run left the schema unchanged, so a rollback that failed leaves the")
		t.Log("migration exactly as applied as it was. Reading it as pending would re-run it.")
	}
}

func TestADirtyRunIsItsOwnStateAndIsNeitherAppliedNorPending(t *testing.T) {
	users := file(30, "add_users", "-- users")

	tests := []struct {
		name string
		runs []Record
	}{
		{name: "a forward run stopped partway", runs: []Record{up(users, 1, Dirty)}},
		{
			name: "a rollback stopped partway",
			runs: []Record{up(users, 1, Applied), down(users, 2, Dirty)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve(t, users, tt.runs...).State; got != StateDirty {
				t.Errorf("resolved to %s, want %s", got, StateDirty)
				t.Log("The schema sits between states. Applied would make up skip statements")
				t.Log("that never ran; pending would make it replay ones that already did and")
				t.Log("would discard the last_statement M9-T6 resumes from.")
			}
		})
	}
}

func TestARunThatCompletedResolvesADirtyOne(t *testing.T) {
	users := file(30, "add_users", "-- users")

	tests := []struct {
		name string
		runs []Record
		want State
	}{
		{
			name: "repaired and applied",
			runs: []Record{up(users, 1, Dirty), up(users, 2, Applied)},
			want: StateApplied,
		},
		{
			name: "unwound by hand and rolled back",
			runs: []Record{up(users, 1, Dirty), down(users, 2, Applied)},
			want: StatePending,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolve(t, users, tt.runs...).State; got != tt.want {
				t.Errorf("resolved to %s, want %s", got, tt.want)
				t.Log("M4-T9 requires the next up to refuse until a dirty run is explicitly")
				t.Log("resolved, and resolving it ends in a recorded run. A dirty state that")
				t.Log("nothing could clear would wedge the migration permanently.")
			}
		})
	}
}

func TestTheStateIsReplayedFromEveryRunRatherThanReadFromTheLast(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{up(users, 1, Applied), down(users, 2, Applied), down(users, 3, Failed)}

	if got := resolve(t, users, runs...).State; got != StatePending {
		t.Errorf("resolved to %s, want %s", got, StatePending)
		t.Log("Reading the last row alone gives applied here, because a failed rollback")
		t.Log("leaves a migration applied. That rule is only true if it WAS applied, and")
		t.Log("this one had already been rolled back. Only a replay of the whole log knows.")
	}
}

func TestAMigrationRolledBackAndReAppliedIsApplied(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{up(users, 1, Applied), down(users, 2, Applied), up(users, 3, Applied)}

	if got := resolve(t, users, runs...).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
	}
}

func TestAFirstRunThatFailedIsNotTheAnswer(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{up(users, 1, Failed), up(users, 2, Applied)}

	if got := resolve(t, users, runs...).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
		t.Log("A retry that succeeded is the answer. Reading the first row would report a")
		t.Log("migration as pending that the database has been carrying since batch 2.")
	}
}

func TestARetryThatFailedDoesNotUnApplyTheMigration(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{up(users, 1, Applied), up(users, 2, Failed)}

	if got := resolve(t, users, runs...).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
	}
}

func TestRunsAreFoldedInBatchAndOrdinalOrderHoweverTheyArrive(t *testing.T) {
	users := file(30, "add_users", "-- users")

	reversed := []Record{
		runAt(users, 2, 1, ir.Down, Applied),
		runAt(users, 1, 1, ir.Up, Applied),
	}

	if got := resolve(t, users, reversed...).State; got != StatePending {
		t.Errorf("resolved to %s, want %s", got, StatePending)
		t.Log("Folded in the order they were handed over, the rollback lands first and the")
		t.Log("apply second, which reports applied for a migration that was rolled back.")
	}
}

func TestAModifiedAppliedMigrationIsDrifted(t *testing.T) {
	users := file(30, "add_users", "-- users")
	rewritten := edited(users, "-- users, with a column nobody applied")

	if got := resolve(t, rewritten, up(users, 1, Applied)).State; got != StateDrifted {
		t.Errorf("an applied migration whose source changed resolved to %s, want %s", got,
			StateDrifted)
	}
}

func TestDriftIsDecidedOnTheSourceHalfAlone(t *testing.T) {
	users := file(30, "add_users", "-- users")

	rec := up(users, 1, Applied)
	rec.Checksum.IR = checksum.OfSource([]byte("a different emitted IR"))

	if got := resolve(t, users, rec).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
		t.Log("Migrations may branch on live query results, so re-planning against")
		t.Log("a changed database legitimately emits different operations. Comparing the IR")
		t.Log("half would report exit code 4 for a failure the operator did not cause.")
	}
}

func TestDriftIsMeasuredAgainstTheRunThatLeftTheMigrationApplied(t *testing.T) {
	users := file(30, "add_users", "-- users")
	rewritten := edited(users, "-- users, rewritten before the second apply")

	runs := []Record{
		up(users, 1, Applied),
		down(users, 2, Applied),
		up(rewritten, 3, Applied),
	}

	if got := resolve(t, rewritten, runs...).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
		t.Log("Editing a migration the database no longer carries is legal, and re-applying")
		t.Log("it records the new hash. Drift compares against that run, not the first one.")
	}
}

func TestARolledBackMigrationThatWasThenEditedIsPendingRatherThanDrifted(t *testing.T) {
	users := file(30, "add_users", "-- users")
	rewritten := edited(users, "-- users, rewritten after the rollback")

	runs := []Record{up(users, 1, Applied), down(users, 2, Applied)}

	if got := resolve(t, rewritten, runs...).State; got != StatePending {
		t.Errorf("resolved to %s, want %s", got, StatePending)
		t.Log("Invariant #6 makes APPLIED migrations immutable. One the database no longer")
		t.Log("carries is free to be edited, and reporting exit code 4 for it would be an")
		t.Log("alarm with no repair.")
	}
}

func TestARecordedChecksumOfAllZeroesReportsDriftRatherThanAgreement(t *testing.T) {
	users := file(30, "add_users", "-- users")

	rec := up(users, 1, Applied)
	rec.Checksum = checksum.Checksum{}

	if got := resolve(t, users, rec).State; got != StateDrifted {
		t.Errorf("resolved to %s, want %s", got, StateDrifted)
		t.Log("checksum.Parse accepts 128 zeroes, so a row holding them decodes cleanly and")
		t.Log("reaches this fold. The comparison must be a plain one; skipping a digest that")
		t.Log("reports Zero would let a row that describes nothing pass as agreement.")
	}
}

func TestAStatusCarriesBothTheRecordedAndTheRecomputedDigest(t *testing.T) {
	users := file(30, "add_users", "-- users")
	rewritten := edited(users, "-- users, edited")

	status := resolve(t, rewritten, up(users, 1, Applied))

	if status.Source != rewritten.source {
		t.Error("the status does not carry what the file hashes to now")
	}

	if status.Recorded.Checksum.Source != users.source {
		t.Error("the status does not carry what the run recorded")
		t.Log("M4-T7 shows both checksums in full and names which half moved, so both have")
		t.Log("to be on the status rather than recomputed by whoever renders it.")
	}
}

func TestAVersionTheDatabaseStillCarriesAndNoFileDescribesIsMissing(t *testing.T) {
	users := file(30, "add_users", "-- users")

	statuses := reconcile(t, nil, up(users, 1, Applied))
	if len(statuses) != 1 || statuses[0].State != StateMissing {
		t.Log("The database holds schema this repository can neither describe nor roll back.")
		t.Fatalf("reconciling a deleted applied migration produced %v, want one missing", statuses)
	}

	if statuses[0].Version != users.migration.Version {
		t.Errorf("the missing status names version %q, want %q", statuses[0].Version,
			users.migration.Version)
	}
}

func TestAVersionWithSeveralRunsAndNoFileIsReportedOnce(t *testing.T) {
	users := file(30, "add_users", "-- users")

	statuses := reconcile(t, nil,
		up(users, 1, Applied), down(users, 2, Applied), up(users, 3, Applied))

	if len(statuses) != 1 {
		t.Log("A version is reported once however many times it ran. Collecting the recorded")
		t.Log("versions by walking the runs is what keeps the order deterministic, and it is")
		t.Log("also what makes one line of dropping repeats load-bearing.")
		t.Fatalf("a deleted migration with three runs produced %d statuses, want 1", len(statuses))
	}

	if statuses[0].State != StateMissing {
		t.Errorf("resolved to %s, want %s", statuses[0].State, StateMissing)
	}
}

func TestAVersionRolledBackAndThenDeletedIsNotReportedAtAll(t *testing.T) {
	users := file(30, "add_users", "-- users")

	statuses := reconcile(t, nil, up(users, 1, Applied), down(users, 2, Applied))
	if len(statuses) != 0 {
		t.Errorf("a rolled-back, deleted migration reported %v, want nothing", statuses)
		t.Log("It is fully undone and its file's absence is expected. migr history still")
		t.Log("prints its rows; missing is the alarm for schema nobody can unwind, and")
		t.Log("diluting it with tombstones is how an operator learns to ignore it.")
	}
}

func TestAVersionThatOnlyEverFailedAndHasNoFileIsNotReportedAtAll(t *testing.T) {
	users := file(30, "add_users", "-- users")

	if statuses := reconcile(t, nil, up(users, 1, Failed)); len(statuses) != 0 {
		t.Errorf("a never-applied, deleted migration reported %v, want nothing", statuses)
	}
}

func TestADirtyVersionWithNoFileIsStillReportedAsDirty(t *testing.T) {
	users := file(30, "add_users", "-- users")

	statuses := reconcile(t, nil, up(users, 1, Dirty))
	if len(statuses) != 1 || statuses[0].State != StateDirty {
		t.Log("The schema being between states dictates the next action, not the file being")
		t.Log("gone. Reporting it as missing understates the worst row in the table.")
		t.Fatalf("a dirty, deleted migration produced %v, want one dirty", statuses)
	}

	if statuses[0].Migration != (migration.Migration{}) {
		t.Errorf("a status with no file carries %q, want a zero migration",
			statuses[0].Migration.Filename())
		t.Log("Migration is how a renderer tells a status with a file from one without, so")
		t.Log("it has to stay zero rather than be filled in from the recorded name.")
	}
}

func TestAMissingHistoryTableIsNotADatabaseWithNothingInIt(t *testing.T) {
	files := []File{
		file(30, "add_users", "-- users"),
		file(31, "add_orders", "-- orders"),
		file(32, "add_items", "-- items"),
	}

	statuses, ok := Reconcile(files, Log{}).Resolved()
	if ok {
		t.Error("reconciling against an absent log reported a history table")
	}

	if len(statuses) != 0 {
		t.Errorf("an absent history table produced %d statuses, want none", len(statuses))
	}

	for _, status := range statuses {
		if status.State == StatePending {
			t.Errorf("%s reported pending against a database with no history table",
				status.Version)
			t.Log("This is the failure M3-T6 found: read as no runs yet, a fully migrated")
			t.Log("production database looks virgin and the next up re-runs everything.")
		}
	}
}

func TestAHistoryTableWithNoRunsResolvesEveryMigrationToPending(t *testing.T) {
	files := []File{file(30, "add_users", "-- users"), file(31, "add_orders", "-- orders")}

	statuses, ok := Reconcile(files, NewLog(nil)).Resolved()
	if !ok {
		t.Fatal("a log built from no runs reported no history table")
	}

	for _, status := range statuses {
		if status.State != StatePending {
			t.Errorf("%s resolved to %s against an empty history, want %s", status.Version,
				status.State, StatePending)
		}
	}

	if len(statuses) != len(files) {
		t.Errorf("an empty history produced %d statuses, want %d", len(statuses), len(files))
		t.Log("This is the contrast case: a fresh database after Ensure has to look different")
		t.Log("from one with no table at all, and it does — present, with every file pending.")
	}
}

func TestStatusesKeepTheOrderTheMigrationsWereGivenIn(t *testing.T) {
	late := file(32, "add_items", "-- items")
	early := file(30, "add_users", "-- users")
	middle := file(31, "add_orders", "-- orders")

	statuses := reconcile(t, []File{late, early, middle})

	want := []string{late.migration.Version, early.migration.Version, middle.migration.Version}
	if got := versionsOf(statuses); !slices.Equal(got, want) {
		t.Errorf("the statuses came back as %v, want %v", got, want)
		t.Log("Order overlays declared dependencies on timestamp order, so the sequence a")
		t.Log("caller hands over is what up will run. Re-sorting by version here would show")
		t.Log("an order migr will not follow, and would discard the overlay (M4-T2).")
	}
}

func TestVersionsNoFileCarriesComeAfterTheMigrationsInVersionOrder(t *testing.T) {
	kept := file(31, "add_orders", "-- orders")
	deletedLate := file(40, "add_items", "-- items")
	deletedEarly := file(30, "add_users", "-- users")

	statuses := reconcile(t, []File{kept},
		up(deletedLate, 1, Applied),
		up(kept, 2, Applied),
		up(deletedEarly, 3, Applied),
	)

	want := []string{
		kept.migration.Version,
		deletedEarly.migration.Version,
		deletedLate.migration.Version,
	}

	if got := versionsOf(statuses); !slices.Equal(got, want) {
		t.Errorf("the statuses came back as %v, want %v", got, want)
		t.Log("Nothing will run a version no file describes, so it has no place in a run")
		t.Log("order and is appended instead. They are sorted among themselves because the")
		t.Log("file sequence is not version-sorted, so there is no position to merge into.")
	}
}

func TestTheReportIsTheSameEveryTimeItIsProduced(t *testing.T) {
	kept := file(30, "add_users", "-- users")

	runs := []Record{up(kept, 1, Applied)}
	for minute := 40; minute < 52; minute++ {
		runs = append(runs, up(file(minute, "deleted", "-- gone"), int64(minute), Applied))
	}

	first := versionsOf(reconcile(t, []File{kept}, runs...))

	for range 32 {
		if got := versionsOf(reconcile(t, []File{kept}, runs...)); !slices.Equal(got, first) {
			t.Log("Invariant #8: nothing that decides an order may depend on map iteration.")
			t.Fatalf("a second reconciliation of one input produced %v, want %v", got, first)
		}
	}
}

func TestReconcileDoesNotDisturbWhatItWasGiven(t *testing.T) {
	files := []File{file(32, "add_items", "-- items"), file(30, "add_users", "-- users")}
	before := slices.Clone(files)

	Reconcile(files, NewLog([]Record{up(files[1], 1, Applied)}))

	if !reflect.DeepEqual(files, before) {
		t.Error("Reconcile reordered the caller's own slice")
	}
}

func TestResolvedIsACopy(t *testing.T) {
	users := file(30, "add_users", "-- users")
	report := Reconcile([]File{users}, NewLog(nil))

	statuses, _ := report.Resolved()
	statuses[0].State = StateApplied

	again, _ := report.Resolved()
	if again[0].State != StatePending {
		t.Error("Resolved handed out the report's own slice, so a caller rewrote a state")
	}
}

func TestADirtyMigrationWhoseFileAlsoChangedIsReportedDirtyRatherThanDrifted(t *testing.T) {
	users := file(30, "add_users", "-- users")
	rewritten := edited(users, "-- users, edited after the run stopped partway")

	if got := resolve(t, rewritten, up(users, 1, Dirty)).State; got != StateDirty {
		t.Errorf("resolved to %s, want %s", got, StateDirty)
		t.Log("Dirty is the condition that blocks everything, and its repair needs the")
		t.Log("recorded row. The status carries both digests, so whoever renders it can")
		t.Log("note the drift alongside without the state having to choose it.")
	}
}

func TestEveryMigrationResolvesToExactlyOneState(t *testing.T) {
	users := file(30, "add_users", "-- users")
	orders := file(31, "add_orders", "-- orders")
	items := file(32, "add_items", "-- items")
	prices := file(33, "add_prices", "-- prices")
	carts := file(34, "add_carts", "-- carts")

	statuses := reconcile(t,
		[]File{users, orders, items, edited(prices, "-- prices, edited")},
		up(users, 1, Applied),
		up(items, 2, Dirty),
		up(prices, 3, Applied),
		up(carts, 4, Applied),
	)

	want := []State{StateApplied, StatePending, StateDirty, StateDrifted, StateMissing}

	got := make([]State, len(statuses))
	for i, status := range statuses {
		got[i] = status.State

		if !status.State.Valid() {
			t.Errorf("%s resolved to %q, which names no state", status.Version, status.State)
		}
	}

	if !slices.Equal(got, want) {
		t.Errorf("the mixed fixture resolved to %v, want %v", got, want)
	}
}

func TestTwoFilesClaimingOneVersionBothResolve(t *testing.T) {
	users := file(30, "add_users", "-- users")
	twin := NewFile(at(30, "add_users_again"), []byte("-- users"))

	statuses := reconcile(t, []File{users, twin}, up(users, 1, Applied))
	if len(statuses) != 2 {
		t.Log("Discover refuses this, so Reconcile needs no error to restate the guarantee.")
		t.Log("Dropping one silently would be worse than showing both.")
		t.Fatalf("two files claiming one version produced %d statuses, want 2", len(statuses))
	}

	for _, status := range statuses {
		if status.State != StateApplied {
			t.Errorf("%s resolved to %s, want %s", status.Migration.Filename(), status.State,
				StateApplied)
		}
	}
}

func TestAMigrationRenamedButNotEditedIsStillApplied(t *testing.T) {
	users := file(30, "add_users", "-- users")
	renamed := NewFile(at(30, "create_users"), []byte("-- users"))

	if got := resolve(t, renamed, up(users, 1, Applied)).State; got != StateApplied {
		t.Errorf("resolved to %s, want %s", got, StateApplied)
		t.Log("The join is by version and the bytes did not change. The recorded")
		t.Log("name now disagrees with the filename, and both are right about what they")
		t.Log("describe: status renders the file's, history renders the run's.")
	}
}

func TestAVersionThatNeverRanAndHasNoFileIsNotReachable(t *testing.T) {
	if statuses := reconcile(t, nil); len(statuses) != 0 {
		t.Errorf("reconciling nothing against nothing produced %v, want nothing", statuses)
	}
}
