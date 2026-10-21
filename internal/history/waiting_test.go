package history

import (
	"context"
	"testing"
	"time"
)

// heldAt returns the state of a shadow-table migration of a file, at one stage.
func heldAt(f File, stage Stage) ShadowState {
	return ShadowState{Version: f.migration.Version, Table: "users", Shadow: "_users_new",
		Retired: "_users_old", Capture: "migr_slot", Stage: stage, UpdatedAt: ran}
}

// resolveHeld folds runs against one file through a log that knows where its shadow tables stand.
func resolveHeld(t *testing.T, f File, states []ShadowState, runs ...Record) State {
	t.Helper()

	statuses, ok := Reconcile([]File{f}, NewLog(runs).WithShadows(states)).Resolved()
	if !ok || len(statuses) != 1 {
		t.Fatalf("reconciling one file produced %d statuses (present %v), want 1", len(statuses), ok)
	}

	return statuses[0].State
}

func TestADirtyUpWhoseShadowTableIsHeldInSyncIsWaiting(t *testing.T) {
	users := file(30, "index_users", "-- users")

	got := resolveHeld(t, users, []ShadowState{heldAt(users, StageSyncing)}, up(users, 1, Dirty))
	if got != StateWaiting {
		t.Errorf("a dirty up held in sync resolved to %s, want %s", got, StateWaiting)
		t.Log("migr status has to print something true for a migration waiting on a person, and")
		t.Log("dirty says the schema is between states, which a held copy is not (M13-T14).")
	}
}

func TestOnlyAHeldCopyWaitsAndEveryOtherStoppedRunStaysDirty(t *testing.T) {
	users := file(30, "index_users", "-- users")

	cases := []struct {
		name   string
		states []ShadowState
		runs   []Record
		want   State
	}{
		{name: "still copying", states: []ShadowState{heldAt(users, StageCopying)},
			runs: []Record{up(users, 1, Dirty)}, want: StateDirty},
		{name: "no shadow table at all", runs: []Record{up(users, 1, Dirty)}, want: StateDirty},
		{name: "a stopped rollback", states: []ShadowState{heldAt(users, StageSyncing)},
			runs: []Record{up(users, 1, Applied), down(users, 2, Dirty)}, want: StateDirty},
		{name: "cut over", states: []ShadowState{heldAt(users, StageCutOver)},
			runs: []Record{up(users, 1, Applied)}, want: StateApplied},
		{name: "applied though a row still says syncing", states: []ShadowState{heldAt(users, StageSyncing)},
			runs: []Record{up(users, 1, Applied)}, want: StateApplied},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveHeld(t, users, c.states, c.runs...); got != c.want {
				t.Errorf("resolved to %s, want %s", got, c.want)
			}
		})
	}
}

func TestAnotherMigrationsHeldCopyDoesNotMakeThisOneWait(t *testing.T) {
	users, orders := file(30, "index_users", "-- users"), file(31, "index_orders", "-- orders")

	got := resolveHeld(t, users, []ShadowState{heldAt(orders, StageSyncing)}, up(users, 1, Dirty))
	if got != StateDirty {
		t.Errorf("a dirty up resolved to %s because another migration's copy is held", got)
	}
}

// heldStore is a history whose read half also remembers where shadow-table migrations stand.
type heldStore struct {
	// runs is what List returns.
	runs []Record
	// states is what ShadowStates returns.
	states []ShadowState
}

// List returns the runs.
func (s heldStore) List(context.Context) ([]Record, error) { return s.runs, nil }

// BeginShadow is never called here.
func (heldStore) BeginShadow(context.Context, ShadowState) error { return nil }

// SaveShadow is never called here.
func (heldStore) SaveShadow(context.Context, ShadowState) error { return nil }

// ConfirmShadow is never called here.
func (heldStore) ConfirmShadow(context.Context, string) error { return nil }

// ShadowStates returns the states.
func (s heldStore) ShadowStates(context.Context) ([]ShadowState, error) { return s.states, nil }

// ClearShadow is never called here.
func (heldStore) ClearShadow(context.Context, string) error { return nil }

func TestReadingAStoreThatRemembersShadowTablesFindsTheWaitingOne(t *testing.T) {
	users := file(30, "index_users", "-- users")
	store := heldStore{runs: []Record{up(users, 1, Dirty)}, states: []ShadowState{heldAt(users, StageSyncing)}}

	log, err := Read(context.Background(), store)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	statuses, _ := Reconcile([]File{users}, log).Resolved()
	if len(statuses) != 1 || statuses[0].State != StateWaiting {
		t.Errorf("a log read from a store holding a synced copy resolved to %v", statuses)
		t.Log("Reconcile reads only the log, so the shadow states have to arrive with it, or")
		t.Log("every report built on Read would print a waiting migration as dirty.")
	}
}

func TestARecordedLagSurvivesItsStoredMillisecondsAndCannotBeNegative(t *testing.T) {
	lag := 2750 * time.Millisecond
	if got := BehindOf(BehindMillis(lag)); got != lag {
		t.Errorf("a lag of %s is stored and read back as %s", lag, got)
	}

	state := heldAt(file(30, "index_users", "-- users"), StageSyncing)
	state.Behind = -time.Second

	if err := state.Validate(); err == nil {
		t.Error("a state recording a negative lag validated")
	}
}
