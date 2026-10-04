package rule

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestADerivedDownIsTheOneAnswerReported(t *testing.T) {
	found := judgingAbout(t, MissingDown, Subject{
		Up:   upOf(ir.CreateTable{Table: "users", Columns: []ir.Column{column(t, "id", "int64")}}),
		Undo: UndoDerived,
	})

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1: %v", len(found), found)
	}

	if found[0].Direction != ir.Down {
		t.Errorf("the finding names the %s half, and what is missing is the down", found[0].Direction)
	}

	if found[0].Operation != 0 || found[0].Kind != "" {
		t.Errorf("the finding is %+v, want it about the whole half rather than one operation",
			found[0])
	}

	for _, phrase := range []string{"authors no down", "when the rollback runs", "write a down",
		"irreversible"} {
		if !strings.Contains(found[0].Detail, phrase) {
			t.Errorf("detail %q does not say %q", found[0].Detail, phrase)
		}
	}
}

func TestEveryOtherAnswerToWhatUndoesAMigrationIsSomebodyElsesToReport(t *testing.T) {
	for _, c := range []struct {
		undo Undo
		why  string
	}{
		{UndoAuthored, "its author wrote one, which is what this rule asks for"},
		{UndoDeclared, "its author answered the question with a reason"},
		{UndoNone, "the reverse check reports it, and two sentences about one defect is how a " +
			"report stops being read"},
		{UndoUnread, "the format check reports it, the down not being readable at all"},
	} {
		t.Run(string(c.undo), func(t *testing.T) {
			up := upOf(ir.CreateTable{Table: "users",
				Columns: []ir.Column{column(t, "id", "int64")}})

			if found := judgingAbout(t, MissingDown, Subject{Up: up, Undo: c.undo}); len(found) != 0 {
				t.Errorf("it was reported anyway, %d times: %v", len(found), found)
				t.Logf("%s", c.why)
			}
		})
	}
}

func TestMissingDownReadsNothingButWhereTheDownCameFrom(t *testing.T) {
	bare := judgingAbout(t, MissingDown, Subject{Undo: UndoDerived})

	if len(bare) != 1 {
		t.Errorf("a migration emitting nothing at all reported %d findings, want 1 -- this rule is "+
			"about the down and not about what the up did", len(bare))
	}
}
