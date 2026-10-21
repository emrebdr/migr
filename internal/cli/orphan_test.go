package cli

import (
	"encoding/json"
	"strings"
	"testing"
)

// strangerSlot is a slot named like migr's for a migration this project does not carry.
const strangerSlot = "migr_20990101t000000_cccccccc"

func TestStatusNamesEveryOrphanedSlotWithWhatItHoldsAndWhatDropsIt(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows = true, true
	f.slots = map[string]bool{usersCapture: true, strangerSlot: true}

	stdout, _, err := at(t, dir, drivers, noon, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	for _, want := range []string{"Replication slots no shadow-table migration owns", usersCapture,
		"holding 3.0 MB of WAL (wal_status reserved)", "the next migr up drops it", strangerSlot,
		"SELECT pg_drop_replication_slot('" + strangerSlot + "')"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("status does not say %q:\n%s", want, stdout)
		}
	}

	doc, _, err := at(t, dir, drivers, noon, "--json", "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	var parsed struct {
		Orphans []orphanedSlot `json:"orphaned_slots"`
	}

	if err := json.Unmarshal([]byte(doc), &parsed); err != nil || len(parsed.Orphans) != 2 ||
		!parsed.Orphans[0].Ours || parsed.Orphans[1].Ours {
		t.Errorf("status --json carries %+v (%v)", parsed.Orphans, err)
	}
}

func TestUpDropsTheOrphanedSlotThisProjectLeftAndSaysSo(t *testing.T) {
	dir, f, drivers := shadowedProject(t)
	f.captures, f.shadows, f.confirmAfter = true, true, 2
	f.slots = map[string]bool{usersCapture: true, strangerSlot: true}

	stdout, stderr, err := shadowed(t, dir, drivers, "up")
	if err != nil {
		t.Fatalf("up exited %d: %v\n%s", ExitCode(err), err, stderr)
	}

	if !strings.Contains(stdout, "released the orphaned replication slot "+usersCapture) ||
		!strings.Contains(stderr, "dropped an orphaned replication slot this project left") {
		t.Errorf("up does not say it dropped the slot:\n%s\n%s", stdout, stderr)
	}

	if !f.slots[strangerSlot] {
		t.Error("up dropped a slot this project did not leave")
	}
}
