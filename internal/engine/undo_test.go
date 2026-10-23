package engine

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

func TestAnAuthoredDownThatEmitsNothingRollsBackAndRecordsItsRow(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")
	src.down[versionAt(0)] = ir.Operations{}

	result, err := Rollback(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if len(result.Runs) != 1 || result.Runs[0].Statements != 0 {
		t.Fatalf("the rollback reported %+v, want one run of no statements", result.Runs)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec") {
			t.Errorf("an empty down sent %q", call)
		}
	}

	written := conn.store.runs[1:]
	if len(written) != 1 || written[0].Direction != ir.Down || written[0].Outcome != history.Applied {
		t.Fatalf("the rollback wrote %+v, want one applied down", written)
	}

	t.Log("The backfill phase of an expand/contract change has nothing of its own to undo: the")
	t.Log("expand's down drops the column it filled. Its author says so by emitting nothing, and the")
	t.Log("migration is still recorded rolled back, or the next up would find it applied.")
}

func TestAnUpThatEmitsNothingIsStillRefused(t *testing.T) {
	conn := newRecorder(t, true)
	opts, src := staged(t, "")
	src.down[versionAt(0)] = ir.Operations{}

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrNothingToRun) {
		t.Fatalf("Apply returned %v, want ErrNothingToRun", err)
	}
}

func TestADerivedDownOfAnUpThatNowEmitsNothingIsRefused(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "")

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ErrNothingToRun) {
		t.Fatalf("Rollback returned %v, want ErrNothingToRun", err)
	}

	if len(conn.store.runs) != 1 {
		t.Errorf("the refused rollback wrote %d rows", len(conn.store.runs)-1)
	}

	t.Log("A migration that branches on live data can emit nothing when asked again. Deriving an")
	t.Log("undo from that would record a rollback of a change nobody undid -- only an author may")
	t.Log("say there is nothing to undo.")
}

func TestADownWhoseOperationsCompileToNothingIsStillRefused(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")
	src.down[versionAt(0)] = ir.Operations{ir.RenameTable{From: stagedTable, To: "elsewhere"}}

	if _, err := Rollback(t.Context(), conn, opts); !errors.Is(err, plan.ErrNothingCompiled) {
		t.Fatalf("Rollback returned %v, want plan.ErrNothingCompiled", err)
	}

	t.Log("An emitted operation that compiles to no statement is a driver reporting success for")
	t.Log("work it did not do, which is a different thing from a down that emits nothing at all.")
}
