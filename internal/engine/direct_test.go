package engine

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// sameQueries compares two statement lists, since a driver.Query holds a slice and is not comparable.
func sameQueries(got, want []driver.Query) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i].SQL != want[i].SQL || !slices.Equal(got[i].Args, want[i].Args) {
			return false
		}
	}

	return true
}

// backfill is the escape-hatch statement a staged migration sends itself, through host.db.exec.
var backfill = driver.Query{SQL: "UPDATE users SET status = 'active' WHERE status IS NULL"}

func TestAPreviewCarriesOutTheStatementsAMigrationRunsItself(t *testing.T) {
	opts, src := staged(t, "abc")
	src.direct[versionAt(0)] = []driver.Query{backfill}

	previewed, err := Preview(t.Context(), newRecorder(t, true), opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if len(previewed.Steps) != 1 {
		t.Fatalf("the preview covers %d migrations, want one", len(previewed.Steps))
	}

	t.Log("host.Call.Statements() is what a preview held rather than ran, and a plan that omits")
	t.Log("it is a preview that lies by omission about a statement migr up commits on its own.")

	if got := previewed.Steps[0].Direct; !sameQueries(got, []driver.Query{backfill}) {
		t.Errorf("the preview carried out %v, want the statement the migration would send", got)
	}
}

func TestAnAppliedMigrationHoldsNoStatementBecauseItRanIt(t *testing.T) {
	opts, src := staged(t, "abc")
	src.direct[versionAt(0)] = []driver.Query{backfill}

	if _, err := Apply(t.Context(), newRecorder(t, true), opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if !slices.Contains(src.modes, ModeApply) {
		t.Fatalf("an apply asked in %v, want %s", src.modes, ModeApply)
	}

	t.Log("The escape hatch is the one callback plan mode changes (ADR 0032): under apply it")
	t.Log("executes, so there is nothing to hold and nothing for a report to carry out.")
}

func TestADerivedDownIsRefusedWhenTheUpReachedTheDatabaseItself(t *testing.T) {
	opts, src := staged(t, "abc")
	src.direct[versionAt(0)] = []driver.Query{backfill}

	conn := newRecorder(t, true)
	carried(t, conn, 0)

	_, err := Rollback(t.Context(), conn, opts)
	if !errors.Is(err, ir.ErrIrreversible) {
		t.Fatalf("Rollback returned %v, want it to read as an irreversible migration", err)
	}

	var refused *DirectIrreversibleError
	if !errors.As(err, &refused) {
		t.Fatalf("Rollback returned %v, want a DirectIrreversibleError", err)
	}

	if !sameQueries(refused.Direct, []driver.Query{backfill}) {
		t.Errorf("the refusal carries %v, want the statement it cannot invert", refused.Direct)
	}
}

func TestADerivedDownRefusedForItsOwnStatementsRunsNothingAtAll(t *testing.T) {
	opts, src := staged(t, "abc")
	src.direct[versionAt(0)] = []driver.Query{backfill}

	conn := newRecorder(t, true)
	carried(t, conn, 0)

	if _, err := Rollback(t.Context(), conn, opts); err == nil {
		t.Fatal("the rollback derived an undo for a migration that reached the database itself")
	}

	t.Log("A rollback runs newest first, so one that stopped partway would leave migrations")
	t.Log("whose successors are already undone. The whole of it is refused before anything runs.")

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "exec ") {
			t.Errorf("the refused rollback executed something: %v", conn.calls)

			break
		}
	}
}

func TestADerivedDownIsUnaffectedByStatementsAnAppliedRunAlreadyExecuted(t *testing.T) {
	opts, src := staged(t, "abc")

	conn := newRecorder(t, true)
	carried(t, conn, 0)

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if !slices.Contains(src.modes, ModePlan) {
		t.Errorf("the derivation asked in %v, want it to ask as a question", src.modes)
	}
}
