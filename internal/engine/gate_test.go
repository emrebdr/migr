package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// contracting is a contract step waiting on the swap the first staged migration carries.
func contracting(t *testing.T, after string) migration.Gate {
	t.Helper()

	return migration.Gate{
		Phase: migration.Phase{Change: "users.email", Name: "contract", After: after},
		Prior: migrationAt(t, 0),
	}
}

// priorIn is the history before a run, holding the prior step in one state and batch.
func priorIn(t *testing.T, state history.State, batch int64) []history.Status {
	t.Helper()

	m := migrationAt(t, 0)

	return []history.Status{{Version: m.Version, Migration: m, State: state,
		Recorded: history.Record{Batch: batch, Version: m.Version}}}
}

func TestAStepWaitsUntilItsPriorStepIsApplied(t *testing.T) {
	for _, state := range []history.State{history.StatePending, history.StateDirty} {
		held, waits := Gated(contracting(t, "swap"), priorIn(t, state, 1), nil, "r2", true)
		if !waits || !strings.Contains(held.Reason, "is not applied yet") {
			t.Errorf("a prior step %s let the contract through: %+v", state, held)
		}
	}

	if _, waits := Gated(contracting(t, "swap"), nil, nil, "r2", true); !waits {
		t.Error("a prior step the history has never seen let the contract through")
	}
}

func TestAStepWaitsForAReleaseAfterTheOneThatAppliedItsPrior(t *testing.T) {
	releases := []history.Release{{Batch: 1, Name: "r1"}}

	held, waits := Gated(contracting(t, "swap"), priorIn(t, history.StateApplied, 1), releases, "r1", true)
	if !waits || held.PriorRelease != "r1" || !strings.Contains(held.Reason, `belongs to release "r1", which is this one`) {
		t.Errorf("the deploy that swapped also contracted: %+v", held)
	}

	if _, waits := Gated(contracting(t, "swap"), priorIn(t, history.StateApplied, 1), releases, "r2",
		true); waits {
		t.Error("a later release is still held")
	}

	t.Log("migr cannot see the app, so the release is what stands for it: a step runs only under a")
	t.Log("deploy after the one that applied the step before it, once that deploy's app is serving.")
}

func TestAPriorStepAppliedBeforeReleasesWereRecordedIsAnEarlierRelease(t *testing.T) {
	if _, waits := Gated(contracting(t, "swap"), priorIn(t, history.StateApplied, 1), nil, "r2",
		true); waits {
		t.Error("a step applied before migr recorded releases holds forever, which no deploy can end")
	}
}

func TestAHoldAtASwapSaysWhatASwapIsWhereDDLCommitsAsItGoes(t *testing.T) {
	for _, c := range []struct {
		name, after   string
		transactional bool
		wants         bool
	}{
		{name: "contract", after: "swap", transactional: false, wants: true},
		{name: "swap", after: "backfill", transactional: false, wants: true},
		{name: "contract", after: "swap", transactional: true, wants: false},
		{name: "backfill", after: "expand", transactional: false, wants: false},
	} {
		gate := contracting(t, c.after)
		gate.Phase.Name = c.name

		held, _ := Gated(gate, priorIn(t, history.StatePending, 0), nil, "r1", c.transactional)
		if got := strings.Contains(held.Reason, "between its renames the column an app reads does not exist"); got != c.wants {
			t.Errorf("%s after %s (transactional %v) says the swap is split = %v, want %v", c.name, c.after,
				c.transactional, got, c.wants)
		}
	}
}

// phased stages two migrations, the second a step waiting on the first, over a session that records releases.
func phased(t *testing.T, release string) (Options, *recorder) {
	t.Helper()

	conn := newRecorder(t, true)
	opts, src := staged(t, "a", "b")
	src.ops[versionAt(0)] = ir.Operations{addIndex("first")}
	src.ops[versionAt(1)] = ir.Operations{addIndex("second")}

	first, second := migrationAt(t, 0), migrationAt(t, 1)
	opts.Phases = migration.Phases{
		first.Version:  {Change: "users.email", Name: "swap"},
		second.Version: {Change: "users.email", Name: "contract", After: "swap"},
	}
	opts.Gates = map[string]migration.Gate{second.Version: {Phase: opts.Phases[second.Version], Prior: first}}
	opts.Release = release

	return opts, conn
}

func TestARunAppliesUpToAWaitingStepAndStopsThereWithoutFailing(t *testing.T) {
	opts, conn := phased(t, "r1")

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 1 || result.Held == nil || result.Held.Migration.Version != versionAt(1) {
		t.Fatalf("the run applied %d and held %+v, want the swap applied and the contract held",
			len(result.Runs), result.Held)
	}

	again, err := Apply(t.Context(), conn, opts)
	if err != nil || len(again.Runs) != 0 || again.Held == nil {
		t.Errorf("a second run under the same release applied %d and held %+v (%v)", len(again.Runs),
			again.Held, err)
	}

	opts.Release = "r2"

	later, err := Apply(t.Context(), conn, opts)
	if err != nil || len(later.Runs) != 1 || later.Held != nil {
		t.Errorf("a run under a later release applied %d and held %+v (%v)", len(later.Runs), later.Held, err)
	}

	t.Log("A hold is the normal state of a change partway through its deploys, so the run reports it")
	t.Log("and succeeds: failing would fail every deploy until the change finished.")
}

func TestARunReachingAStepWithNoReleaseIsRefusedBeforeAnythingRuns(t *testing.T) {
	opts, conn := phased(t, "")

	result, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNoRelease) || len(result.Runs) != 0 {
		t.Errorf("Apply returned %v having applied %d, want ErrNoRelease before anything", err, len(result.Runs))
	}

	if !strings.Contains(err.Error(), migrationAt(t, 0).Filename()) {
		t.Errorf("the refusal does not name the step: %v", err)
	}
}

func TestAReleaseIsRecordedOncePerBatchWithItsFirstAppliedRow(t *testing.T) {
	opts, conn := phased(t, "r1")
	opts.Gates = nil

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(conn.store.releases) != 1 || conn.store.releases[0] != (history.Release{Batch: 1, Name: "r1",
		RecordedAt: conn.store.releases[0].RecordedAt}) {
		t.Errorf("the batch recorded %+v, want one release for batch 1", conn.store.releases)
	}

	if idx := indexOf(conn.calls, "release r1"); idx < 0 || idx > indexOf(conn.calls, "record "+versionAt(0)) ||
		indexOf(conn.calls, "commit") < idx {
		t.Errorf("the release was not written inside the first record's transaction: %v", conn.calls)
	}
}

func TestARollbackIsNeverHeld(t *testing.T) {
	opts, conn := phased(t, "r1")
	opts.Gates = nil

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	opts, _ = phased(t, "")

	if result, err := Rollback(t.Context(), conn, opts); err != nil || len(result.Runs) != 2 {
		t.Errorf("Rollback undid %d (%v), and a rollback is the operator's instruction", len(result.Runs), err)
	}
}

// indexOf returns where one call was made, and -1 where it was not.
func indexOf(calls []string, call string) int {
	for i, c := range calls {
		if c == call {
			return i
		}
	}

	return -1
}

func TestNothingAfterAWaitingStepRunsEither(t *testing.T) {
	opts, conn := phased(t, "r1")

	third := migrationAt(t, 2)
	extra, src := staged(t, "a", "b", "c")
	src.ops[versionAt(0)] = ir.Operations{addIndex("first")}
	src.ops[versionAt(1)] = ir.Operations{addIndex("second")}
	src.ops[versionAt(2)] = ir.Operations{addIndex("third")}
	extra.Phases, extra.Gates, extra.Release = opts.Phases, opts.Gates, opts.Release

	result, err := Apply(t.Context(), conn, extra)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(result.Runs) != 1 || result.Held == nil {
		t.Errorf("the run applied %d past the hold at the contract, and %s comes after it", len(result.Runs),
			third.Filename())
		t.Log("Order is kept: a migration after a waiting step may build on what that step leaves, so")
		t.Log("it waits with it rather than running ahead of it.")
	}
}
