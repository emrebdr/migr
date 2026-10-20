//go:build integration

package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/history"
)

func TestAReleaseIsRecordedBesideTheHistoryAndReadBackOnBothServers(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		held := c.History().(mysqlHistory)
		dropLater(t, c, held.table, held.progressTable(), held.releaseTable())
		drop(t.Context(), c, held.table, held.progressTable(), held.releaseTable())

		if got, err := held.Releases(t.Context()); err != nil || got != nil {
			t.Fatalf("a database with no release table answered %v, %v", got, err)
		}

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		want := history.Release{Batch: 3, Name: "build-42",
			RecordedAt: time.Date(2026, 10, 20, 9, 30, 0, 123456000, time.UTC)}

		if err := c.Transact(t.Context(), func(ctx context.Context) error {
			return held.RecordRelease(ctx, want)
		}); err != nil {
			t.Fatalf("RecordRelease: %v", err)
		}

		got, err := held.Releases(t.Context())
		if err != nil || len(got) != 1 || got[0].Batch != 3 || got[0].Name != "build-42" ||
			!got[0].RecordedAt.Equal(want.RecordedAt) {
			t.Errorf("the release read back as %+v, %v, want %+v", got, err, want)
		}

		if _, present := introspect(t, c).Table(held.releaseTable()); present {
			t.Error("the snapshot holds the release table, which every migr diff would propose dropping")
		}
	})
}
