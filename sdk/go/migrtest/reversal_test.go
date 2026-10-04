package migrtest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

// reported stands in for a test, so a case can assert what a helper reports rather than be failed by it.
type reported struct {
	// ctx is what a half runs under, which outside a real test is simply not canceled.
	ctx context.Context
	// findings are everything the helper reported, in order.
	findings []string
	// fatal reports whether the helper gave up, which a stand-in cannot act on as testing does.
	fatal bool
}

// newReported builds a stand-in reporter for one case.
func newReported(t *testing.T) *reported {
	t.Helper()

	return &reported{ctx: t.Context()}
}

// Helper does what a helper marker does outside a test, which is nothing.
func (r *reported) Helper() {}

// Context reports what a captured half runs under.
func (r *reported) Context() context.Context { return r.ctx }

// Errorf records a finding.
func (r *reported) Errorf(format string, args ...any) {
	r.findings = append(r.findings, fmt.Sprintf(format, args...))
}

// Fatalf records a finding and that the helper had nothing left to do.
func (r *reported) Fatalf(format string, args ...any) {
	r.fatal = true
	r.Errorf(format, args...)
}

// Log records an argument beside the findings it belongs to.
func (r *reported) Log(args ...any) {
	r.findings = append(r.findings, fmt.Sprint(args...))
}

// said reports everything this stand-in was told, as one string to look for a phrase in.
func (r *reported) said() string {
	return strings.Join(r.findings, "\n")
}

// quiet reports whether the helper found nothing at all to say.
func (r *reported) quiet() bool {
	return len(r.findings) == 0
}

func TestReversalReportsWhichOfTheThreeAnswersAMigrationGives(t *testing.T) {
	for _, c := range []struct {
		name   string
		m      migr.Migration
		undo   migr.Undo
		reason string
		halves int
	}{
		{"it authors its own down", creates{}, migr.UndoAuthored, "", 2},
		{"it declares that it cannot be undone", oneWay{}, migr.UndoDeclared,
			"the dropped values are not recoverable; restore from a backup", 1},
		{"the engine derives its inverse", derives{}, migr.UndoDerived, "", 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			reversed := Reversal(t, c.m)

			if reversed.Undo != c.undo {
				t.Errorf("this migration is undone by %q, want %q", reversed.Undo, c.undo)
			}

			if reversed.Reason != c.reason {
				t.Errorf("the reason is %q, want %q", reversed.Reason, c.reason)
			}

			if len(reversed.Up.Operations) == 0 {
				t.Error("the up half was not captured, and it is the half an inverse comes from")
			}

			halves := 1
			if len(reversed.Down.Operations) > 0 {
				halves = 2
			}

			if halves != c.halves {
				t.Errorf("%d halves were captured, want %d", halves, c.halves)
			}

			if reversed.Problem != "" {
				t.Errorf("this migration can be undone, and was reported as %q", reversed.Problem)
			}
		})
	}
}

func TestADownIsCapturedExactlyWhenTheMigrationAuthorsOne(t *testing.T) {
	authored := Reversal(t, creates{})

	authored.Down.Is(t, migr.DropTable{
		Table: theTable,
		From: &migr.TableDef{
			Columns: []migr.Column{
				{Name: "id", Type: migr.Int64()},
				{Name: "email", Type: migr.String(255)},
			},
			PrimaryKey: []string{"id"},
		},
	})

	derived := Reversal(t, derives{})

	if len(derived.Down.Operations) != 0 {
		t.Errorf("a derived inverse was reported as a captured down: %v", derived.Down.Operations)
		t.Log("ADR 0022 keeps every inverse in the engine, so there is nothing to capture here.")
	}
}

func TestADerivedUndoOverAHeldStatementIsReported(t *testing.T) {
	stood := newReported(t)

	reversed := Stage{}.reversal(stood, derivesOverStatement{})

	if stood.quiet() {
		t.Fatal("a migration whose up execs and authors no down was reported as fine")
	}

	for _, phrase := range []string{"authors no down", theStatement, "no inverse to derive",
		"ADR 0036"} {
		if !strings.Contains(stood.said(), phrase) {
			t.Errorf("the finding does not say %q:\n%s", phrase, stood.said())
		}
	}

	if reversed.Problem == "" {
		t.Error("the finding was printed and not carried on the value a test can read")
	}
}

func TestTheSamePatternWithAnAuthoredDownIsNotReported(t *testing.T) {
	stood := newReported(t)

	reversed := Stage{}.reversal(stood, mixes{})

	if !stood.quiet() {
		t.Errorf("a migration that execs and authors its own down was reported:\n%s", stood.said())
	}

	if len(reversed.Up.Statements) != 1 {
		t.Fatalf("the up sent %d statements, want the one this fixture sends",
			len(reversed.Up.Statements))
	}

	if len(reversed.Down.Operations) != 1 {
		t.Errorf("the authored down emitted %d operations, want 1", len(reversed.Down.Operations))
	}

	t.Log("The refusal is about derivation, not about the pattern: an authored down is the")
	t.Log("author saying what undoes their own data change, which is what ADR 0036 asks for.")
}

func TestAMigrationThatSendsNoStatementsIsNeverReported(t *testing.T) {
	for _, m := range []migr.Migration{creates{}, derives{}, oneWay{}} {
		stood := newReported(t)
		empty := Stage{}

		empty.reversal(stood, m)

		if !stood.quiet() {
			t.Errorf("%T was reported:\n%s", m, stood.said())
		}
	}
}

func TestACaptureThatEmittedSomethingElseIsReportedAgainstTheBytes(t *testing.T) {
	for _, c := range []struct {
		name  string
		want  []migr.Operation
		says  string
		fatal bool
	}{
		{"a different field", []migr.Operation{migr.AddColumn{Table: theTable,
			Column: migr.Column{Name: "tier", Type: migr.Int64(), Nullable: true}}},
			"operation 1", false},
		{"a different count", []migr.Operation{theIndex(), theIndex()},
			"emitted 1 operations, want 2", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			stood := newReported(t)

			Up(t, derives{}).is(stood, c.want...)

			if !strings.Contains(stood.said(), c.says) {
				t.Errorf("the finding does not say %q:\n%s", c.says, stood.said())
			}

			if stood.fatal != c.fatal {
				t.Errorf("the helper gave up: %t, want %t", stood.fatal, c.fatal)
			}
		})
	}
}
