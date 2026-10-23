package history

import (
	"errors"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/ir"
)

func TestTheZeroLogIsADatabaseWithNoHistoryTable(t *testing.T) {
	var zero Log

	if zero.Present() {
		t.Error("the zero Log reported a history table, so an unset value reads as an empty one")
		t.Log("Log's fields are unexported precisely so that Log{} is the only literal a caller")
		t.Log("can write, and it has to mean the safe thing: this database has no history.")
	}
}

func TestALogBuiltFromNoRunsIsStillAHistoryTable(t *testing.T) {
	if !NewLog(nil).Present() {
		t.Error("a log built from no runs reported no history table")
		t.Log("A table holding nothing is a fresh project. A database with no table at all is")
		t.Log("an uninitialized one, or a history.table naming something that is not there.")
		t.Log("Reading the second as the first is how a fully migrated database looks virgin.")
	}
}

func TestReadReportsATableNothingCreatedAsAnAbsentLogRatherThanAFailure(t *testing.T) {
	store := stubStore{err: errors.Join(ErrNoTable, errors.New("42P01"))}

	log, err := Read(t.Context(), store)
	if err != nil {
		t.Fatalf("Read reported %v, want a missing table read as an absent log", err)
	}

	if log.Present() {
		t.Error("a store reporting ErrNoTable produced a log that claims a history table")
	}
}

func TestReadPropagatesEveryOtherFailureRatherThanReadingItAsAbsence(t *testing.T) {
	refused := errors.New("permission denied for table migr_migrations")

	log, err := Read(t.Context(), stubStore{err: refused})
	if !errors.Is(err, refused) {
		t.Errorf("Read reported %v, want the store's own failure", err)
		t.Log("Only ErrNoTable becomes an absent log. Anything else read as absence would")
		t.Log("report a database migr cannot see into as one with nothing in it.")
	}

	if log.Present() {
		t.Error("Read returned a present log alongside a failure")
	}
}

func TestReadNeverReportsACorruptRowAsAnAbsentTable(t *testing.T) {
	corrupt := &CorruptError{
		Table: "migr_migrations", Batch: 7, Ordinal: 2,
		Field: "checksum", Err: errors.New("malformed checksum"),
	}

	log, err := Read(t.Context(), stubStore{err: corrupt})
	if !errors.Is(err, ErrCorrupt) {
		t.Errorf("Read reported %v, want an error unwrapping to ErrCorrupt", err)
		t.Log("A corrupt row needs manual repair and is not drift. It must also not be read")
		t.Log("as an absent table, which would invite the next up to re-run everything.")
	}

	if errors.Is(err, ErrNoTable) {
		t.Error("a corrupt row was reported as a database with no history table")
	}

	if log.Present() {
		t.Error("Read returned a present log alongside a corrupt row")
	}
}

func TestReadCollectsTheRunsAStoreHandsBack(t *testing.T) {
	users := file(30, "add_users", "-- users")

	log, err := Read(t.Context(), stubStore{runs: []Record{up(users, 1, Applied)}})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if !log.Present() || len(log.Runs()) != 1 {
		t.Errorf("Read produced present=%t with %d runs, want present with 1", log.Present(),
			len(log.Runs()))
	}
}

func TestRunsAreSortedIntoTheOrderTheRunsHappened(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{
		runAt(users, 2, 1, ir.Down, Applied),
		runAt(users, 1, 2, ir.Up, Applied),
		runAt(users, 1, 1, ir.Up, Failed),
	}

	got := batchOrdinals(NewLog(runs).Runs())
	want := [][2]int64{{1, 1}, {1, 2}, {2, 1}}

	if !slices.Equal(got, want) {
		t.Errorf("the runs came back as %v, want %v", got, want)
		t.Log("The store contract promises this order, but NewLog is a hole a caller reaches")
		t.Log("directly, and three drivers must not be able to reach three answers about one")
		t.Log("log. Sorting at construction puts the invariant on the type.")
	}
}

func TestABatchOrdersBeforeAnOrdinalWithinIt(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{runAt(users, 2, 1, ir.Up, Applied), runAt(users, 1, 9, ir.Up, Applied)}

	got := batchOrdinals(NewLog(runs).Runs())
	want := [][2]int64{{1, 9}, {2, 1}}

	if !slices.Equal(got, want) {
		t.Errorf("the runs came back as %v, want %v", got, want)
		t.Log("Ordinal 9 of batch 1 happened before ordinal 1 of batch 2. Sorting on the")
		t.Log("ordinal alone would reverse them.")
	}
}

func TestAppliedAtDoesNotDecideTheOrderRunsAreFoldedIn(t *testing.T) {
	users := file(30, "add_users", "-- users")

	first := runAt(users, 1, 1, ir.Up, Applied)
	first.AppliedAt = ran.Add(time.Hour)

	second := runAt(users, 1, 2, ir.Down, Applied)
	second.AppliedAt = ran

	got := batchOrdinals(NewLog([]Record{second, first}).Runs())
	want := [][2]int64{{1, 1}, {1, 2}}

	if !slices.Equal(got, want) {
		t.Errorf("the runs came back as %v, want %v", got, want)
		t.Log("applied_at is not the key: two rows can share a millisecond, and")
		t.Log("a tie there rolls two migrations back in the wrong order. (batch, ordinal) is")
		t.Log("the key, and a clock that disagrees with it does not get a vote.")
	}
}

func TestNewLogDoesNotDisturbWhatItWasGiven(t *testing.T) {
	users := file(30, "add_users", "-- users")

	runs := []Record{runAt(users, 2, 1, ir.Down, Applied), runAt(users, 1, 1, ir.Up, Applied)}
	before := slices.Clone(runs)

	NewLog(runs)

	if !reflect.DeepEqual(runs, before) {
		t.Error("NewLog sorted the caller's own slice rather than a copy of it")
	}
}

func TestRunsIsACopy(t *testing.T) {
	users := file(30, "add_users", "-- users")
	log := NewLog([]Record{up(users, 1, Applied)})

	log.Runs()[0].Outcome = Failed

	if log.Runs()[0].Outcome != Applied {
		t.Error("Runs handed out the log's own slice, so a caller rewrote a recorded run")
	}
}

// batchOrdinals renders the key each run was ordered by, which is what these tests compare.
func batchOrdinals(runs []Record) [][2]int64 {
	keys := make([][2]int64, len(runs))
	for i, rec := range runs {
		keys[i] = [2]int64{rec.Batch, int64(rec.Ordinal)}
	}

	return keys
}
