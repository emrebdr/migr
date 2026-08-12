package sql

import (
	"slices"
	"strings"
	"testing"
)

// lines splits the template the way a parser reading it line by line will.
func lines() []string {
	return strings.Split(Template, "\n")
}

// countLines reports how many lines of the template are exactly the given text.
func countLines(want string) int {
	found := 0

	for _, line := range lines() {
		if line == want {
			found++
		}
	}

	return found
}

func TestTheTemplateCarriesNoSQLOfItsOwn(t *testing.T) {
	for i, line := range lines() {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "--") {
			continue
		}

		t.Errorf("line %d is neither blank nor a comment: %q", i+1, line)
		t.Log(`Invariant #3 is "no raw SQL outside a driver package. Ever." The one file migr`)
		t.Log("itself authors that is named .sql is markers and guidance only -- an example")
		t.Log("statement, however illustrative, is the exception that turns the invariant into")
		t.Log("a preference, and it would ship in every migration anyone ever creates.")
	}
}

func TestEachMarkerAppearsOnceOnALineOfItsOwn(t *testing.T) {
	for _, marker := range []string{MarkerUp, MarkerDown} {
		if got := countLines(marker); got != 1 {
			t.Errorf("%q appears as %d whole lines of the template, want exactly 1", marker, got)
			t.Log("A parser finds a section by matching a whole line. A marker that appears twice")
			t.Log("gives it two answers, and one that appears only inside a longer line gives none.")
		}
	}
}

func TestTheUpSectionComesFirst(t *testing.T) {
	up := slices.Index(lines(), MarkerUp)
	down := slices.Index(lines(), MarkerDown)

	if up < 0 || down < 0 || up >= down {
		t.Errorf("the up marker is at line %d and the down marker at line %d, want up first",
			up+1, down+1)
		t.Log("A file is read top to bottom and applied before it is ever rolled back. Ordering")
		t.Log("the sections the other way makes every migration read backwards from how it runs.")
	}
}

func TestTheMarkersCarryTheHouseMarkerPrefix(t *testing.T) {
	for _, marker := range []string{MarkerUp, MarkerDown} {
		if !strings.HasPrefix(marker, "-- migr:") {
			t.Errorf("%q does not begin with the -- migr: prefix", marker)
			t.Log("The golden files already spell theirs -- migr:statement 1 and -- migr:unsupported.")
			t.Log("One prefix is what lets a reader tell a marker migr owns from a comment they wrote.")
		}
	}
}

func TestTheTemplateTellsAnAuthorWhyTheDownIsTheirs(t *testing.T) {
	down := Template[strings.Index(Template, MarkerDown):]

	for _, want := range []string{"raw", "inverse"} {
		if !strings.Contains(down, want) {
			t.Errorf("the down section does not mention %q:\n%s", want, down)
			t.Log("SQL reaches the database as ir.Raw, whose Reverse always fails, so a .sql")
			t.Log("migration with an empty down can never have one derived (ADR 0022, C4). The")
			t.Log("file itself is where an author is standing when they decide to skip it.")
		}
	}
}

func TestTheTemplateEndsWithOneNewline(t *testing.T) {
	if !strings.HasSuffix(Template, "\n") || strings.HasSuffix(Template, "\n\n") {
		t.Errorf("the template ends %q, want exactly one trailing newline",
			Template[len(Template)-2:])
	}
}

func TestTheExtensionIsADotAndALowercaseWord(t *testing.T) {
	if !strings.HasPrefix(Ext, ".") || strings.ToLower(Ext) != Ext || len(Ext) < 2 {
		t.Errorf("Ext is %q, want a dot followed by a lowercase word", Ext)
		t.Log("internal/migration takes everything after the last dot as the extension and")
		t.Log("records it without judging it, so a bare dot or an empty one is refused there")
		t.Log("rather than here -- which would make migr create fail on its own template.")
	}
}
