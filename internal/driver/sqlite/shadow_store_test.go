package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// stateAt is a shadow-table state at a stage, as a run records it.
func stateAt(stage history.Stage, position string) history.ShadowState {
	return history.ShadowState{Version: "20261020T130000", Table: "people", Shadow: "_people_migr_new",
		Retired: "_people_migr_old", Capture: "migr_capture", Stage: stage, Cursor: []string{"7"},
		Position: position, Copied: 7, Applied: 2,
		UpdatedAt: time.Date(2026, 10, 20, 9, 30, 0, 0, time.FixedZone("CEST", 2*60*60))}
}

func TestAShadowStateSurvivesTheRoundTripAndASaveLeavesItsConfirmationAlone(t *testing.T) {
	_, held := ensured(t)
	ctx := t.Context()

	if err := held.BeginShadow(ctx, stateAt(history.StageCopying, "10")); err != nil {
		t.Fatalf("BeginShadow: %v", err)
	}

	if err := held.ConfirmShadow(ctx, "20261020T130000"); err != nil {
		t.Fatalf("ConfirmShadow: %v", err)
	}

	synced := stateAt(history.StageSyncing, "12")
	synced.Behind = 1500 * time.Millisecond

	if err := held.SaveShadow(ctx, synced); err != nil {
		t.Fatalf("SaveShadow: %v", err)
	}

	got, err := held.ShadowStates(ctx)
	if err != nil || len(got) != 1 {
		t.Fatalf("ShadowStates: %v, %v", got, err)
	}

	if s := got[0]; s.Stage != history.StageSyncing || s.Position != "12" || !s.Confirmed || s.Behind != synced.Behind ||
		len(s.Cursor) != 1 || s.Cursor[0] != "7" || s.UpdatedAt.Location() != time.UTC {
		t.Errorf("the state read back as %+v, want it saved with its confirmation kept", s)
	}

	if err := held.ClearShadow(ctx, "20261020T130000"); err != nil {
		t.Fatalf("ClearShadow: %v", err)
	}

	if got, _ := held.ShadowStates(ctx); len(got) != 0 {
		t.Errorf("a cleared state is still there: %+v", got)
	}
}

func TestReadingShadowStatesCreatesNothingOnADatabaseWithoutTheTable(t *testing.T) {
	_, held := store(t)

	if got, err := held.ShadowStates(t.Context()); err != nil || got != nil {
		t.Fatalf("a database with no shadow-state table answered %v, %v", got, err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.shadowTable()); found {
		t.Error("reading the shadow states created the table, which a report never may")
	}
}

func TestTheShadowStateTableIsNeverIntrospected(t *testing.T) {
	c, _ := ensured(t)

	snapshot, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	if _, seen := snapshot.Table(history.ShadowTable(c.table)); seen {
		t.Error("Introspect reported the shadow-state table, which migr diff would propose dropping")
	}
}

func TestSQLiteDeclaresNoCaptureAndOpensNoStream(t *testing.T) {
	c, _ := ensured(t)

	if fact := c.Capabilities().Capture(); fact.Permitted || !strings.Contains(fact.Reason(), "replication log") {
		t.Errorf("SQLite declares capture as %+v", fact)
	}

	_, err := (&sqliteDriver{}).Capture(context.Background(), "", capture.Request{})
	if !errors.Is(err, capture.ErrUnsupported) {
		t.Errorf("Capture returned %v", err)
	}
}

func TestEnsureAddsTheShadowStateTableToAHistoryThatPredatesIt(t *testing.T) {
	c, held := ensured(t)
	apply(t, c, ir.DropTable{Table: held.shadowTable()})

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure over a history with no shadow-state table: %v", err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.shadowTable()); !found {
		t.Error("Ensure did not create the shadow-state table beside a history that already existed")
	}
}
