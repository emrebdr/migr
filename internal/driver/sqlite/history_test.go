package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/version"
)

// historyTable is the name these tests record under, which is the contract's own default.
const historyTable = driver.DefaultHistoryTable

// sourceDigest and irDigest are the two halves of a checksum, neither of which this store parses.
const (
	sourceDigest = "3fd4a1f57f4c1f4dc4a1b6a6b3e0c6a5d29a2f2f0b2f3d5e6a7b8c9d0e1f2a3b"
	irDigest     = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a49382716052f3e4d5c6b7a8998aabbccdd"
)

// store opens a session on a file and returns its history store, the table not yet created.
func store(t *testing.T) (*sqliteConn, sqliteHistory) {
	t.Helper()

	c := open(t, filepath.Join(t.TempDir(), "app.db"), driver.Options{})

	held, ok := c.History().(sqliteHistory)
	if !ok {
		t.Fatalf("History returned %T, not this package's store", c.History())
	}

	return c, held
}

// ensured opens a session whose history table exists, which is every write path's precondition.
func ensured(t *testing.T) (*sqliteConn, sqliteHistory) {
	t.Helper()

	c, held := store(t)

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	return c, held
}

// run returns a complete record for one migration, which every write path starts from.
func run(t *testing.T, batch int64, ordinal int32) history.Record {
	t.Helper()

	sum, err := checksum.Parse("migr1:" + sourceDigest + ":" + irDigest)
	if err != nil {
		t.Fatalf("parsing a checksum: %v", err)
	}

	return history.Record{
		Batch:     batch,
		Ordinal:   ordinal,
		Version:   "20260929T120000",
		Name:      "create_users",
		Direction: ir.Up,
		Checksum:  sum,
		AppliedAt: time.Date(2026, 9, 29, 11, 22, 33, 123456789,
			time.FixedZone("UTC+5:30", 5*3600+30*60)),
		Duration: 1500 * time.Millisecond,
		Driver:   Name,
		Runtime:  "sql",
	}
}

func TestEveryPathThatTouchesTheTableReportsErrNoTableWhenThereIsNone(t *testing.T) {
	failure := errors.New("the migration failed")

	for name, call := range map[string]func(context.Context, sqliteHistory) error{
		"List": func(ctx context.Context, h sqliteHistory) error {
			_, err := h.List(ctx)

			return err
		},
		"Latest": func(ctx context.Context, h sqliteHistory) error {
			_, _, err := h.Latest(ctx)

			return err
		},
		"Record": func(ctx context.Context, h sqliteHistory) error {
			return h.Record(ctx, run(t, 1, 1))
		},
		"MarkFailed": func(ctx context.Context, h sqliteHistory) error {
			return h.MarkFailed(ctx, run(t, 1, 1), failure)
		},
		"MarkDirty": func(ctx context.Context, h sqliteHistory) error {
			return h.MarkDirty(ctx, run(t, 1, 1), 2, failure)
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, held := store(t)

			err := call(t.Context(), held)
			if !errors.Is(err, history.ErrNoTable) {
				t.Fatalf("%s returned %v, want history.ErrNoTable", name, err)
			}
		})
	}

	t.Log("The sentinel is asked for rather than classified: SQLite reports a missing table as plain")
	t.Log("SQLITE_ERROR, which is the code a syntax error carries too -- measured. Read as \"no runs")
	t.Log("yet\", a fully migrated production database looks virgin and the next up re-runs every")
	t.Log("migration it had already applied, which is the worst failure in this contract.")
}

func TestARecordSurvivesTheRoundTripFieldForField(t *testing.T) {
	_, held := ensured(t)

	want := run(t, 3, 2)

	if err := held.Record(t.Context(), want); err != nil {
		t.Fatalf("Record: %v", err)
	}

	log, err := held.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(log) != 1 {
		t.Fatalf("the history holds %d runs, want 1", len(log))
	}

	got := log[0]

	want.Outcome = history.Applied
	want.EngineVersion = version.Version
	want.IRVersion = ir.Version

	assertSameRun(t, got, want)
}

// assertSameRun compares two runs field by field, the instant by the moment rather than the clock.
func assertSameRun(t *testing.T, got, want history.Record) {
	t.Helper()

	if !got.AppliedAt.Equal(want.AppliedAt) {
		t.Errorf("applied_at is %v, want the same instant as %v", got.AppliedAt, want.AppliedAt)
	}

	if got.AppliedAt.Location() != time.UTC {
		t.Errorf("applied_at came back in %v, and a recorded instant is read back in UTC",
			got.AppliedAt.Location())
	}

	got.AppliedAt, want.AppliedAt = time.Time{}, time.Time{}

	if got != want {
		t.Errorf("the run reads back as\n%+v\nwant\n%+v", got, want)
	}
}

func TestANanosecondInstantSurvivesAndIsNotTheLocalClocksIdeaOfIt(t *testing.T) {
	_, held := ensured(t)

	rec := run(t, 1, 1)

	if err := held.Record(t.Context(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	stored := recordedColumn(t, held, "applied_at")

	if stored != "2026-09-29T05:52:33.123456789Z" {
		t.Errorf("applied_at is stored as %q", stored)
		t.Log("It is rendered here rather than handed to modernc: a bound time.Time is stored as Go's")
		t.Log("own String form, zone abbreviation included -- measured as")
		t.Log("\"2026-09-29 11:22:33.123456789 +0530 UTC+5:30\", which no RFC3339 parser reads back.")
	}
}

func TestAStoredInstantIsFixedWidthSoItSortsLikeTheInstant(t *testing.T) {
	_, held := ensured(t)

	rec := run(t, 1, 1)
	rec.AppliedAt = time.Date(2026, 9, 29, 0, 0, 0, 110000000, time.UTC)

	if err := held.Record(t.Context(), rec); err != nil {
		t.Fatalf("Record: %v", err)
	}

	if stored := recordedColumn(t, held, "applied_at"); stored != "2026-09-29T00:00:00.110000000Z" {
		t.Errorf("applied_at is stored as %q, and a trimmed fraction sorts wrongly as text", stored)
	}
}

// recordedColumn reads one column of the single recorded run, as the engine stored it.
func recordedColumn(t *testing.T, held sqliteHistory, column string) string {
	t.Helper()

	rows := read(t, held.session, `SELECT `+quote(column)+` FROM `+quote(held.table))

	if len(rows.Values) != 1 || rows.Values[0][0] == nil {
		t.Fatalf("reading %s back gave %d rows", column, len(rows.Values))
	}

	return *rows.Values[0][0]
}

func TestAnErrorlessRunRecordsNullRatherThanAnEmptyString(t *testing.T) {
	_, held := ensured(t)

	if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
		t.Fatalf("Record: %v", err)
	}

	rows := read(t, held.session, `SELECT "error" FROM `+quote(held.table))

	if len(rows.Values) != 1 {
		t.Fatalf("the history holds %d rows", len(rows.Values))
	}

	if rows.Values[0][0] != nil {
		t.Errorf("a completed run recorded its error as %q, want NULL", *rows.Values[0][0])
	}
}

func TestTheThreeWritePathsSetTheOutcomeAndTheCallerCannotOverrideIt(t *testing.T) {
	failure := errors.New("ALTER TABLE failed")

	for _, tt := range []struct {
		name    string
		write   func(sqliteHistory, history.Record) error
		outcome history.Outcome
		last    int32
		says    bool
	}{
		{name: "Record", outcome: history.Applied, write: func(h sqliteHistory,
			rec history.Record,
		) error {
			return h.Record(context.Background(), rec)
		}},
		{name: "MarkFailed", outcome: history.Failed, says: true, write: func(h sqliteHistory,
			rec history.Record,
		) error {
			return h.MarkFailed(context.Background(), rec, failure)
		}},
		{name: "MarkDirty", outcome: history.Dirty, last: 4, says: true, write: func(h sqliteHistory,
			rec history.Record,
		) error {
			return h.MarkDirty(context.Background(), rec, 4, failure)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, held := ensured(t)

			rec := run(t, 1, 1)
			rec.Outcome = history.Dirty
			rec.Error = "a caller's own idea of what happened"
			rec.LastStatement = 99

			if err := tt.write(held, rec); err != nil {
				t.Fatalf("%s: %v", tt.name, err)
			}

			got, found, err := held.Latest(t.Context())
			if err != nil || !found {
				t.Fatalf("Latest: found=%v err=%v", found, err)
			}

			assertOutcome(t, got, tt.outcome, tt.last, tt.says, failure)
		})
	}
}

// assertOutcome holds a recorded run to the outcome, error and position its write path sets.
func assertOutcome(t *testing.T, got history.Record, want history.Outcome, last int32, says bool,
	failure error,
) {
	t.Helper()

	if got.Outcome != want {
		t.Errorf("the outcome is %s, want %s", got.Outcome, want)
	}

	if got.LastStatement != last {
		t.Errorf("last_statement is %d, want %d", got.LastStatement, last)
	}

	if says && got.Error != failure.Error() {
		t.Errorf("the recorded error is %q, want %q", got.Error, failure.Error())
	}

	if !says && got.Error != "" {
		t.Errorf("a completed run recorded the error %q", got.Error)
	}
}

func TestARunMarkedAsNotCompletingMustSayWhatStoppedIt(t *testing.T) {
	_, held := ensured(t)

	if err := held.MarkFailed(t.Context(), run(t, 1, 1), nil); err == nil {
		t.Error("MarkFailed accepted a run with no failure to record")
	}

	if err := held.MarkDirty(t.Context(), run(t, 1, 1), 1, nil); err == nil {
		t.Error("MarkDirty accepted a run with no failure to record")
	}
}

func TestOneRunRecordedTwiceIsRefusedRatherThanReplacingTheEvidence(t *testing.T) {
	_, held := ensured(t)

	if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
		t.Fatalf("the first Record: %v", err)
	}

	err := held.Record(t.Context(), run(t, 1, 1))
	if !errors.Is(err, ErrAlreadyRecorded) {
		t.Fatalf("the second Record returned %v, want %v", err, ErrAlreadyRecorded)
	}

	log, err := held.List(t.Context())
	if err != nil || len(log) != 1 {
		t.Fatalf("the history holds %d runs after the refusal: %v", len(log), err)
	}

	t.Log("This one does have a code of its own: the extended SQLITE_CONSTRAINT_PRIMARYKEY, 1555,")
	t.Log("which lib.Error.Code() reports rather than the primary 19. Measured.")
}

func TestLatestIsTheMostRecentRunByTheKeyRatherThanByTheClock(t *testing.T) {
	_, held := ensured(t)

	first := run(t, 1, 1)
	first.AppliedAt = time.Date(2026, 9, 29, 23, 0, 0, 0, time.UTC)

	second := run(t, 2, 1)
	second.Version = "20260929T130000"
	second.AppliedAt = time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)

	for _, rec := range []history.Record{first, second} {
		if err := held.Record(t.Context(), rec); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	got, found, err := held.Latest(t.Context())
	if err != nil || !found {
		t.Fatalf("Latest: found=%v err=%v", found, err)
	}

	if got.Batch != 2 {
		t.Errorf("Latest returned batch %d, want 2 -- the later batch, not the later clock", got.Batch)
	}
}

func TestAHistoryWithNoRunsIsNotAHistoryWithNoTable(t *testing.T) {
	_, held := ensured(t)

	log, err := held.List(t.Context())
	if err != nil {
		t.Fatalf("List over an empty history: %v", err)
	}

	if len(log) != 0 {
		t.Errorf("an empty history listed %d runs", len(log))
	}

	_, found, err := held.Latest(t.Context())
	if err != nil {
		t.Fatalf("Latest over an empty history: %v", err)
	}

	if found {
		t.Error("an empty history reported a most recent run")
	}
}

func TestARecordedValueThisBuildCannotReadBackIsACorruptRow(t *testing.T) {
	for _, tt := range []struct {
		column string
		value  string
	}{
		{column: "direction", value: "sideways"},
		{column: "outcome", value: "probably"},
		{column: "checksum", value: "not a checksum"},
		{column: "applied_at", value: "the other day"},
	} {
		t.Run(tt.column, func(t *testing.T) {
			_, held := ensured(t)

			if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
				t.Fatalf("Record: %v", err)
			}

			exec(t, held.session, `UPDATE `+quote(held.table)+` SET `+quote(tt.column)+
				` = '`+tt.value+`'`)

			_, err := held.List(t.Context())
			if !errors.Is(err, history.ErrCorrupt) {
				t.Fatalf("List returned %v, want history.ErrCorrupt", err)
			}

			var corrupt *history.CorruptError
			if !errors.As(err, &corrupt) || corrupt.Field != tt.column {
				t.Errorf("the failure is %v, and does not name %s as the column", err, tt.column)
			}
		})
	}
}

func TestASeedIsRecordedInTheSeedTableAndNowhereElse(t *testing.T) {
	c, held := ensured(t)

	seeds, ok := c.Seeds().(sqliteHistory)
	if !ok || seeds.table != driver.DefaultSeedTable {
		t.Fatalf("Seeds returned %#v, want this package's store over %q", c.Seeds(),
			driver.DefaultSeedTable)
	}

	if err := seeds.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure the seed history: %v", err)
	}

	if err := seeds.Record(t.Context(), run(t, 2, 3).Stamp()); err != nil {
		t.Fatalf("Record a seed: %v", err)
	}

	if runs, err := seeds.List(t.Context()); err != nil || len(runs) != 1 {
		t.Errorf("the seed history holds %d runs (%v), want the one recorded", len(runs), err)
	}

	if runs, err := held.List(t.Context()); err != nil || len(runs) != 0 {
		t.Errorf("the migration history holds %d runs (%v) after a seed was recorded", len(runs), err)
		t.Log("Seeds keep a history of their own, so a seed is never reconciled as a migration and")
		t.Log("migr status never reports one.")
	}
}
