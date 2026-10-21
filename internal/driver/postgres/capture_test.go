package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capture"
)

func TestLogicalDecodingIsPermittedOnlyWithEverySettingAndPrivilegeInPlace(t *testing.T) {
	if got := (captureAnswer{level: logicalLevel, replicates: true, free: 3}).capture(); !got.Permitted {
		t.Errorf("a logical server with a replicating role and free slots was refused: %+v", got)
	}

	for name, tc := range map[string]struct {
		answer captureAnswer
		names  string
	}{
		"wal_level":  {captureAnswer{level: "replica", replicates: true, free: 3}, "wal_level = logical (it is replica"},
		"role":       {captureAnswer{level: logicalLevel, free: 3}, "REPLICATION attribute"},
		"slots used": {captureAnswer{level: logicalLevel, replicates: true}, "free replication slot"},
	} {
		t.Run(name, func(t *testing.T) {
			got := tc.answer.capture()
			if got.Permitted || !strings.Contains(got.Reason(), tc.names) {
				t.Errorf("the answer %+v was read as %+v, want a refusal naming %q", tc.answer, got, tc.names)
			}
		})
	}
}

func TestEveryMissingPrerequisiteIsNamedAtOnce(t *testing.T) {
	if got := (captureAnswer{level: "minimal"}).capture(); len(got.Missing) != 3 {
		t.Errorf("a server lacking all three was read as missing %v", got.Missing)
	}
}

func TestAPermittedServerSaysHowLongASlotKeepsItsWAL(t *testing.T) {
	unbounded := (captureAnswer{level: logicalLevel, replicates: true, free: 3, keep: "-1"}).capture()
	if !strings.Contains(unbounded.Retention, "max_slot_wal_keep_size = -1") ||
		!strings.Contains(unbounded.Retention, "fills it") {
		t.Errorf("an unbounded slot is described as %q", unbounded.Retention)
		t.Log("A slot nobody reads keeps WAL until the disk fills, and migr plan prints this line")
		t.Log("beside a shadow-table migration so the operator knows what a stopped one costs.")
	}

	bounded := (captureAnswer{level: logicalLevel, replicates: true, free: 3, keep: "10GB"}).capture()
	if !strings.Contains(bounded.Retention, "at most 10GB") || !strings.Contains(bounded.Retention, "invalidates") {
		t.Errorf("a bounded slot is described as %q", bounded.Retention)
	}
}

func TestAChangeStreamIsRefusedARequestItCannotCarry(t *testing.T) {
	for name, req := range map[string]capture.Request{
		"no name":    {Tables: []string{"people"}},
		"two tables": {Name: "migr_x", Tables: []string{"people", "teams"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := (&pgDriver{}).Capture(context.Background(), "", req); err == nil {
				t.Errorf("Capture accepted %+v", req)
			}
		})
	}
}
