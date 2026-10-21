package cli

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
)

// orphanedSlot is a change-stream resource no live shadow-table migration owns, as a report renders it.
type orphanedSlot struct {
	// Name is the resource's, a replication slot's on PostgreSQL.
	Name string `json:"name"`
	// File is the migration of this project it was made for, absent where it names none.
	File string `json:"file,omitempty"`
	// Retained is how many bytes of log the server keeps for it.
	Retained int64 `json:"retained_bytes"`
	// Status is the server's word for how that log is kept, absent where it has none.
	Status string `json:"status,omitempty"`
	// Ours reports a resource only this project's runs can have left, which migr up releases.
	Ours bool `json:"ours"`
	// Why says how it came to be left.
	Why string `json:"why"`
}

// orphansOf reads every change-stream resource no copying or syncing migration owns, none where the engine keeps none.
func orphansOf(ctx context.Context, conn driver.Conn, carried []migration.Migration,
	states []history.ShadowState) ([]orphanedSlot, error) {
	keeper, keeps := conn.(driver.CaptureKeeper)
	if !keeps {
		return nil, nil
	}

	resources, err := keeper.CaptureResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading which change-stream resources the server holds: %w", err)
	}

	var found []orphanedSlot

	for _, o := range engine.Orphans(resources, states, carried) {
		found = append(found, orphanedSlotOf(o))
	}

	return found, nil
}

// orphanedSlotOf renders one orphan.
func orphanedSlotOf(o engine.Orphan) orphanedSlot {
	return orphanedSlot{Name: o.Resource.Name, File: describeFile(o.Migration), Retained: o.Resource.Retained,
		Status: o.Resource.Status, Ours: o.Ours, Why: o.Why}
}

// writeOrphans lists every orphaned resource under a heading, each with what keeps it and what drops it.
func writeOrphans(b *strings.Builder, found []orphanedSlot) {
	if len(found) == 0 {
		return
	}

	b.WriteString("\nReplication slots no shadow-table migration owns -- each keeps the WAL since it was " +
		"last advanced until it is dropped, so one left alone fills the disk:\n")

	for _, one := range found {
		fmt.Fprintf(b, "  !  %s, holding %s%s: %s -- %s\n", one.Name, describeBytes(one.Retained),
			describeSlotStatus(one.Status), one.Why, describeOrphanRemedy(one))
	}
}

// describeSlotStatus names how the server keeps a slot's WAL, and nothing where it says nothing.
func describeSlotStatus(status string) string {
	if status == "" {
		return ""
	}

	return " (wal_status " + status + ")"
}

// describeOrphanRemedy says what drops an orphan: the next migr up for this project's, a person for anyone else's.
func describeOrphanRemedy(one orphanedSlot) string {
	if one.Ours {
		return "the next migr up drops it"
	}

	return "once nothing reads it, drop it with SELECT pg_drop_replication_slot(" + quotedLiteral(one.Name) +
		") and DROP PUBLICATION IF EXISTS " + quoted(one.Name)
}

// quotedLiteral renders a name as a string literal, the way the remedy spells it.
func quotedLiteral(name string) string {
	return "'" + strings.ReplaceAll(name, "'", "''") + "'"
}

// describeBytes renders a size in the largest unit that keeps it above one.
func describeBytes(n int64) string {
	units := []string{"bytes", "kB", "MB", "GB", "TB"}
	size, unit := float64(n), 0

	for size >= 1024 && unit < len(units)-1 {
		size /= 1024
		unit++
	}

	if unit == 0 {
		return fmt.Sprintf("%d bytes of WAL", n)
	}

	return fmt.Sprintf("%.1f %s of WAL", size, units[unit])
}

// logReleased warns of every orphaned resource a run released or had to leave, since a run must never drop one quietly.
func logReleased(log *slog.Logger) func(engine.ReleasedCapture) {
	return func(r engine.ReleasedCapture) {
		attrs := []any{"slot", r.Orphan.Resource.Name, "retained", describeBytes(r.Orphan.Resource.Retained),
			"why", r.Orphan.Why}

		if r.InUse {
			log.Warn("left an orphaned replication slot something is reading through -- the next migr up "+
				"tries again", attrs...)

			return
		}

		log.Warn("dropped an orphaned replication slot this project left", attrs...)
	}
}

// releasedSlots renders what a run released, before it ran anything.
func releasedSlots(released []engine.ReleasedCapture) []orphanedSlot {
	var found []orphanedSlot

	for _, r := range released {
		if !r.InUse {
			found = append(found, orphanedSlotOf(r.Orphan))
		}
	}

	return found
}
