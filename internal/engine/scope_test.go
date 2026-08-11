package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
)

// versions renders what a result acted on, in the order it acted.
func versions(result Result) []string {
	acted := make([]string, 0, len(result.Runs))

	for _, run := range result.Runs {
		acted = append(acted, run.Migration.Version)
	}

	return acted
}

// stoppedShortOf renders the versions a result reports it did not run, in the order reported.
func stoppedShortOf(result Result) []string {
	left := make([]string, 0, len(result.Skipped))

	for _, item := range result.Skipped {
		left = append(left, item.Version)
	}

	return left
}

// inRunOrder stages migrations in the order a run will take them, which need not be version order.
func inRunOrder(t *testing.T, order ...int) Options {
	t.Helper()

	opts, src := staged(t, make([]string, len(order))...)

	files := make([]history.File, 0, len(order))

	for _, n := range order {
		m := migrationAt(t, n)
		files = append(files, history.NewFile(m, []byte(bodyAt(n))))
		src.ops[m.Version] = operations(string(rune('a' + n)))
	}

	opts.Migrations = files

	return opts
}

// inBatches stages a history in which each batch applied the migrations given for it, in order.
func inBatches(t *testing.T, conn *recorder, batches ...[]int) {
	t.Helper()

	conn.store.table = true

	for i, batch := range batches {
		var ordinal int32

		for _, n := range batch {
			ordinal++
			conn.store.runs = append(conn.store.runs, ran(t, n, int64(i)+1, ordinal))
		}
	}
}

func TestAnUnnarrowedRunActsOnEverythingItCan(t *testing.T) {
	up := newRecorder(t, true)
	up.store.table = true

	unset, _ := staged(t, "a", "b", "c")

	all, _ := staged(t, "a", "b", "c")
	all.Scope = All()

	unnarrowed, err := Apply(t.Context(), up, unset)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	explicit := newRecorder(t, true)
	explicit.store.table = true

	stated, err := Apply(t.Context(), explicit, all)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !slices.Equal(versions(unnarrowed), versions(stated)) {
		t.Errorf("an unset scope ran %v and All() ran %v", versions(unnarrowed), versions(stated))
	}

	if len(unnarrowed.Runs) != 3 {
		t.Errorf("the unnarrowed run applied %d migrations, want all three", len(unnarrowed.Runs))
		t.Log("The zero Scope has to be the neutral one, or every existing caller silently")
		t.Log("changes behavior the day this field is added.")
	}
}

func TestSteppingUpTakesTheFirstPendingAndSteppingDownTakesTheLastApplied(t *testing.T) {
	up := newRecorder(t, true)
	up.store.table = true

	rising, _ := staged(t, "a", "b", "c")
	rising.Scope = Steps(2)

	applied, err := Apply(t.Context(), up, rising)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if want := []string{versionAt(0), versionAt(1)}; !slices.Equal(versions(applied), want) {
		t.Errorf("--step 2 going up applied %v, want %v", versions(applied), want)
	}

	down := newRecorder(t, true)
	carried(t, down, 0, 1, 2)

	falling, _ := staged(t, "a", "b", "c")
	falling.Scope = Steps(2)

	undone, err := Rollback(t.Context(), down, falling)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if want := []string{versionAt(2), versionAt(1)}; !slices.Equal(versions(undone), want) {
		t.Errorf("--step 2 going down undid %v, want %v", versions(undone), want)
	}

	t.Log("One count, two meanings: the first two pending one way and the last two applied the")
	t.Log("other. They are not one function with a direction flag because they share no answer.")
}

func TestANarrowedBatchStillNumbersItsOrdinalsFromOne(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1, 2)

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = Steps(2)

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	for i, run := range result.Runs {
		if run.Ordinal != int32(i)+1 {
			t.Errorf("run %d carries ordinal %d, want %d", i, run.Ordinal, i+1)
			t.Log("An ordinal numbers the runs an invocation performed, not the migrations it")
			t.Log("considered (ADR 0021, E), so narrowing has to happen before they are assigned.")
		}
	}
}

func TestAStepCountLargerThanTheQueueRunsTheQueue(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a", "b")
	opts.Scope = Steps(99)

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 2 {
		t.Errorf("--step 99 applied %d migrations, want the two there were", len(result.Runs))
	}

	if len(result.Skipped) != 0 {
		t.Errorf("--step 99 reports %v stopped short of, want nothing", stoppedShortOf(result))
	}
}

func TestAStepCountBelowOneIsRefusedBeforeTheLockIsTaken(t *testing.T) {
	for _, steps := range []int32{0, -1} {
		conn := newRecorder(t, true)
		conn.store.table = true

		opts, _ := staged(t, "a")
		opts.Scope = Steps(steps)

		_, err := Apply(t.Context(), conn, opts)
		if !errors.Is(err, ErrNoSteps) {
			t.Fatalf("Apply with --step %d returned %v, want a refusal reporting ErrNoSteps",
				steps, err)
		}

		if len(conn.calls) != 0 {
			t.Errorf("the session was asked for %v before the refusal", conn.calls)
			t.Log("A selector naming nothing is knowable with no database, so refusing it after")
			t.Log("the lock would be a lock held for a run that was never going to happen.")
		}
	}
}

func TestABlankVersionIsRefusedBeforeTheLockIsTaken(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a")
	opts.Scope = To("")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrUnknownVersion", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}
}

func TestGoingUpToAVersionAppliesThroughItAndGoingDownLeavesItApplied(t *testing.T) {
	up := newRecorder(t, true)
	up.store.table = true

	rising, _ := staged(t, "a", "b", "c")
	rising.Scope = To(versionAt(1))

	applied, err := Apply(t.Context(), up, rising)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if want := []string{versionAt(0), versionAt(1)}; !slices.Equal(versions(applied), want) {
		t.Errorf("--to going up applied %v, want %v", versions(applied), want)
	}

	down := newRecorder(t, true)
	carried(t, down, 0, 1, 2)

	falling, _ := staged(t, "a", "b", "c")
	falling.Scope = To(versionAt(1))

	undone, err := Rollback(t.Context(), down, falling)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if want := []string{versionAt(2)}; !slices.Equal(versions(undone), want) {
		t.Errorf("--to going down undid %v, want %v", versions(undone), want)
	}

	t.Log("--to names the state the database should end in, so both runs leave it at that")
	t.Log("version: going up applies through it, going down undoes everything newer.")
}

func TestGoingUpToAnAlreadyAppliedVersionStillAppliesWhatComesBeforeIt(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 1, 1, 1)}

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = To(versionAt(1))

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if want := []string{versionAt(0)}; !slices.Equal(versions(result), want) {
		t.Errorf("--to an applied version ran %v, want %v", versions(result), want)
		t.Log("The target is not in the queue at all, so a cut taken over the queue alone would")
		t.Log("find nothing and refuse. The cut is taken over the report, which still holds it.")
	}
}

func TestTheCutFollowsTheOrderTheRunWillTakeRatherThanVersionOrder(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts := inRunOrder(t, 2, 0, 1)
	opts.Scope = To(versionAt(0))

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{versionAt(2), versionAt(0)}
	if !slices.Equal(versions(result), want) {
		t.Errorf("--to applied %v, want %v", versions(result), want)
		t.Log("Version 2 is declared to run first, which ADR 0018 makes the use case rather than a")
		t.Log("mistake. A cut taken by comparing versions would stop before it and apply version 0")
		t.Log("alone -- against a database missing what that migration was built on.")
	}
}

func TestARollbackCutFollowsTheRunsThatHappenedRatherThanVersionOrder(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 2, 1, 1), ran(t, 0, 1, 2), ran(t, 1, 1, 3)}

	opts := inRunOrder(t, 2, 0, 1)
	opts.Scope = To(versionAt(0))

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []string{versionAt(1)}
	if !slices.Equal(versions(result), want) {
		t.Errorf("--to undid %v, want %v", versions(result), want)
		t.Log("Version 0 ran second, so only version 1 is newer than it. Undoing by version would")
		t.Log("have undone version 1 and version 2, and version 2 ran before version 0.")
	}
}

func TestAVersionNoMigrationCarriesIsRefusedAndNamesWhatThereWas(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a", "b")
	opts.Scope = To("20260230T000000")

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrUnknownVersion) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrUnknownVersion", err)
	}

	for _, want := range []string{"20260230T000000", versionAt(0), versionAt(1)} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}

	t.Log("February 30th is not an instant and not a version anything reconciled, and one")
	t.Log("refusal is the right answer to both -- naming what there was is what makes it usable.")
}

func TestRollingBackToAVersionTheDatabaseDoesNotCarryIsRefused(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "a", "b")
	opts.Scope = To(versionAt(1))

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrNotApplied) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrNotApplied", err)
	}

	if !strings.Contains(err.Error(), versionAt(0)) {
		t.Errorf("the refusal does not name the version the database does carry: %v", err)
	}

	t.Log("Leaving a database at a version it does not hold would have to guess between")
	t.Log("stopping before it and undoing everything, and neither guess is recoverable.")
}

func TestTheLastBatchIsTheMostRecentOneTheDatabaseStillCarries(t *testing.T) {
	conn := newRecorder(t, true)
	inBatches(t, conn, []int{0, 1}, []int{2})

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = LastBatch()

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if want := []string{versionAt(2)}; !slices.Equal(versions(result), want) {
		t.Fatalf("the last batch undid %v, want %v", versions(result), want)
	}

	if want := []string{versionAt(1), versionAt(0)}; !slices.Equal(stoppedShortOf(result), want) {
		t.Errorf("the run reports %v left alone, want %v", stoppedShortOf(result), want)
	}

	t.Log("This is what migr down with no arguments means, and it is a batch rather than a")
	t.Log("single migration because one invocation is the unit an operator undoes.")
}

func TestTheLastBatchIsReadFromTheRunsThatAppliedItRatherThanFromLatest(t *testing.T) {
	conn := newRecorder(t, true)
	inBatches(t, conn, []int{0, 1}, []int{2})
	conn.store.runs = append(conn.store.runs, undone(t, 2, 3, 1))

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = LastBatch()

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []string{versionAt(1), versionAt(0)}
	if !slices.Equal(versions(result), want) {
		t.Fatalf("the last batch undid %v, want %v", versions(result), want)
	}

	t.Log("Batch 3 is a rollback's own, and batch 2 is already given back. Latest is direction-")
	t.Log("and outcome-blind, so reading the batch from it would name a batch with nothing left")
	t.Log("to undo -- and a second migr down would then report nothing to do, forever.")
}

func TestTheLastBatchIsARollbacksAloneAndAnApplyRefusesItBeforeTheLock(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a")
	opts.Scope = LastBatch()

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNoBatch) {
		t.Fatalf("Apply returned %v, want a refusal reporting ErrNoBatch", err)
	}

	if len(conn.calls) != 0 {
		t.Errorf("the session was asked for %v before the refusal", conn.calls)
	}

	t.Log("A pending migration has never run, so it belongs to no batch and there is no last")
	t.Log("one to act on. Silently applying everything instead would be the worst answer.")
}

func TestADriftedMigrationOutsideTheNarrowedRangeStillRefusesTheWholeRun(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1, 2)
	conn.store.runs[0].Checksum.Source = checksum.OfSource([]byte("what the file used to hold"))

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = Steps(1)

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrDrifted) {
		t.Fatalf("Rollback returned %v, want a refusal reporting ErrDrifted", err)
	}

	t.Log("The drifted migration is the oldest and --step 1 would never have reached it. The")
	t.Log("refusals are a property of the reconciled database rather than of one invocation's")
	t.Log("appetite, so they fire on the whole report before narrowing sees it.")
}

func TestWhatANarrowedRunStoppedShortOfIsReportedWithTheStateItIsStillIn(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = Steps(1)

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{versionAt(1), versionAt(2)}
	if !slices.Equal(stoppedShortOf(result), want) {
		t.Fatalf("the run reports %v stopped short of, want %v", stoppedShortOf(result), want)
	}

	for _, item := range result.Skipped {
		if item.State != history.StatePending {
			t.Errorf("%s is reported as %q, want the state it is still in", item.Version, item.State)
		}

		if item.Migration.Version != item.Version {
			t.Errorf("%s carries no migration, and a narrowed-out one always has a file",
				item.Version)
		}
	}

	t.Log("Without this, migr up --step 1 cannot say there is more to do without reconciling")
	t.Log("a second time against a database it has already given the lock back on.")
}

func TestANarrowedRunStillTakesItsBatchFromTheLastRecordedRun(t *testing.T) {
	conn := newRecorder(t, true)
	inBatches(t, conn, []int{0}, []int{1})

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = Steps(1)

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if result.Batch != 3 {
		t.Errorf("the narrowed run recorded under batch %d, want one past the last run", result.Batch)
		t.Log("The batch is a decision about writing and the log was read for reconciling. A")
		t.Log("maximum taken from the filtered queue is the maximum of a filtered slice.")
	}
}
