package engine

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/history"
)

// detached runs the staged shadow-table migration and cancels it once the hold has read the log three times.
func detached(t *testing.T, w *world, c *worldConn) (Options, error) {
	t.Helper()

	app := newApp(w, 0, "")

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := shadowRun(t, c)
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding && app.polls == 3 {
			cancel()
		}
	}

	_, err := Apply(ctx, c, opts)

	return opts, err
}

// reconciledState is what the staged migration reconciles to against the world's history and shadow state.
func reconciledState(t *testing.T, w *world, opts Options) history.State {
	t.Helper()

	states := slices.Collect(func(yield func(history.ShadowState) bool) {
		for _, s := range w.states {
			if !yield(s) {
				return
			}
		}
	})

	statuses, _ := history.Reconcile(opts.Migrations, history.NewLog(w.runs).WithShadows(states)).Resolved()
	if len(statuses) != 1 {
		t.Fatalf("the staged project reconciled to %d statuses", len(statuses))
	}

	return statuses[0].State
}

func TestCtrlCDuringTheHoldLeavesTheMigrationWaitingRatherThanFailed(t *testing.T) {
	w := newWorld(5)
	c := connect(t, w)

	opts, err := detached(t, w, c)
	if !errors.Is(err, ErrAwaitingCutover) || !errors.Is(err, context.Canceled) {
		t.Fatalf("the detached run returned %v, want ErrAwaitingCutover over the cancellation", err)
	}

	var waiting *WaitingError
	if !errors.As(err, &waiting) || waiting.State.Stage != history.StageSyncing || waiting.State.Shadow != shadowCopy {
		t.Errorf("the detached run reported %+v", waiting)
	}

	if slices.Contains(w.calls, "drop-stream "+shadowCapture) || !w.slots[shadowCapture] {
		t.Errorf("detaching released the capture: %v", w.calls)
	}

	if got := reconciledState(t, w, opts); got != history.StateWaiting {
		t.Errorf("a detached hold reconciles to %s, want %s", got, history.StateWaiting)
	}
}

func TestCtrlCWhileCopyingIsAStoppedRunAndNothingWaits(t *testing.T) {
	w := newWorld(7)
	newApp(w, 0, "copy#2")
	w.cause = context.Canceled
	c := connect(t, w)

	opts := shadowRun(t, c)

	_, err := Apply(t.Context(), c, opts)
	if errors.Is(err, ErrAwaitingCutover) {
		t.Fatalf("a run canceled while copying reported waiting for a cutover: %v", err)
	}

	var stopped *ShadowError
	if !errors.As(err, &stopped) || !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled copy returned %v", err)
	}

	if got := reconciledState(t, w, opts); got != history.StateDirty {
		t.Errorf("a copy stopped partway reconciles to %s, want %s", got, history.StateDirty)
	}
}

func TestARollbackIsRefusedWhileAMigrationWaitsForItsCutover(t *testing.T) {
	w := newWorld(5)
	c := connect(t, w)

	opts, err := detached(t, w, c)
	if !errors.Is(err, ErrAwaitingCutover) {
		t.Fatalf("staging a waiting migration: %v", err)
	}

	w.restart()

	if _, err := Rollback(t.Context(), c, opts); !errors.Is(err, ErrWaiting) {
		t.Errorf("a rollback over a waiting migration returned %v, want ErrWaiting", err)
	}

	if _, kept := w.tables[shadowCopy]; !kept || !w.slots[shadowCapture] {
		t.Error("the refused rollback touched the copy or the capture")
	}
}

func TestTheHoldRecordsTheLagItMeasuredWhereAnotherProcessCanReadIt(t *testing.T) {
	w := newWorld(6)
	w.batch = 1
	app := newApp(w, 8, "")
	c := connect(t, w)

	recorded := false

	opts := shadowRun(t, c)
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding && p.Behind > 0 && w.states[shadowVersion].Behind == p.Behind {
			recorded = true
		}
	}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !recorded || app.polls < 8 {
		t.Errorf("no read of the held log recorded the lag it measured, over %d polls", app.polls)
		t.Log("migr cutover runs in another process, so a lag held only in memory is a lag no")
		t.Log("operator asked to confirm the swap can see (M13-T14).")
	}
}

func TestAnIdleHoldWritesItsHeartbeatEveryFewSecondsAndNotOnEveryRead(t *testing.T) {
	w := newWorld(3)
	app := newApp(w, 40, "")
	c := connect(t, w)

	idleSaves, cutting := 0, false
	w.at = func(point string) {
		cutting = cutting || point == "cutover"

		if point == "save" && !cutting && app.polls > 3 && w.states[shadowVersion].Stage == history.StageSyncing {
			idleSaves++
		}

		app.at(point)
	}

	at := start
	opts := shadowRun(t, c)
	opts.Clock = func() time.Time {
		at = at.Add(time.Second)

		return at
	}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if idleSaves == 0 || idleSaves >= app.polls/2 {
		t.Errorf("an idle hold of %d reads saved its state %d times, want a heartbeat every %s",
			app.polls, idleSaves, ShadowHeartbeat)
		t.Log("Without a heartbeat, a holder that read nothing new looks exactly like one that died;")
		t.Log("with a save on every read, a hold of days is a write a second to the history.")
	}
}

func TestUnattendedCutsOverOnceCaughtUpWithNothingConfirmed(t *testing.T) {
	w := newWorld(6)
	w.batch = 1
	newApp(w, 0, "")
	c := connect(t, w)

	var heldReads []int

	opts := shadowRun(t, c)
	opts.Unattended = true
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding {
			heldReads = append(heldReads, p.Batch)
		}
	}

	result, err := Apply(t.Context(), c, opts)
	if err != nil {
		t.Fatalf("an unattended run returned %v", err)
	}

	checkCutOver(t, w)

	if w.states[shadowVersion].Confirmed {
		t.Error("the unattended run wrote a confirmation, which only a person gives")
	}

	if len(result.Runs) != 1 || result.Runs[0].Shadow == nil || !result.Runs[0].Shadow.Unattended {
		t.Errorf("the run does not say it cut over unattended: %+v", result.Runs)
	}

	if len(heldReads) == 0 || heldReads[len(heldReads)-1] != 0 || !slices.ContainsFunc(heldReads,
		func(n int) bool { return n > 0 }) {
		t.Errorf("the held reads applied %v changes, want reads until one found nothing new", heldReads)
		t.Log("Unattended skips the confirmation, not the catching up: the swap catches up under its")
		t.Log("lock, so cutting over while the stream still trails holds that lock for longer.")
	}
}

func TestResolveRolledBackAbandonsAMigrationWaitingForItsCutover(t *testing.T) {
	w := newWorld(5)
	c := connect(t, w)

	opts, err := detached(t, w, c)
	if !errors.Is(err, ErrAwaitingCutover) {
		t.Fatalf("staging a waiting migration: %v", err)
	}

	w.restart()

	settled, err := Resolve(t.Context(), c, opts, Repair{Mode: RepairRolledBack, Reason: "abandoned"})
	if err != nil {
		t.Fatalf("Resolve over a waiting migration: %v", err)
	}

	if settled.Abandons == nil || settled.Abandons.Shadow != shadowCopy {
		t.Errorf("the resolution reported abandoning %+v", settled.Abandons)
	}

	if _, left := w.tables[shadowCopy]; left || len(w.slots) != 0 || len(w.states) != 0 {
		t.Errorf("abandoning a waiting migration left the slots %v and the states %v", w.slots, w.states)
	}
}
