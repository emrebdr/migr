package sql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
)

// declaredReason is the reason every fixture here gives for its migration having no undo.
const declaredReason = "the rows are gone; restore from a backup"

const declaredIrreversible = `-- migr:up
DROP TABLE legacy_sessions;

-- migr:irreversible: ` + declaredReason + `
`

func TestADeclaredIrreversibleMigrationStillSaysWhatItDoes(t *testing.T) {
	got := rawSQL(t, emit(t, sourceOf(t, declaredIrreversible), ir.Up))

	if want := []string{"DROP TABLE legacy_sessions;"}; !equal(got, want) {
		t.Errorf("the up section holds %q, want %q", got, want)
		t.Log("The declaration closes the section above it, exactly as a down marker would; a")
		t.Log("migration that declares it cannot be undone still has something to do.")
	}
}

func TestADeclaredIrreversibleMigrationRefusesItsDownWithTheAuthorsReason(t *testing.T) {
	_, err := sourceOf(t, declaredIrreversible).Emit(t.Context(), file, ir.Down)

	if !errors.Is(err, engine.ErrDeclaredIrreversible) {
		t.Fatalf("Emit(down) returned %v, want the declaration", err)
	}

	if errors.Is(err, engine.ErrNoDownAuthored) {
		t.Errorf("a declared migration reported that it merely authored no down")
		t.Log("The difference is the whole point: one has an inverse derived for it and the other")
		t.Log("must not, since its author has already said there is nothing to derive.")
	}

	if !strings.Contains(err.Error(), declaredReason) {
		t.Errorf("the refusal is %q, want the reason its author gave", err)
	}
}

func TestADeclarationIsRefusedWithoutAReason(t *testing.T) {
	for _, body := range []string{
		"-- migr:up\nDROP TABLE t;\n\n-- migr:irreversible:\n",
		"-- migr:up\nDROP TABLE t;\n\n-- migr:irreversible:   \n",
	} {
		_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up)

		if !errors.Is(err, ErrMalformed) {
			t.Errorf("a declaration with no reason was accepted: %v", err)
			t.Log("Invariant #10 lets a migration declare itself irreversible *with a reason*, and")
			t.Log("the reason is the whole content of that claim.")
		}
	}
}

func TestADeclarationIsRefusedBesideADownSection(t *testing.T) {
	body := "-- migr:up\nDROP TABLE t;\n\n-- migr:down\nCREATE TABLE t (id bigint);\n\n" +
		"-- migr:irreversible: " + declaredReason + "\n"

	_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up)

	if !errors.Is(err, ErrMalformed) {
		t.Errorf("a file both wrote an undo and declared it has none: %v", err)
	}

	if err != nil && !strings.Contains(err.Error(), MarkerDown) {
		t.Errorf("the refusal does not name both markers: %v", err)
	}
}

func TestADeclarationIsRefusedAboveTheUpMarker(t *testing.T) {
	body := "-- migr:irreversible: " + declaredReason + "\n-- migr:up\nDROP TABLE t;\n"

	_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up)

	if !errors.Is(err, ErrMalformed) {
		t.Fatalf("a declaration above the up marker was accepted: %v", err)
	}

	if !strings.Contains(err.Error(), MarkerUp) {
		t.Errorf("the refusal is %q, want it to name the marker the declaration got above", err)
		t.Log("Everything above the first marker is a comment this parser passes over, so a")
		t.Log("declaration placed there would be ignored in silence. Refusing it as SQL below")
		t.Log("the declaration would be the right code for the wrong reason: it reads as a")
		t.Log("complaint about the statements rather than about where the declaration sits.")
	}
}

func TestADeclarationIsRefusedCarriedTwice(t *testing.T) {
	body := "-- migr:up\nDROP TABLE t;\n\n-- migr:irreversible: one\n-- migr:irreversible: two\n"

	_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up)

	if !errors.Is(err, ErrMalformed) {
		t.Errorf("a doubled declaration was accepted: %v", err)
	}
}

func TestSQLBelowADeclarationIsRefusedRatherThanDropped(t *testing.T) {
	body := "-- migr:up\nDROP TABLE t;\n\n-- migr:irreversible: " + declaredReason +
		"\nCREATE TABLE t (id bigint);\n"

	_, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up)

	if !errors.Is(err, ErrMalformed) {
		t.Errorf("SQL below a declaration was accepted: %v", err)
		t.Log("It belongs to no direction and would never run, which is the same reason SQL above")
		t.Log("the up marker is refused rather than dropped.")
	}
}

func TestACommentBelowADeclarationIsFine(t *testing.T) {
	body := "-- migr:up\nDROP TABLE t;\n\n-- migr:irreversible: " + declaredReason +
		"\n-- the backup lives in the nightly bucket\n"

	if _, err := sourceOf(t, body).Emit(t.Context(), file, ir.Up); err != nil {
		t.Errorf("a comment below a declaration was refused: %v", err)
	}
}

// equal reports whether two statement lists hold the same statements in the same order.
func equal(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}

	return true
}
