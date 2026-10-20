//go:build integration

package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/history"
)

func TestAReleaseIsRecordedBesideTheHistoryAndReadBack(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		store := session.History().(history.Releaser)

		if got, err := store.Releases(t.Context()); err != nil || got != nil {
			t.Fatalf("a database with no release table answered %v, %v", got, err)
		}

		if err := session.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		want := history.Release{Batch: 3, Name: "build-42",
			RecordedAt: time.Date(2026, 10, 20, 9, 30, 0, 123456000, time.UTC)}

		if err := session.Transact(t.Context(), func(ctx context.Context) error {
			return store.RecordRelease(ctx, want)
		}); err != nil {
			t.Fatalf("RecordRelease: %v", err)
		}

		got, err := store.Releases(t.Context())
		if err != nil || len(got) != 1 || got[0] != want {
			t.Errorf("the release read back as %+v, %v, want %+v", got, err, want)
		}

		if _, present := introspect(t, session).Table(history.ReleaseTable(session.history.table)); present {
			t.Error("the snapshot holds the release table, which every migr diff would propose dropping")
		}
	})
}
