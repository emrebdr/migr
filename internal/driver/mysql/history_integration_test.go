//go:build integration

package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// sourceDigest and irDigest are the two halves of a checksum, neither of which this store parses.
const (
	sourceDigest = "3fd4a1f57f4c1f4dc4a1b6a6b3e0c6a5d29a2f2f0b2f3d5e6a7b8c9d0e1f2a3b"
	irDigest     = "9c8b7a6f5e4d3c2b1a0f9e8d7c6b5a49382716052f3e4d5c6b7a8998aabbccdd"
)

// store returns a session's history store with its table not yet created, and removes it afterwards.
func store(t *testing.T, c *mysqlConn) mysqlHistory {
	t.Helper()

	held, ok := c.History().(mysqlHistory)
	if !ok {
		t.Fatalf("History returned %T, not this package's store", c.History())
	}

	dropLater(t, c, held.table)
	drop(t.Context(), c, held.table)

	return held
}

// ensured returns a store whose table exists, which is every write path's precondition.
func ensured(t *testing.T, c *mysqlConn) mysqlHistory {
	t.Helper()

	held := store(t, c)

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	return held
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
		AppliedAt: time.Date(2026, 9, 29, 11, 22, 33, 123456000,
			time.FixedZone("UTC+5:30", 5*3600+30*60)),
		Duration: 1500 * time.Millisecond,
		Driver:   Name,
		Runtime:  "sql",
	}
}

func TestEveryPathThatTouchesTheTableReportsErrNoTableWhenThereIsNone(t *testing.T) {
	failure := errors.New("the migration failed")

	calls := map[string]func(context.Context, mysqlHistory) error{
		"List": func(ctx context.Context, h mysqlHistory) error {
			_, err := h.List(ctx)

			return err
		},
		"Latest": func(ctx context.Context, h mysqlHistory) error {
			_, _, err := h.Latest(ctx)

			return err
		},
		"Record": func(ctx context.Context, h mysqlHistory) error {
			return h.Record(ctx, run(t, 1, 1))
		},
		"MarkFailed": func(ctx context.Context, h mysqlHistory) error {
			return h.MarkFailed(ctx, run(t, 1, 1), failure)
		},
		"MarkDirty": func(ctx context.Context, h mysqlHistory) error {
			return h.MarkDirty(ctx, run(t, 1, 1), 2, failure)
		},
	}

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		for name, call := range calls {
			t.Run(name, func(t *testing.T) {
				if err := call(t.Context(), held); !errors.Is(err, history.ErrNoTable) {
					t.Fatalf("%s returned %v, want %v", name, err, history.ErrNoTable)
				}
			})
		}

		t.Log("Read as 'no runs yet', a fully migrated production database looks virgin and the next")
		t.Log("up re-runs every migration it had already applied. ADR 0019 calls this the worst")
		t.Log("failure in the contract, and the interface shape is unchanged by getting it wrong.")
	})
}

func TestARecordSurvivesTheRoundTripFieldForField(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)
		want := run(t, 7, 3)

		if err := held.Record(t.Context(), want); err != nil {
			t.Fatalf("Record: %v", err)
		}

		got, found, err := held.Latest(t.Context())
		if err != nil || !found {
			t.Fatalf("Latest: %v (found %v)", err, found)
		}

		if got.Batch != want.Batch || got.Ordinal != want.Ordinal || got.Version != want.Version ||
			got.Name != want.Name || got.Direction != want.Direction ||
			got.Checksum != want.Checksum || got.Duration != want.Duration ||
			got.Driver != want.Driver || got.Runtime != want.Runtime {
			t.Errorf("read back\n%+v\nwant\n%+v", got, want)
		}

		if !got.AppliedAt.Equal(want.AppliedAt) {
			t.Errorf("the instant read back as %s, want %s", got.AppliedAt, want.AppliedAt)
		}

		if got.Outcome != history.Applied {
			t.Errorf("Record wrote outcome %q", got.Outcome)
		}

		if got.EngineVersion == "" || got.IRVersion == 0 {
			t.Errorf("Stamp left the build unrecorded: %+v", got)
		}

		t.Log("The instant is written in a zone five and a half hours from UTC and comes back as the")
		t.Log("same moment: it is rendered as UTC text, the session's time_zone is +00:00, and the")
		t.Log("column keeps microseconds.")
	})
}

func TestANanosecondInstantIsRecordedToTheMicrosecondBothEnginesKeep(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		rec := run(t, 1, 1)
		rec.AppliedAt = time.Date(2026, 9, 29, 11, 22, 33, 123456789, time.UTC)

		if err := held.Record(t.Context(), rec); err != nil {
			t.Fatalf("Record: %v", err)
		}

		got, _, err := held.Latest(t.Context())
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}

		want := rec.AppliedAt.Truncate(time.Microsecond)
		if !got.AppliedAt.Equal(want) {
			t.Errorf("the instant read back as %s, want %s", got.AppliedAt, want)
		}

		t.Log("Truncated here rather than handed to the server: given nine digits MySQL rounds them")
		t.Log("up and MariaDB truncates them, so the same instant would be recorded as two different")
		t.Log("microseconds on the two engines. Measured on both.")
	})
}

func TestAnErrorlessRunRecordsNullRatherThanAnEmptyString(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
			t.Fatalf("Record: %v", err)
		}

		if got := scalar(t, c, "SELECT `error` IS NULL FROM "+quote(held.table)); got != "1" {
			t.Errorf("the error column is not null on a run that had none")
		}
	})
}

func TestTheThreeWritePathsSetTheOutcomeAndTheCallerCannotOverrideIt(t *testing.T) {
	failure := errors.New("the migration failed")

	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		dirty := run(t, 1, 1)
		dirty.Outcome = history.Applied
		dirty.Error = "a lie the caller left behind"
		dirty.LastStatement = 99

		if err := held.MarkDirty(t.Context(), dirty, 3, failure); err != nil {
			t.Fatalf("MarkDirty: %v", err)
		}

		failed := run(t, 1, 2)
		failed.Error = "another lie"
		failed.LastStatement = 99

		if err := held.MarkFailed(t.Context(), failed, failure); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}

		applied := run(t, 1, 3)
		applied.Error = "a third"
		applied.LastStatement = 99

		if err := held.Record(t.Context(), applied); err != nil {
			t.Fatalf("Record: %v", err)
		}

		records, err := held.List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		if len(records) != 3 {
			t.Fatalf("List returned %d runs, want 3", len(records))
		}

		if records[0].Outcome != history.Dirty || records[0].LastStatement != 3 ||
			records[0].Error != failure.Error() {
			t.Errorf("MarkDirty wrote %+v", records[0])
		}

		if records[1].Outcome != history.Failed || records[1].LastStatement != 0 ||
			records[1].Error != failure.Error() {
			t.Errorf("MarkFailed wrote %+v", records[1])
		}

		if records[2].Outcome != history.Applied || records[2].LastStatement != 0 ||
			records[2].Error != "" {
			t.Errorf("Record wrote %+v", records[2])
		}
	})
}

func TestARunMarkedAsNotCompletingMustSayWhatStoppedIt(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		if err := held.MarkFailed(t.Context(), run(t, 1, 1), nil); !errors.Is(err, ErrHistory) {
			t.Errorf("MarkFailed(nil) returned %v", err)
		}

		if err := held.MarkDirty(t.Context(), run(t, 1, 1), 1, nil); !errors.Is(err, ErrHistory) {
			t.Errorf("MarkDirty(nil) returned %v", err)
		}
	})
}

func TestOneRunRecordedTwiceIsRefusedRatherThanReplacingTheEvidence(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
			t.Fatalf("Record: %v", err)
		}

		second := run(t, 1, 1)
		second.Version = "20260930T090000"

		err := held.Record(t.Context(), second)
		if !errors.Is(err, ErrAlreadyRecorded) {
			t.Fatalf("a duplicate run returned %v, want %v", err, ErrAlreadyRecorded)
		}

		if got := scalar(t, c, "SELECT `version` FROM "+quote(held.table)); got != "20260929T120000" {
			t.Errorf("the recorded run is now %q", got)
		}
	})
}

func TestAWriteThatFailsOnTheTablesShapeIsExplainedByTheCatalog(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		exec(t, c, "ALTER TABLE "+quote(held.table)+" DROP COLUMN `engine_version`")

		err := held.Record(t.Context(), run(t, 1, 1))
		if !errors.Is(err, ErrHistoryShape) {
			t.Fatalf("Record against a table missing a column returned %v, want %v", err,
				ErrHistoryShape)
		}

		if !strings.Contains(err.Error(), "engine_version") {
			t.Errorf("the failure does not name the column that is not there: %v", err)
		}

		t.Log("MySQL numbers a missing table and a missing column apart -- 1146 and 1054 -- so the")
		t.Log("first is classified from the failure alone. The second is not: 1054 says only that a")
		t.Log("column was unknown, and which one this build needed is what the catalog answers. That")
		t.Log("tail of explainWrite is what this drives, and it runs only after a write has failed.")
	})
}

func TestLatestIsTheMostRecentRunByTheKeyRatherThanByTheClock(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		early := run(t, 2, 1)
		early.Version = "20260101T000000"
		early.AppliedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

		if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
			t.Fatalf("Record: %v", err)
		}

		if err := held.Record(t.Context(), early); err != nil {
			t.Fatalf("Record: %v", err)
		}

		got, found, err := held.Latest(t.Context())
		if err != nil || !found {
			t.Fatalf("Latest: %v (found %v)", err, found)
		}

		if got.Batch != 2 || got.Ordinal != 1 {
			t.Errorf("Latest is batch %d ordinal %d, want 2/1", got.Batch, got.Ordinal)
		}
	})
}

func TestAHistoryWithNoRunsIsNotAHistoryWithNoTable(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := ensured(t, c)

		records, err := held.List(t.Context())
		if err != nil {
			t.Fatalf("List over an empty history: %v", err)
		}

		if len(records) != 0 {
			t.Errorf("List returned %d runs", len(records))
		}

		_, found, err := held.Latest(t.Context())
		if err != nil {
			t.Fatalf("Latest over an empty history: %v", err)
		}

		if found {
			t.Error("Latest found a run in an empty history")
		}
	})
}

func TestARecordedValueThisBuildCannotReadBackIsACorruptRow(t *testing.T) {
	for name, c := range map[string]struct{ column, value string }{
		"a direction":   {"direction", "sideways"},
		"an outcome":    {"outcome", "probably"},
		"a checksum":    {"checksum", "not-a-checksum"},
		"an applied_at": {"applied_at", "1970-01-01 00:00:00.000000"},
	} {
		t.Run(name, func(t *testing.T) {
			eachServer(t, func(t *testing.T, s server, session *mysqlConn) {
				held := ensured(t, session)

				if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
					t.Fatalf("Record: %v", err)
				}

				if c.column == "applied_at" {
					exec(t, session, "ALTER TABLE "+quote(held.table)+
						" MODIFY COLUMN `applied_at` varchar(64) NOT NULL")
					exec(t, session, "UPDATE "+quote(held.table)+" SET `applied_at` = 'not a time'")
				} else {
					exec(t, session, "UPDATE "+quote(held.table)+" SET "+quote(c.column)+
						" = "+quoteLiteral(c.value))
				}

				_, err := held.List(t.Context())

				var corrupt *history.CorruptError
				if !errors.As(err, &corrupt) {
					t.Fatalf("List returned %v, want a CorruptError", err)
				}

				if corrupt.Field != c.column {
					t.Errorf("the corrupt row names %q, want %q", corrupt.Field, c.column)
				}
			})
		})
	}
}
