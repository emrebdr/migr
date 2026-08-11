package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// carried stages a history in which the first n migrations were applied, in the order given.
func carried(t *testing.T, conn *recorder, versions ...int) {
	t.Helper()

	conn.store.table = true

	var ordinal int32

	for _, n := range versions {
		ordinal++
		conn.store.runs = append(conn.store.runs, ran(t, n, 1, ordinal))
	}
}

func TestARollbackRunsTheReverseOperationsInReverseOrder(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "abc")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"begin", "exec c2", "exec b1", "exec a0", "record " + versionAt(0), "commit",
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
	}

	t.Log("Undone forwards, the first statement would drop something a later one still depends")
	t.Log("on. ir.ReverseAll inverts every operation and reverses their order, and this is the")
	t.Log("first thing in the repository that calls it.")
}

func TestTheAppliedMigrationsAreUndoneNewestFirst(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1, 2)

	opts, _ := staged(t, "a", "b", "c")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	var order []string
	for _, run := range result.Runs {
		order = append(order, run.Migration.Version)
	}

	want := []string{versionAt(2), versionAt(1), versionAt(0)}
	if !slices.Equal(order, want) {
		t.Fatalf("the rollback undid\n  %v\nwant\n  %v", order, want)
	}

	t.Log("A migration is built on the ones before it, so undoing the oldest first would remove")
	t.Log("what the newer ones still stand on.")
}

func TestTheUndoOrderIsTheOrderTheRunsHappenedRatherThanTheOrderTheFilesAreIn(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 1, 1, 1), ran(t, 0, 1, 2)}

	opts, _ := staged(t, "a", "b")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []string{versionAt(0), versionAt(1)}
	got := []string{result.Runs[0].Migration.Version, result.Runs[1].Migration.Version}

	if !slices.Equal(got, want) {
		t.Fatalf("the rollback undid\n  %v\nwant the reverse of the order the runs happened\n  %v",
			got, want)
	}

	t.Log("The later-timestamped migration ran first, which is what a declared dependency pointing")
	t.Log("backwards in time does (ADR 0018). Reversing the report or sorting by version would")
	t.Log("undo the migration the other one was built on while that one is still applied.")
}

func TestTheWholeQueueIsInvertedBeforeAnyOfItRuns(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, src := staged(t, "a", "b")
	src.session = conn

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	firstRun := slices.Index(conn.calls, "begin")
	if firstRun < 0 {
		t.Fatalf("the session was asked for %v, want a transaction", conn.calls)
	}

	for at, call := range conn.calls {
		if strings.HasPrefix(call, "emit ") && at > firstRun {
			t.Fatalf("the session was asked for %v, want every emit before the first run",
				conn.calls)
		}
	}

	t.Log("An irreversible migration is a property of the migration, not of the run, so it is")
	t.Log("knowable before anything happens. Found lazily, a migration with no inverse at")
	t.Log("position three would be found after positions five and four had been undone.")
}

func TestAnIrreversibleMigrationHaltsTheRollbackBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, src := staged(t, "a", "b")
	src.ops[versionAt(0)] = ir.Operations{addIndex("a0"), unrecorded("gone")}

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ir.ErrIrreversible) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ir.ErrIrreversible", err)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("The irreversible migration is the older of the two, so a lazy rollback would have")
	t.Log("undone the newer one first and then refused -- leaving the database carrying one")
	t.Log("migration whose predecessor is gone, which is a state nothing describes.")
}

func TestTheIrreversibleRefusalNamesTheMigrationAndTheOperationWithNoInverse(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{addIndex("a0"), unrecorded("gone")}

	_, err := Rollback(t.Context(), conn, opts)

	var failure *MigrationError
	if !errors.As(err, &failure) {
		t.Fatalf("Rollback returned %v, which is not a *MigrationError", err)
	}

	message := failure.Error()

	for _, want := range []string{
		migrationAt(t, 0).Filename(),
		ir.KindDropIndex.String(),
		"supply \"from\"",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal reads %q and does not name %q", message, want)
		}
	}

	t.Log("Invariant #9 wants the migration and the offending operation. ReverseAll names the")
	t.Log("operation and its position, IrreversibleError names the field that would fix it, and")
	t.Log("MigrationError names the file -- none of the three knows what the other two do.")
}

func TestAnIrreversibleRollbackWritesNoRowAndAllocatesNoBatch(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")
	src.ops[versionAt(0)] = ir.Operations{unrecorded("gone")}

	result, err := Rollback(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Rollback reported success for a migration nothing can undo")
	}

	if len(conn.store.runs) != 1 {
		t.Errorf("the history holds %v, want only the run that applied it", conn.store.runs)
	}

	if result.Batch != 0 || slices.Contains(conn.calls, "latest") {
		t.Errorf("the session was asked for %v and reports batch %d, want no batch at all",
			conn.calls, result.Batch)
	}

	var failure *MigrationError
	if errors.As(err, &failure) && failure.Batch != 0 {
		t.Errorf("the failure names batch %d, want zero: it preceded one", failure.Batch)
	}

	t.Log("A batch is allocated only once the whole queue is known to be runnable, so a refused")
	t.Log("rollback leaves the numbering exactly where it found it.")
}

func TestAMigrationThatAuthorsNoDownIsAskedForItsUpAndInverted(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []ir.Direction{ir.Down, ir.Up}
	if !slices.Equal(src.directions, want) {
		t.Fatalf("the source was asked for %v, want %v", src.directions, want)
	}

	t.Log("The source is asked first, because a down its author wrote beats one this framework")
	t.Log("guessed at. Deriving here rather than in each SDK is what stops three SDKs reaching")
	t.Log("three different inverses for one migration.")
}

func TestAnAuthoredDownIsPreferredToADerivedOne(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")
	src.down[versionAt(0)] = ir.Operations{addIndex("z9")}

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if !slices.Equal(src.directions, []ir.Direction{ir.Down}) {
		t.Fatalf("the source was asked for %v, want the down alone", src.directions)
	}

	if !slices.Contains(conn.calls, "exec z9") {
		t.Fatalf("the session was asked for %v, want the authored down rather than an inverse",
			conn.calls)
	}

	t.Log("Invariant #10 says a migration has a Down. Deriving one over the top of an authored")
	t.Log("one would run something its author did not write and could not have reviewed.")
}

func TestAPendingMigrationIsSkippedRatherThanRolledBack(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a", "b")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(result.Skipped) != 1 || result.Skipped[0].State != history.StatePending {
		t.Fatalf("the result reports %v skipped, want the one migration nothing applied",
			result.Skipped)
	}

	if slices.Contains(src.asked, versionAt(1)) {
		t.Errorf("the source was asked about %v, which includes a migration nothing applied",
			src.asked)
	}

	t.Log("There is nothing to undo for a migration the database never carried, and asking it")
	t.Log("how to undo itself would run a runtime process for an answer nothing reads.")
}

func TestAVersionOnlyTheDatabaseCarriesRefusesTheWholeRollback(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1), ran(t, 9, 1, 2)}

	opts, _ := staged(t, "a")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrMissing", err)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("migr up skips such a version because it has nothing to run for it. A rollback runs")
	t.Log("newest first, so this one sits in front of every migration older than it and stepping")
	t.Log("around it would undo them while it stays.")
}

func TestARollbackWithNoMigrationsOnDiskRefusesRatherThanReportingNothingToUndo(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, _ := staged(t, "a", "b")
	opts.Migrations = nil

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrMissing", err)
	}

	t.Log("This is migr down run against a misconfigured migrations.dir, and it is the case that")
	t.Log("decides the rule: under a skip rule it reports nothing to undo against a fully")
	t.Log("migrated database, and the operator concludes the tool works.")
}

func TestADriftedMigrationRefusesTheWholeRollback(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	was := ran(t, 0, 1, 1)
	was.Checksum.Source = checksum.OfSource([]byte("what the file used to hold"))
	conn.store.runs = []history.Record{was}

	opts, _ := staged(t, "a")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrDrifted", err)
	}

	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("errors.As found no *DriftError in %T", err)
	}

	if drift.Direction != ir.Down {
		t.Errorf("the refusal reports direction %q, want the one the run was performing",
			drift.Direction)
	}

	if !strings.Contains(err.Error(), "stronger reason") {
		t.Errorf("the refusal does not say why a rollback's is the stronger one: %v", err)
	}

	t.Log("Drift refuses a rollback for a stronger reason than it refuses an apply: the undo")
	t.Log("about to run would be authored by bytes that are not the ones that ran.")
}

func TestADirtyMigrationRefusesTheWholeRollback(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	stopped := ran(t, 0, 1, 1)
	stopped.Outcome = history.Dirty
	stopped.LastStatement = 2
	stopped.Error = "the server refused this statement"
	conn.store.runs = []history.Record{stopped}

	opts, _ := staged(t, "a")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrDirty", err)
	}

	t.Log("A dirty run left the schema between states, so nothing can say what undoing it means.")
}

func TestARollbackWithNothingAppliedRunsNothingAndAllocatesNoBatch(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, src := staged(t, "a", "b")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(result.Runs) != 0 || result.Batch != 0 {
		t.Errorf("the result reports %d runs under batch %d, want none", len(result.Runs),
			result.Batch)
	}

	if len(src.asked) != 0 || slices.Contains(conn.calls, "latest") {
		t.Errorf("the session was asked for %v and the source for %v, with nothing to undo",
			conn.calls, src.asked)
	}

	t.Log("migr down against a database carrying nothing must not open a transaction, allocate a")
	t.Log("batch, or ask a runtime what any migration undoes.")
}

func TestARollbackRecordsADownRunUnderANewBatchNumberedFromOne(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, _ := staged(t, "a", "b")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if result.Batch != 2 {
		t.Fatalf("the rollback recorded under batch %d, want 2", result.Batch)
	}

	written := conn.store.runs[2:]
	if len(written) != 2 {
		t.Fatalf("the rollback wrote %d rows, want one per migration", len(written))
	}

	var ordinal int32

	for i, rec := range written {
		ordinal++

		if rec.Direction != ir.Down || rec.Outcome != history.Applied {
			t.Errorf("row %d is %s %s, want an applied down", i, rec.Direction, rec.Outcome)
		}

		if rec.Batch != 2 || rec.Ordinal != ordinal {
			t.Errorf("row %d is (batch %d, ordinal %d), want (2, %d)",
				i, rec.Batch, rec.Ordinal, ordinal)
		}
	}

	if written[0].Version != versionAt(1) {
		t.Errorf("the first row undoes %s, want the newest migration", written[0].Version)
	}

	t.Log("A rollback is its own invocation, so it takes its own batch and numbers its runs from")
	t.Log("one. Reusing the batch it undoes would collide on (batch, ordinal) and would replace")
	t.Log("the evidence that the migration was ever applied.")
}

func TestTheRecordedDownCarriesTheChecksumOfTheOperationsItActuallyRan(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "abc")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	inverse, err := ir.ReverseAll(operations("abc"))
	if err != nil {
		t.Fatalf("ReverseAll: %v", err)
	}

	emitted, err := checksum.OfOperations(inverse)
	if err != nil {
		t.Fatalf("hashing the inverse: %v", err)
	}

	want := checksum.Checksum{Source: checksum.OfSource([]byte(bodyAt(0))), IR: emitted}
	if got := conn.store.runs[1].Checksum; got != want {
		t.Fatalf("the down row records %s, want %s", got, want)
	}

	t.Log("The IR half records what one run emitted (ADR 0009), and this run emitted the inverse.")
	t.Log("Copying the up row's digest would be a row that lies about what it did -- so a")
	t.Log("version's rows now carry two IR digests, and nothing may compare them to each other.")
}

func TestARollbackThatFailsLeavesTheMigrationsItHadNotReachedApplied(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, _ := staged(t, "a", "b")
	conn.fail = "b0"

	result, err := Rollback(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Rollback reported success for a run whose first statement failed")
	}

	if len(result.Runs) != 0 {
		t.Errorf("the result claims %d runs, want none", len(result.Runs))
	}

	report := history.Reconcile(opts.Migrations, history.NewLog(conn.store.runs))

	statuses, present := report.Resolved()
	if !present {
		t.Fatal("the staged history reconciled to nothing at all")
	}

	for _, st := range statuses {
		if st.State != history.StateApplied {
			t.Errorf("%s reconciles to %s after a rollback that undid nothing, want applied",
				st.Version, st.State)
		}
	}

	t.Log("A failed down leaves the fold unchanged, so the migration it failed on is still")
	t.Log("applied and the older one was never reached. That is what internal/history has")
	t.Log("documented since M4-T3, and this is the first thing that produces it.")
}

func TestAMigrationRolledBackIsPendingAndAppliesAgainFromItsUp(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	src.asked = nil
	src.directions = nil

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply after the rollback: %v", err)
	}

	if len(result.Runs) != 1 || result.Runs[0].Migration.Version != versionAt(0) {
		t.Fatalf("the re-apply ran %v, want the rolled-back migration", result.Runs)
	}

	if !slices.Equal(src.directions, []ir.Direction{ir.Up}) {
		t.Fatalf("the source was asked for %v, want the up alone", src.directions)
	}

	t.Log("Rolling forward again runs the original Up, not the reverse of the Down that undid it")
	t.Log("(docs/ir.md 8.4). The fold reaches pending because a completed down leaves the")
	t.Log("migration uncarried, anchored on the down row rather than on the up.")
}

func TestARollbackIsRefusedForWhatItWasNotGiven(t *testing.T) {
	cases := []struct {
		field    string
		strip    func(*Options)
		sentinel error
	}{
		{"planner", func(o *Options) { o.Planner = plan.Planner{} }, plan.ErrNoCompiler},
		{"source", func(o *Options) { o.Source = nil }, ErrNoSource},
		{"clock", func(o *Options) { o.Clock = nil }, ErrNoClock},
		{"runtime", func(o *Options) { o.Runtime = "" }, ErrNoRuntime},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			conn := newRecorder(t, true)
			carried(t, conn, 0)

			opts, _ := staged(t, "a")
			tc.strip(&opts)

			_, err := Rollback(t.Context(), conn, opts)
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("a rollback with no %s returned %v, want %v", tc.field, err, tc.sentinel)
			}

			if len(conn.calls) != 0 {
				t.Fatalf("the session was asked for %v before the refusal", conn.calls)
			}
		})
	}

	t.Log("Both directions refuse the same things in the same place, because both are built from")
	t.Log("one Options and one session preamble.")
}

func TestNothingIsAskedOfTheDatabaseWhenTheRollbackLockCannotBeTaken(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)
	conn.lockErr = errors.New("somebody else holds it")

	opts, _ := staged(t, "a")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, conn.lockErr) {
		t.Fatalf("Rollback returned %v, want what the acquire reported", err)
	}

	if len(conn.calls) != 0 {
		t.Fatalf("the session was asked for %v after the lock could not be taken", conn.calls)
	}
}

func TestARollbackQueueIsOrderedByTheRunsThatAppliedIt(t *testing.T) {
	cases := []struct {
		name string
		runs []history.Record
		want []int32
	}{
		{
			name: "within one batch",
			runs: []history.Record{{Batch: 1, Ordinal: 1}, {Batch: 1, Ordinal: 2}},
			want: []int32{2, 1},
		},
		{
			name: "across batches",
			runs: []history.Record{{Batch: 2, Ordinal: 1}, {Batch: 1, Ordinal: 9}},
			want: []int32{1, 9},
		},
		{
			name: "a batch beats an ordinal",
			runs: []history.Record{{Batch: 1, Ordinal: 5}, {Batch: 3, Ordinal: 1}},
			want: []int32{1, 5},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			queue := make([]queued, 0, len(tc.runs))
			for _, rec := range tc.runs {
				queue = append(queue, queued{recorded: rec})
			}

			var got []int32
			for _, item := range newestFirst(queue) {
				got = append(got, item.recorded.Ordinal)
			}

			if !slices.Equal(got, tc.want) {
				t.Fatalf("ordered to %v, want %v", got, tc.want)
			}
		})
	}

	t.Log("This is history.byRun reflected, and (batch, ordinal) is the history table's primary")
	t.Log("key, so no two runs can tie and the order is total.")
}

func TestARolledBackVersionWithNoFileIsNotReportedAsMissing(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 9, 1, 1), undone(t, 9, 2, 1)}

	opts, _ := staged(t)

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback returned %v, want no complaint about a version nothing carries", err)
	}

	if len(result.Runs) != 0 || len(result.Skipped) != 0 {
		t.Fatalf("the result reports %v and %v, want nothing at all", result.Runs, result.Skipped)
	}

	t.Log("A migration applied, rolled back and then deleted is fully undone, so reconciliation")
	t.Log("emits no status for it and there is nothing here to refuse. The missing refusal is")
	t.Log("for schema the database still carries, not for every version it has ever seen.")
}

func TestAFailedRollbackRecordsADownRunAfterItsTransactionRolledBack(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "a")
	conn.fail = "a0"

	if _, err := Rollback(t.Context(), conn, opts); err == nil {
		t.Fatal("Rollback reported success for a run whose only statement failed")
	}

	rollback := slices.Index(conn.calls, "rollback")
	marked := slices.Index(conn.calls, "mark-failed-outside-any-transaction "+versionAt(0))

	if rollback < 0 || marked < rollback {
		t.Fatalf("the session was asked for %v, want the failure recorded after the rollback",
			conn.calls)
	}

	failed := conn.store.runs[len(conn.store.runs)-1]
	if failed.Direction != ir.Down || failed.Outcome != history.Failed {
		t.Fatalf("the last row is %s %s, want a failed down", failed.Direction, failed.Outcome)
	}

	t.Log("migr history is what an operator reads after an incident, and a rollback that could")
	t.Log("not complete is exactly the event worth looking up.")
}

func TestARollbackNamesTheMigrationAndTheOperationAStatementCameFrom(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "abc")
	conn.fail = "b1"

	_, err := Rollback(t.Context(), conn, opts)

	var failure *MigrationError
	if !errors.As(err, &failure) {
		t.Fatalf("Rollback returned %v, which is not a *MigrationError", err)
	}

	if failure.Migration.Version != versionAt(0) {
		t.Errorf("the failure names %s, want %s", failure.Migration.Version, versionAt(0))
	}

	if failure.Operation == nil || failure.Operation.Kind() != ir.KindDropIndex {
		t.Errorf("the failure names %v, want the inverse operation that failed", failure.Operation)
	}

	t.Log("The plan compiled for a rollback holds the inverse operations, so a position resolves")
	t.Log("to what actually ran rather than to what the migration originally declared.")
}

func TestARollbackAsksTheSourceOnlyAboutWhatItWillUndo(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 1)

	opts, src := staged(t, "a", "b", "c")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if !slices.Equal(src.asked, []string{versionAt(1), versionAt(1)}) {
		t.Fatalf("the source was asked about %v, want only the applied migration", src.asked)
	}

	t.Log("Asked twice for one migration: once for the down it authored none of, once for the up")
	t.Log("the inverse is derived from. The two migrations nothing applied are never asked.")
}

func TestARollbackErrorNamesTheMigrationEvenWhenTheSourceIsWhatFailed(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")

	broken := errors.New("this migration will not load")
	src.fail[versionAt(0)] = broken

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, broken) {
		t.Fatalf("Rollback returned %v, want the failure the source reported", err)
	}

	var failure *MigrationError
	if !errors.As(err, &failure) || failure.Migration.Version != versionAt(0) {
		t.Fatalf("Rollback returned %v, which does not name the migration that would not load", err)
	}

	if !strings.Contains(err.Error(), "undoes") {
		t.Errorf("the message %q does not say which question the source was asked", err.Error())
	}

	if len(conn.store.runs) != 1 {
		t.Errorf("the history holds %v, want no row for a run that never produced operations",
			conn.store.runs)
	}
}

func TestASourceThatCannotSayWhatAMigrationDoesCannotHaveAnUndoDerivedFromIt(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "a")

	broken := errors.New("this migration will not load")
	src.failUp[versionAt(0)] = broken

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, broken) {
		t.Fatalf("Rollback returned %v, want the failure the source reported", err)
	}

	if !strings.Contains(err.Error(), "derive") {
		t.Errorf("the message %q does not say the up was asked for in order to invert it",
			err.Error())
	}

	if slices.Contains(conn.calls, "begin") {
		t.Fatalf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("The source reports no authored down and then cannot say what the migration does either,")
	t.Log("so there is nothing to invert. Passing that through as no operations would reach the")
	t.Log("empty-migration refusal instead, which names the wrong problem entirely.")
}

func TestASkippedRollbackCarriesTheMigrationItSkipped(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "a", "b")

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(result.Skipped) != 1 {
		t.Fatalf("the result reports %d skipped, want one", len(result.Skipped))
	}

	if result.Skipped[0].Migration == (migration.Migration{}) {
		t.Errorf("the skipped entry carries no migration, so a renderer cannot name the file")
	}
}
