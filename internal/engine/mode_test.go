package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestAnApplyAsksEveryMigrationAsARun(t *testing.T) {
	conn := newRecorder(t, true)
	opts, src := staged(t, "ab", "cd")

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []Mode{ModeApply, ModeApply}
	if !slices.Equal(src.modes, want) {
		t.Errorf("the source was asked in %v, want %v", src.modes, want)
	}
}

func TestAPreviewAsksEveryMigrationAsAQuestion(t *testing.T) {
	for _, dir := range []ir.Direction{ir.Up, ir.Down} {
		conn := newRecorder(t, true)
		carried(t, conn, 0)

		opts, src := staged(t, "ab")
		src.down[versionAt(0)] = ir.Operations{addIndex("z9")}

		if _, err := Preview(t.Context(), conn, opts, dir); err != nil {
			t.Fatalf("Preview(%s): %v", dir, err)
		}

		if slices.Contains(src.modes, ModeApply) {
			t.Errorf("previewing %s asked in %v, and a preview runs nothing", dir, src.modes)
			t.Log("A migration reached through a language runtime can write directly, through")
			t.Log("host.db.exec. Asking it as a run is how migr plan performs the side effects it")
			t.Log("was invoked to avoid -- against a production database, silently.")
		}
	}
}

func TestARollbackAsksTheDownAsARunAndTheDerivedUpAsAQuestion(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	want := []Mode{ModeApply, ModePlan}
	if !slices.Equal(src.modes, want) {
		t.Errorf("the source was asked in %v, want %v", src.modes, want)
		t.Log("One run issues both modes. The down is a run; the up that follows it is the")
		t.Log("question the inverse is derived from, and nothing it answers is executed -- so")
		t.Log("letting the escape hatch fire while asking would perform a write the operator")
		t.Log("never requested (docs/protocol.md §4, CLAUDE.md §4.3).")
	}

	if !slices.Equal(src.directions, []ir.Direction{ir.Down, ir.Up}) {
		t.Fatalf("the source was asked for %v, and the modes above answer for those", src.directions)
	}
}

func TestAnAuthoredDownIsAskedAsTheRunItIs(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, src := staged(t, "ab")
	src.down[versionAt(0)] = ir.Operations{addIndex("z9")}

	if _, err := Rollback(t.Context(), conn, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if !slices.Equal(src.modes, []Mode{ModeApply}) {
		t.Errorf("the source was asked in %v, want the one run this rollback is", src.modes)
	}
}

func TestARunIsRefusedInAModeThisBuildDoesNotKnow(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "ab")

	for _, mode := range []Mode{"", "dry-run", "APPLY"} {
		if _, err := newExecutor(conn, opts, mode); !errors.Is(err, ErrUnknownMode) {
			t.Errorf("newExecutor in mode %q returned %v, want %v", mode, err, ErrUnknownMode)
		}
	}

	for _, mode := range Modes() {
		if _, err := newExecutor(conn, opts, mode); err != nil {
			t.Errorf("newExecutor in mode %q returned %v", mode, err)
		}
	}
}
