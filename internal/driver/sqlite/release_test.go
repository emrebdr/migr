package sqlite

import (
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

func TestAReleaseSurvivesTheRoundTripOnePerBatch(t *testing.T) {
	_, held := ensured(t)

	at := time.Date(2026, 10, 20, 9, 30, 0, 123456789, time.FixedZone("CEST", 2*60*60))

	for _, r := range []history.Release{{Batch: 2, Name: "build-42", RecordedAt: at},
		{Batch: 1, Name: "build-41", RecordedAt: at}} {
		if err := held.RecordRelease(t.Context(), r); err != nil {
			t.Fatalf("RecordRelease: %v", err)
		}
	}

	got, err := held.Releases(t.Context())
	if err != nil {
		t.Fatalf("Releases: %v", err)
	}

	if len(got) != 2 || got[0].Batch != 1 || got[1].Name != "build-42" || !got[1].RecordedAt.Equal(at) ||
		got[1].RecordedAt.Location() != time.UTC {
		t.Fatalf("the releases read back as %+v, want both in batch order and the instant in UTC", got)
	}

	if err := held.RecordRelease(t.Context(), history.Release{Batch: 1, Name: "again", RecordedAt: at}); err == nil {
		t.Error("a second release for one batch was accepted, and a batch is one deploy")
	}
}

func TestReadingReleasesCreatesNothingOnADatabaseWithoutTheTable(t *testing.T) {
	_, held := store(t)

	if got, err := held.Releases(t.Context()); err != nil || got != nil {
		t.Fatalf("a database with no release table answered %v, %v", got, err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.releaseTable()); found {
		t.Error("reading the releases created the table, which a report never may")
	}
}

func TestEnsureAddsTheReleaseTableToAHistoryThatPredatesIt(t *testing.T) {
	c, held := ensured(t)
	apply(t, c, ir.DropTable{Table: held.releaseTable()})

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure over a history with no release table: %v", err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.releaseTable()); !found {
		t.Error("Ensure did not create the release table beside a history that already existed")
		t.Log("Every database migrated before this build has a history and no release table, so")
		t.Log("creating it only alongside a new history would leave none of them able to gate a step.")
	}
}
