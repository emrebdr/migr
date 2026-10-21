package engine

import (
	"context"
	"maps"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
)

func TestTheStreamIsAcknowledgedOnlyOnceTheTransactionRecordingThePositionHasCommitted(t *testing.T) {
	w := newWorld(7)
	newApp(w, 3, "")
	c := connect(t, w)

	if _, err := Apply(t.Context(), c, shadowRun(t, c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	checkCutOver(t, w)

	if len(w.acks) == 0 {
		t.Fatal("no position was acknowledged, so a PostgreSQL slot would hold every byte of WAL until the cutover")
	}

	if w.acksInTx > 0 {
		t.Errorf("%d acknowledgements were made inside an open transaction", w.acksInTx)
		t.Log("Measured on 17, advancing a slot inside a transaction that then rolls back stays")
		t.Log("advanced, so a position acknowledged before its commit is a change the slot forgets.")
	}

	for i := 1; i < len(w.acks); i++ {
		before, _ := strconv.Atoi(w.acks[i-1])
		after, _ := strconv.Atoi(w.acks[i])

		if after < before {
			t.Errorf("the acknowledgements went backwards: %v", w.acks)
		}
	}
}

func TestACutoverThatRolledBackReadsTheLogAgainFromWhatCommittedAndLosesNoChange(t *testing.T) {
	w := newWorld(7)
	app := newApp(w, 2, "")
	w.cutoverFails = 1
	c := connect(t, w)

	opts := shadowRun(t, c)
	opts.Backoff = Exponential{Base: time.Millisecond, Retries: 2}

	written := false
	w.at = func(point string) {
		if point == "cutover" && !written {
			written = true

			w.write(capture.Update, 3, "written-just-before-the-cutover")
		}

		app.at(point)
	}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	checkCutOver(t, w)

	if w.tables[peopleTable]["3"] != "written-just-before-the-cutover" {
		t.Errorf("row 3 holds %q after a cutover that rolled back once, want the write its catch-up read",
			w.tables[peopleTable]["3"])
		t.Log("The rolled-back catch-up undid its writes while the stream read on past them; without")
		t.Log("reading again from the recorded position the second attempt swaps in a copy missing them.")
	}

	if !slices.Contains(w.calls, "close "+shadowCapture) {
		t.Errorf("the retry did not reopen the stream: %v", w.calls)
	}
}

// keepingConn is the world's session that also lists and releases the capture resources the server holds.
type keepingConn struct {
	*worldConn
	// released names every resource it dropped.
	released []string
	// busy names a resource some connection is reading through.
	busy string
}

// CaptureResources lists every slot the world holds.
func (k *keepingConn) CaptureResources(context.Context) ([]driver.CaptureResource, error) {
	var found []driver.CaptureResource

	for _, name := range slices.Sorted(maps.Keys(k.w.slots)) {
		found = append(found, driver.CaptureResource{Name: name, Active: name == k.busy, Retained: 4096})
	}

	return found, nil
}

// ReleaseCapture drops one slot.
func (k *keepingConn) ReleaseCapture(_ context.Context, name string) error {
	k.released = append(k.released, name)
	delete(k.w.slots, name)

	return nil
}

func TestOrphansAreToldApartFromLiveCapturesAndFromStrangers(t *testing.T) {
	ours := migration.Migration{Version: versionAt(0), Name: "index_people", Ext: ".sql"}
	migrations := []migration.Migration{ours}
	live := history.ShadowState{Version: "20260101T000000", Capture: "migr_20260101t000000_aaaaaaaa", Stage: history.StageSyncing}
	cut := history.ShadowState{Version: "20260102T000000", Capture: "migr_20260102t000000_bbbbbbbb", Stage: history.StageCutOver}

	resources := []driver.CaptureResource{
		{Name: live.Capture}, {Name: cut.Capture}, {Name: shadowCapture}, {Name: "migr_20990101t000000_cccccccc"},
	}

	orphans := Orphans(resources, []history.ShadowState{live, cut}, migrations)

	got := map[string]bool{}
	for _, o := range orphans {
		got[o.Resource.Name] = o.Ours
	}

	want := map[string]bool{cut.Capture: true, shadowCapture: true, "migr_20990101t000000_cccccccc": false}
	if !maps.Equal(got, want) {
		t.Errorf("the orphans are %v, want %v", got, want)
		t.Log("A syncing state owns its slot; a cut-over state's slot and one named for a version of")
		t.Log("this project with no state are this project's own; anything else is someone else's.")
	}
}

func TestUpReleasesTheSlotsThisProjectLeftAndOnlyThose(t *testing.T) {
	w := newWorld(5)
	newApp(w, 2, "")
	k := &keepingConn{worldConn: connect(t, w)}

	w.slots[shadowCapture] = true
	w.slots["migr_20990101t000000_cccccccc"] = true

	opts := shadowRun(t, k.worldConn)

	var told []ReleasedCapture

	opts.Released = func(r ReleasedCapture) { told = append(told, r) }

	result, err := Apply(t.Context(), k, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !slices.Equal(k.released, []string{shadowCapture}) || len(told) != 1 || len(result.Released) != 1 {
		t.Errorf("the run released %v and reported %v, want only %s", k.released, told, shadowCapture)
	}

	if !w.slots["migr_20990101t000000_cccccccc"] {
		t.Error("the run dropped a slot named for a migration this project does not carry")
	}

	if !maps.Equal(w.tables[peopleTable], w.truth) {
		t.Error("the run that released the slots did not cut the migration over")
	}
}

func TestASlotSomethingIsReadingThroughIsLeftAndSaidSo(t *testing.T) {
	w := newWorld(5)
	newApp(w, 2, "")
	k := &keepingConn{worldConn: connect(t, w), busy: shadowCapture}
	w.slots[shadowCapture] = true

	opts := shadowRun(t, k.worldConn)

	var told []ReleasedCapture

	opts.Released = func(r ReleasedCapture) { told = append(told, r) }

	if _, err := Apply(t.Context(), k, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(k.released) != 0 || len(told) != 1 || !told[0].InUse {
		t.Errorf("a slot in use was released %v, reported %+v", k.released, told)
	}
}
