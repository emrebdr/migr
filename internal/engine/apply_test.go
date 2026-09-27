package engine

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

func TestAMigrationRunsAsOneTransaction(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "abc")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"begin", "exec a0", "exec b1", "exec c2", "record " + versionAt(0), "commit",
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Errorf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
		t.Log("Every statement runs between the begin and the commit. One outside the boundary is")
		t.Log("a schema change a failure would not undo, which is this task's whole claim.")
	}
}

func TestEachMigrationIsRecordedBeforeItsTransactionCommits(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a", "b")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"begin", "exec a0", "record " + versionAt(0), "commit",
		"begin", "exec b0", "record " + versionAt(1), "commit",
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
	}

	t.Log("ADR 0013: the row is written inside the migration's transaction, so a crashed run")
	t.Log("leaves no row at all rather than one claiming a migration was applied that was not.")
	t.Log("A row written after the commit is a window where the schema and the history disagree.")
}

func TestMigrationsRunInTheOrderTheyWereGiven(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a", "b", "c")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var order []string
	for _, run := range result.Runs {
		order = append(order, run.Migration.Version)
	}

	want := []string{versionAt(0), versionAt(1), versionAt(2)}
	if !slices.Equal(order, want) {
		t.Fatalf("the queue ran\n  %v\nwant the order it was given\n  %v", order, want)
	}

	t.Log("migration.Order already overlays declared dependencies onto version order and hands")
	t.Log("back a total sequence (M4-T2). Re-sorting here would run an order nothing computed.")
}

func TestAFailureAbortsTheRestOfTheQueue(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, src := staged(t, "a", "b", "c")

	result, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for a queue whose first migration failed")
	}

	if len(result.Runs) != 0 {
		t.Errorf("the result claims %d runs, want none", len(result.Runs))
	}

	if !slices.Equal(src.asked, []string{versionAt(0)}) {
		t.Errorf("the source was asked about %v after the first migration failed", src.asked)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "record ") {
			t.Errorf("a run was recorded despite the queue failing at its first migration")
		}
	}

	t.Log("Continuing past a failure applies later migrations on top of a schema change that")
	t.Log("did not land, which is the state nothing downstream can describe or roll back.")
}

func TestTheMigrationsBeforeAFailureKeepTheirCommittedWork(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, _ := staged(t, "b", "a")

	result, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for a queue whose second migration failed")
	}

	if len(result.Runs) != 1 || result.Runs[0].Migration.Version != versionAt(0) {
		t.Fatalf("the result reports %v, want the one migration that did commit", result.Runs)
	}

	if len(conn.store.runs) != 2 {
		t.Fatalf("the history holds %d rows, want the applied run and the failed one",
			len(conn.store.runs))
	}

	if got := conn.store.runs[0].Outcome; got != history.Applied {
		t.Errorf("the first row is %s, want applied", got)
	}

	if got := conn.store.runs[1].Outcome; got != history.Failed {
		t.Errorf("the second row is %s, want failed", got)
	}

	t.Log("Aborting a queue means stopping, never undoing. A Result populated alongside an error")
	t.Log("is what lets an operator see which migrations did commit before the run stopped.")
}

func TestAFailurePartwayCommitsNothingAndRunsNothingFurther(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "b1"
	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply reported success for a migration whose second statement failed")
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"begin", "exec a0", "exec b1", "rollback",
		"mark-failed-outside-any-transaction " + versionAt(0),
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Errorf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
		t.Log("A failure must discard the transaction and stop. Executing the statements after it")
		t.Log("would run them in an aborted transaction, and committing would keep nothing anyway.")
	}

	if !errors.Is(err, errRefused) {
		t.Errorf("Apply returned %v, which does not unwrap to what the session reported", err)
	}
}

func TestAFailedRunIsRecordedOnlyAfterItsTransactionRolledBack(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, _ := staged(t, "a")

	if _, err := Apply(t.Context(), conn, opts); err == nil {
		t.Fatal("Apply reported success for a migration whose only statement failed")
	}

	rollback := slices.Index(conn.calls, "rollback")
	marked := slices.Index(conn.calls, "mark-failed-outside-any-transaction "+versionAt(0))

	if rollback < 0 || marked < 0 || marked < rollback {
		t.Fatalf("the session was asked for %v, want the failure recorded after the rollback",
			conn.calls)
	}

	if len(conn.store.runs) != 1 || conn.store.runs[0].Outcome != history.Failed {
		t.Fatalf("the history holds %v, want one failed run", conn.store.runs)
	}

	t.Log("ADR 0013's log records every run including failures, and migr history is what an")
	t.Log("operator reads after an incident. Written inside the transaction the row would be")
	t.Log("discarded with it, and PostgreSQL refuses every statement in an aborted one anyway.")
}

func TestAFailureNamesTheStatementThatCausedIt(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "c2"
	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)

	var failure *StatementError
	if !errors.As(err, &failure) {
		t.Fatalf("Apply returned %v, which is not a *StatementError", err)
	}

	if failure.Position != 3 {
		t.Errorf("the failure reports position %d, want 3", failure.Position)
		t.Log("The position counts from one, as golden files number statements, and M4-T9 records")
		t.Log("position-1 as the last statement that succeeded.")
	}

	if failure.Statement.SQL != "c2" {
		t.Errorf("the failure names statement %q, want %q", failure.Statement.SQL, "c2")
	}
}

func TestAFailureNamesTheMigrationAndTheOperationTheStatementCameFrom(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "b1"
	opts, _ := staged(t, "abc")

	_, err := Apply(t.Context(), conn, opts)

	var failure *MigrationError
	if !errors.As(err, &failure) {
		t.Fatalf("Apply returned %v, which is not a *MigrationError", err)
	}

	if failure.Migration.Version != versionAt(0) {
		t.Errorf("the failure names %s, want %s", failure.Migration.Version, versionAt(0))
	}

	if failure.Operation == nil {
		t.Fatal("the failure names no operation, so the message cannot say which one failed")
	}

	if got := failure.Operation.Kind().String(); !strings.Contains(failure.Error(), got) {
		t.Errorf("the message %q does not name the operation %s", failure.Error(), got)
	}

	if !strings.Contains(failure.Error(), migrationAt(t, 0).Filename()) {
		t.Errorf("the message %q does not name the migration", failure.Error())
	}

	t.Log("Invariant #9: every user-facing error names the migration ID and the offending")
	t.Log("operation. A driver reports a statement; plan.Plan.At is what turns that position")
	t.Log("back into the operation the migration actually declared.")
}

func TestTheHistoryTableIsEnsuredUnderTheLockAndBeforeAnyTransaction(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	locked := slices.Index(conn.calls, "lock")
	ensured := slices.Index(conn.calls, "ensure")
	began := slices.Index(conn.calls, "begin")

	if locked != 0 || ensured < locked || began < ensured {
		t.Fatalf("the session was asked for %v, want lock, then ensure, then any transaction",
			conn.calls)
	}

	t.Log("Two runs racing between the existence check and the CREATE TABLE collide on a catalog")
	t.Log("index, and the advisory key needs no table to exist. Ensuring lazily inside a")
	t.Log("migration's transaction would drop the table with the first rollback, row and all.")
}

func TestAnAbsentHistoryTableAfterEnsureIsRefusedRatherThanTreatedAsNoRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.list = history.ErrNoTable
	opts, _ := staged(t, "a")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, history.ErrNoTable) {
		t.Fatalf("Apply returned %v, want a refusal reporting history.ErrNoTable", err)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec") {
			t.Fatalf("the session was asked for %v after the history could not be read", conn.calls)
		}
	}

	t.Log("An absent log reconciles to zero statuses by design (ADR 0019), so a run that read it")
	t.Log("as 'nothing pending' would report success having applied nothing at all.")
}

func TestTheFirstBatchOnAnEmptyHistoryIsOne(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.Batch != 1 {
		t.Fatalf("the first invocation recorded under batch %d, want 1", result.Batch)
	}

	if got := conn.store.runs[0].Batch; got != 1 {
		t.Fatalf("the recorded row says batch %d, want 1", got)
	}

	t.Log("Batches number from one, so the zero value is not a batch. That is what lets a Result")
	t.Log("reporting batch 0 mean honestly that no batch was used.")
}

func TestABatchIsOneMoreThanTheLastRecordedRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 7, 1)}

	opts, _ := staged(t, "a", "b")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.Batch != 8 {
		t.Fatalf("the invocation recorded under batch %d, want 8", result.Batch)
	}

	t.Log("A batch is one migr invocation and migr down reverses the last one (M4-T8), so a batch")
	t.Log("that reused a number would roll back two invocations as though they were one.")
}

func TestOrdinalsNumberTheRunsRatherThanTheDiscoveredMigrations(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1)}

	opts, _ := staged(t, "a", "b", "c")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []int32{1, 2}
	got := make([]int32, 0, len(result.Runs))

	for _, run := range result.Runs {
		got = append(got, run.Ordinal)
	}

	if !slices.Equal(got, want) {
		t.Fatalf("the two pending migrations took ordinals %v, want %v", got, want)
	}

	t.Log("The first staged migration was already applied, so it is not a run in this batch. An")
	t.Log("ordinal counted over the discovered list would leave batch 2 starting at ordinal 2 and")
	t.Log("a rollback stepping through a gap.")
}

func TestNoBatchIsAllocatedWhenNothingWillRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 4, 1)}

	opts, _ := staged(t, "a")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.Batch != 0 {
		t.Errorf("an invocation that ran nothing reports batch %d, want 0", result.Batch)
	}

	if slices.Contains(conn.calls, "latest") {
		t.Errorf("the session was asked for %v, want no batch read when nothing will run",
			conn.calls)
	}

	t.Log("Batch 0 is not a batch, so a Result reporting it says plainly that none was used.")
}

func TestAnAppliedMigrationIsSkippedRatherThanRerun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1)}

	opts, src := staged(t, "a", "b")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Skipped) != 1 || result.Skipped[0].State != history.StateApplied {
		t.Fatalf("the result reports %v skipped, want the one already-applied migration",
			result.Skipped)
	}

	if slices.Contains(src.asked, versionAt(0)) {
		t.Errorf("the source was asked about %v, which includes an already-applied migration",
			src.asked)
	}

	t.Log("Reconciling under the lock is what makes this stable: deciding outside it lets a")
	t.Log("second run apply the same migrations between the decision and the acquire.")
}

func TestAVersionOnlyTheDatabaseCarriesIsSkipped(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 9, 1, 1)}

	opts, _ := staged(t, "a")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	var missing []Skipped

	for _, skipped := range result.Skipped {
		if skipped.State == history.StateMissing {
			missing = append(missing, skipped)
		}
	}

	if len(missing) != 1 || missing[0].Version != versionAt(9) {
		t.Fatalf("the result reports %v skipped, want the version no file describes",
			result.Skipped)
	}

	if missing[0].Migration != (migration.Migration{}) {
		t.Errorf("a version no file describes carries %v, want a zero migration",
			missing[0].Migration)
	}

	t.Log("The database carries schema this repository can neither describe nor roll back. That")
	t.Log("is worth reporting and is not worth refusing over, since nothing here can run it.")
}

func TestADriftedMigrationRefusesTheWholeRunBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	was := ran(t, 1, 1, 1)
	was.Checksum.Source = checksum.OfSource([]byte("what the file used to hold"))
	conn.store.runs = []history.Record{was}

	opts, _ := staged(t, "a", "b")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrDrifted", err)
	}

	var drift *DriftError
	if !errors.As(err, &drift) {
		t.Fatalf("errors.As found no *DriftError in %T", err)
	}

	if drift.Recorded != was.Checksum {
		t.Errorf("the refusal reports %v recorded, want the checksum the history row holds",
			drift.Recorded)
	}

	if drift.Source != checksum.OfSource([]byte(bodyAt(1))) {
		t.Errorf("the refusal reports %v on disk, want what the file hashes to now", drift.Source)
		t.Log("The recomputed half has to come from the file rather than from the row, or the")
		t.Log("two values always agree and the message can never report the difference.")
	}

	if drift.Direction != ir.Up {
		t.Errorf("the refusal reports direction %q, want the one the run was performing",
			drift.Direction)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec") {
			t.Fatalf("the session was asked for %v before the refusal", conn.calls)
		}
	}

	t.Log("The drifted migration is second in the queue and the first is pending, so running")
	t.Log("until the queue reaches it would leave a partially migrated database for a condition")
	t.Log("that was knowable before the first statement.")
}

func TestADirtyMigrationRefusesTheWholeRunBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	stopped := ran(t, 1, 1, 1)
	stopped.Outcome = history.Dirty
	stopped.LastStatement = 4
	stopped.Error = "the server refused this statement"
	conn.store.runs = []history.Record{stopped}

	opts, _ := staged(t, "a", "b")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrDirty) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrDirty", err)
	}

	if !strings.Contains(err.Error(), "statement 4") {
		t.Errorf("the refusal does not say how far the dirty run got: %v", err)
	}

	var dirty *DirtyError
	if errors.As(err, &dirty) {
		t.Errorf("a migration refused for being dirty reports itself as one that just became "+
			"dirty: %v", err)
		t.Log("Both report ErrDirty, deliberately -- errors.Is is what a caller reaches for. What")
		t.Log("separates them is errors.As, so a run that changed nothing must not carry the type")
		t.Log("that describes a window it opened.")
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec") {
			t.Fatalf("the session was asked for %v before the refusal", conn.calls)
		}
	}

	t.Log("A dirty run left the schema between states, so nothing after it can be reasoned about")
	t.Log("and Outcome.Blocking says so on the run axis rather than the state one.")
}

func TestADuplicateVersionIsRefusedRatherThanAppliedTwice(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")
	opts.Migrations = append(opts.Migrations, opts.Migrations[0])

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, migration.ErrDuplicateVersion) {
		t.Fatalf("Apply returned %v, want a refusal reporting migration.ErrDuplicateVersion", err)
	}

	t.Log("Discover already refuses two files claiming one version, but this executor is given a")
	t.Log("caller's slice. Applied twice, the second run collides on (batch, ordinal) partway")
	t.Log("through the queue rather than being refused before it started.")
}

func TestOperationsAreEmittedOnlyForTheMigrationsThatWillRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1), ran(t, 1, 1, 2)}

	opts, src := staged(t, "a", "b", "c")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !slices.Equal(src.asked, []string{versionAt(2)}) {
		t.Fatalf("the source was asked about %v, want only the pending migration", src.asked)
	}

	if !slices.Equal(src.directions, []ir.Direction{ir.Up}) {
		t.Fatalf("the source was asked for %v, want what an apply run performs", src.directions)
	}

	t.Log("Emitting is not free: M6 spawns a runtime process per migration, and ADR 0009 lets a")
	t.Log("migration branch on live query results. Asking about a migration that will not run is")
	t.Log("a subprocess of side effects for an answer nothing reads.")
}

func TestAFailureToEmitOperationsRecordsNoRunAtAll(t *testing.T) {
	conn := newRecorder(t, true)
	opts, src := staged(t, "a")

	broken := errors.New("this migration will not load")
	src.fail[versionAt(0)] = broken

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, broken) {
		t.Fatalf("Apply returned %v, want the failure the source reported", err)
	}

	if len(conn.store.runs) != 0 {
		t.Fatalf("the history holds %v, want nothing for a migration that never produced any",
			conn.store.runs)
	}

	t.Log("A run whose operations were never produced is not a run: there is no checksum to")
	t.Log("record it under, and Record.Validate would refuse the row anyway.")
}

func TestTheRecordedRunCarriesWhatItsRowMustSay(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "ab")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(conn.store.runs) != 1 {
		t.Fatalf("the history holds %d rows, want one", len(conn.store.runs))
	}

	rec := conn.store.runs[0]

	if rec.AppliedAt != start {
		t.Errorf("the row records %v, want the instant the injected clock reported (%v)",
			rec.AppliedAt, start)
	}

	if rec.Duration != tick {
		t.Errorf("the row records a duration of %v, want %v", rec.Duration, tick)
	}

	if rec.Driver != "recorder" || rec.Runtime != runtimeName {
		t.Errorf("the row records driver %q and runtime %q, want the session's and the options'",
			rec.Driver, rec.Runtime)
	}

	if rec.Direction != ir.Up || rec.Version != versionAt(0) || rec.Name != nameAt(0) {
		t.Errorf("the row records %s of %s_%s, want an up of the migration that ran",
			rec.Direction, rec.Version, rec.Name)
	}

	if run := result.Runs[0]; run.Statements != 2 || run.Started != start {
		t.Errorf("the result reports %+v, want two statements started at %v", run, start)
	}

	t.Log("forbidigo bans time.Now in this package, and a history row must carry a non-zero")
	t.Log("AppliedAt. The clock is therefore injected rather than defaulted, because there is")
	t.Log("nothing here to default it to.")
}

func TestTheRecordedChecksumPairsTheFileWithWhatTheMigrationEmitted(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "abc")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	emitted, err := checksum.OfOperations(operations("abc"))
	if err != nil {
		t.Fatalf("hashing the staged operations: %v", err)
	}

	want := checksum.Checksum{Source: checksum.OfSource([]byte(bodyAt(0))), IR: emitted}
	if got := conn.store.runs[0].Checksum; got != want {
		t.Fatalf("the row records %s, want %s", got, want)
	}

	t.Log("The source half decides drift (ADR 0009) and the IR half records what one run emitted.")
	t.Log("Hashing after compiling would record the planner's output rather than the migration's,")
	t.Log("which matters the day M13-T5 starts rewriting operations on the way through.")
}

func TestAClockThatTicksBackwardsDoesNotFailAMigration(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")
	opts.Clock = backwards()

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := conn.store.runs[0].Duration; got != 0 {
		t.Fatalf("the row records a duration of %v, want it clamped to zero", got)
	}

	t.Log("Record.Validate refuses a negative duration, and it is validated inside the")
	t.Log("transaction. Left unclamped, a clock stepping backwards would roll back a migration")
	t.Log("that worked, over provenance rather than over schema.")
}

func TestTheLockIsReleasedAfterAFailedMigrationHasRolledBack(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, _ := staged(t, "a")

	if _, err := Apply(t.Context(), conn, opts); err == nil {
		t.Fatal("Apply reported success for a migration whose only statement failed")
	}

	rollback := slices.Index(conn.calls, "rollback")
	unlock := slices.Index(conn.calls, "unlock")

	if unlock != len(conn.calls)-1 || unlock < rollback {
		t.Fatalf("the session was asked for %v, want the release last and after the rollback",
			conn.calls)
	}

	t.Log("PostgreSQL refuses every command in an aborted transaction, the one giving an advisory")
	t.Log("lock back included. Releasing before the rollback turns a failed migration into a")
	t.Log("failed release as well, and the second failure is the one an operator reads.")
}

func TestASessionThatHandsBackNoHistoryStoreIsRefusedBeforeTheLock(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store = nil
	opts, _ := staged(t, "a")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNoHistory) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNoHistory", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("History returns no error, so a nil store is a value nothing can report on later. Held")
	t.Log("as far as the first Ensure, it would be a lock taken for a run that cannot proceed.")
}

func TestAnApplyRunIsRefusedForWhatItWasNotGiven(t *testing.T) {
	cases := []struct {
		field    string
		strip    func(*Options)
		sentinel error
	}{
		{"planner", func(o *Options) { o.Planner = plan.Planner{} }, plan.ErrNoCompiler},
		{"source", func(o *Options) { o.Source = nil }, ErrNoSource},
		{"clock", func(o *Options) { o.Clock = nil }, ErrNoClock},
		{"runtime", func(o *Options) { o.Runtime = nil }, ErrNoRuntime},
	}

	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			conn := newRecorder(t, true)
			opts, _ := staged(t, "a")
			tc.strip(&opts)

			_, err := Apply(t.Context(), conn, opts)
			if !errors.Is(err, tc.sentinel) {
				t.Fatalf("an apply run with no %s returned %v, want %v", tc.field, err, tc.sentinel)
			}

			if len(conn.calls) != 0 {
				t.Fatalf("the session was asked for %v before the refusal", conn.calls)
			}
		})
	}

	t.Log("Every one of these is knowable with no database, so a run missing one must not reach")
	t.Log("the lock. A clock found missing after the acquire is a lock held for nothing.")
}

func TestAMigrationThatCompilesToNothingIsRefusedBeforeAnythingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNothingToRun) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNothingToRun", err)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec") || call == "begin" {
			t.Fatalf("the session was asked for %v before the refusal", conn.calls)
		}
	}

	t.Log("An empty migration reported as applied is the conformance suite's \"reports success and")
	t.Log("changes nothing\" failure, one layer up from the Compile it forbids it in.")
}

func TestASessionWithoutTransactionalDDLRunsRatherThanBeingRefused(t *testing.T) {
	conn := newRecorder(t, false)
	conn.store.table = true

	opts, _ := staged(t, "abc")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply returned %v, want the migration to run", err)
	}

	if len(result.Runs) != 1 {
		t.Fatalf("the run applied %d migrations, want the one it was given", len(result.Runs))
	}

	want := []string{
		"lock", "ensure", "list", "latest",
		"begin", "exec a0", "exec b1", "exec c2", "record " + versionAt(0), "commit",
		"unlock",
	}
	if !slices.Equal(conn.calls, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
	}

	t.Log("A session with no transactional DDL is exactly what MySQL declares in M9-T3, and it")
	t.Log("still goes through Transact: the driver is the only thing that knows what one means")
	t.Log("on its engine. What changes is what a failure records, not how a success runs.")
}

func TestAMigrationThatCannotBeOneTransactionIsRefused(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBc")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNotAtomic) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNotAtomic", err)
	}

	if !strings.Contains(err.Error(), "M13-T4") {
		t.Errorf("the refusal does not name the task that lifts it: %v", err)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("Three runs means two commits, and M4-T9's dirty state does not exist to record that a")
	t.Log("migration got halfway. The run loop that executes them is already written, so M13-T4")
	t.Log("deletes this precondition rather than rewriting an executor.")
}

func TestAMigrationThatIsEntirelyStandaloneStatementsIsRefused(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "AB")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNotAtomic) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNotAtomic", err)
	}

	if !strings.Contains(err.Error(), "M13-T4") {
		t.Errorf("the refusal does not name the task that lifts it: %v", err)
	}

	if slices.Contains(conn.calls, "begin") {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("This is one group, so a refusal counting groups lets it through -- and the driver then")
	t.Log("rejects the first statement for declaring it cannot run where the executor put it.")
	t.Log("The precondition is one transactional run, which is also where the row is written.")
}

func TestTheRefusalCountsTheStatementsThatForcedTheSplit(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "aBcDDe")

	_, err := Apply(t.Context(), conn, opts)
	if err == nil {
		t.Fatal("Apply accepted a migration that cannot run as one transaction")
	}

	if !strings.Contains(err.Error(), "3 of its statements") {
		t.Errorf("the refusal does not count the statements that cannot run in a transaction: %v", err)
	}
}

func TestNothingIsAskedOfTheDatabaseWhenTheLockCannotBeTaken(t *testing.T) {
	conn := newRecorder(t, true)
	conn.lockErr = errors.New("somebody else holds it")
	opts, _ := staged(t, "a")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, conn.lockErr) {
		t.Fatalf("Apply returned %v, want what the acquire reported", err)
	}

	if len(conn.calls) != 0 {
		t.Fatalf("the session was asked for %v after the lock could not be taken", conn.calls)
	}

	t.Log("Contention is exit code 3 through driver.ErrLockTimeout, and it survives the wrapping")
	t.Log("here because nothing between the acquire and the caller replaces the error.")
}

func TestARunThatAppliesNothingStillReportsWhatItSkipped(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1), ran(t, 1, 1, 2)}

	opts, _ := staged(t, "a", "b")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 0 {
		t.Errorf("the result claims %d runs, want none", len(result.Runs))
	}

	if len(result.Skipped) != 2 {
		t.Errorf("the result reports %d skipped, want both migrations", len(result.Skipped))
	}

	if slices.Contains(conn.calls, "begin") {
		t.Errorf("the session was asked for %v with nothing to apply", conn.calls)
	}

	t.Log("migr up on an up-to-date database is the common case, and it must not open a")
	t.Log("transaction, allocate a batch, or ask a runtime what any migration does.")
}

func TestARunDurationIsMeasuredUpToTheHistoryWrite(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a", "b")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 2 {
		t.Fatalf("the result reports %d runs, want two", len(result.Runs))
	}

	if result.Runs[0].Started != start || result.Runs[1].Started != start.Add(2*tick) {
		t.Fatalf("the runs started at %v and %v, want one clock reading each side of every run",
			result.Runs[0].Started, result.Runs[1].Started)
	}

	for _, run := range result.Runs {
		if run.Duration != tick {
			t.Fatalf("%s took %v, want %v", run.Migration.Version, run.Duration, tick)
		}
	}

	t.Log("The duration is read inside the transaction because it has to be in the row the commit")
	t.Log("makes durable. It therefore excludes the commit itself, which is a stated cost rather")
	t.Log("than an oversight.")
}

func TestTheStatementPositionContinuesAcrossOneMigrationAndRestartsForTheNext(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "b1"
	opts, _ := staged(t, "xy", "ab")

	_, err := Apply(t.Context(), conn, opts)

	var failure *StatementError
	if !errors.As(err, &failure) {
		t.Fatalf("Apply returned %v, which is not a *StatementError", err)
	}

	if failure.Position != 2 {
		t.Fatalf("the failure reports position %d, want 2 within its own migration", failure.Position)
	}

	t.Log("A position is a place in one migration, which is what M4-T9 records as how far a run")
	t.Log("got. Counted across the queue it would name a statement the failing migration does")
	t.Log("not have, and M9-T6 would resume from the wrong one.")
}

func TestAHistoryWriteThatFailsIsReportedRatherThanSwallowed(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")

	refused := errors.New("the history table would not take that row")
	conn.store.record = refused

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, refused) {
		t.Fatalf("Apply returned %v, want the failure the store reported", err)
	}

	if !slices.Contains(conn.calls, "rollback") {
		t.Fatalf("the session was asked for %v, want the transaction discarded", conn.calls)
	}

	t.Log("The row is written inside the transaction, so a write that fails takes the schema")
	t.Log("change with it. A migration applied but not recorded is what the next up re-runs.")
}

func TestABookkeepingFailureDoesNotReplaceTheFailureItWasBookkeeping(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, _ := staged(t, "a")

	lost := errors.New("the failure row could not be written either")
	conn.store.failed = lost

	_, err := Apply(t.Context(), conn, opts)

	if !errors.Is(err, errRefused) {
		t.Errorf("Apply returned %v, which no longer names why the migration failed", err)
	}

	if !errors.Is(err, lost) {
		t.Errorf("Apply returned %v, which hides that the failure was never recorded", err)
	}

	t.Log("Reporting only the bookkeeping failure replaces the operator's problem with a")
	t.Log("diagnostic's; reporting only the migration's hides that migr history will not show it.")
}

func TestTheCleanupPathsDoNotInheritTheContextThatCanceledTheRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.fail = "a0"
	opts, _ := staged(t, "a")

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := Apply(ctx, conn, opts); err == nil {
		t.Fatal("Apply reported success for a migration whose only statement failed")
	}

	for _, call := range conn.calls {
		if strings.HasSuffix(call, "-on-a-canceled-context") {
			t.Errorf("the session was asked for %v, and %q inherited the canceled context",
				conn.calls, call)
		}
	}

	if len(conn.store.runs) != 1 {
		t.Fatalf("the history holds %v, want the failed run recorded despite the cancellation",
			conn.store.runs)
	}

	t.Log("Ctrl-C is the ordinary way a migration is interrupted. On the canceled context the")
	t.Log("release fails and the lock stays held until the session ends, and the failure row is")
	t.Log("never written -- both failing in exactly the case they exist for.")
}

func TestTheHistoryRowIsWrittenInsideTheLastRunThatCanHoldIt(t *testing.T) {
	cases := []struct {
		spec string
		last int
	}{
		{"abc", 0},
		{"ABC", -1},
		{"aBc", 2},
		{"abC", 0},
		{"AbC", 1},
		{"aBcDDe", 4},
	}

	for _, tc := range cases {
		t.Run(tc.spec, func(t *testing.T) {
			if got := lastTransactional(Groups(statements(tc.spec))); got != tc.last {
				t.Fatalf("%q writes its row inside run %d, want %d", tc.spec, got, tc.last)
			}
		})
	}

	t.Log("The row goes in the last run that is a transaction, so nothing after it is uncommitted")
	t.Log("when the row claims the migration applied. The first would claim it too early, and -1")
	t.Log("is the migration that has nowhere to put it at all -- which is a refusal, not a run.")
}

func TestAMigrationOfSeveralRunsRecordsInsideTheLastTransactionalOne(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "a")

	exec := &executor{conn: conn, store: conn.store, opts: opts, batch: 1}
	ready := prepared{
		item:     queued{migration: migrationAt(t, 0), ordinal: 1},
		compiled: plan.Plan{Direction: ir.Up},
		groups:   Groups(statements("aBc")),
		sum:      sumOf(0),
	}
	rec := exec.newRecord(ready)

	survived, err := exec.runMigration(t.Context(), ready.groups, exec.recorder(&rec))
	if err != nil {
		t.Fatalf("runMigration: %v", err)
	}

	if survived != 0 {
		t.Errorf("a run that succeeded reports %d statements left behind, want none", survived)
	}

	want := []string{
		"begin", "exec a0", "commit",
		"exec-outside-any-transaction B1",
		"begin", "exec c2", "record " + versionAt(0), "commit",
	}
	if !slices.Equal(conn.calls, want) {
		t.Fatalf("the session was asked for\n  %v\nwant\n  %v", conn.calls, want)
	}

	if len(conn.store.runs) != 1 || conn.store.runs[0].Ordinal != 1 {
		t.Fatalf("the history holds %v, want the one row this migration is entitled to",
			conn.store.runs)
	}

	t.Log("checkRunnable still refuses this shape, so the loop is reached only from here. It is")
	t.Log("written and held to its behavior anyway, because M13-T4's job is to delete a")
	t.Log("precondition rather than to design an executor -- which is what M4-T5's done-when asks.")
	t.Log("The row goes inside the last transactional run: written inside the first, it would")
	t.Log("commit a claim that the migration applied while two of its runs had not happened.")
}

func TestElapsedIsNeverNegative(t *testing.T) {
	if got := elapsed(start, start.Add(-time.Hour)); got != 0 {
		t.Fatalf("elapsed over a backwards interval is %v, want 0", got)
	}

	if got := elapsed(start, start.Add(time.Hour)); got != time.Hour {
		t.Fatalf("elapsed over an hour is %v, want 1h", got)
	}
}
