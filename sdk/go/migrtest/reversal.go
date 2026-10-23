package migrtest

import (
	"fmt"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

// What an SDK is the authority on about a rollback, and what it is not.
//
// It knows which of docs/protocol.md §7.4's three answers a migration gives, because that is a fact
// about the migration's own shape. It knows the escape hatch's refusal, because the capture can see the
// statements the up sent itself. It does not know whether an operation can be inverted at all: all
// twenty inverses live in internal/ir, precisely so that two SDKs could
// not reach two answers for one migration. A copy here would be a third, and the day it drifted it
// would pass a down the engine refuses.
//
// So that half is the engine's, reached through Project: migr validate runs the reverse check, and
// Running.RoundTrip measures whether a rollback leaves anything behind.

// Reversed is how a migration is undone, and what each half it has emitted.
type Reversed struct {
	// Undo is which of the three answers docs/protocol.md §7.4 names this migration gives.
	Undo migr.Undo
	// Reason is the author's reason a rollback halts with, and empty unless Undo is declared.
	Reason string
	// Up is what the up half emitted, which is what a derived inverse would be derived from.
	Up Capture
	// Down is what an authored down emitted, and empty unless Undo is authored.
	Down Capture
	// Problem is why this migration cannot be undone as it stands, and empty where it can.
	Problem string
}

// Reversal reports how a migration is undone, capturing every half it has.
func Reversal(t *testing.T, m migr.Migration) Reversed {
	t.Helper()

	return Stage{}.reversal(t, m)
}

// Reversal reports how a migration is undone against this stage, capturing every half it has.
func (s Stage) Reversal(t *testing.T, m migr.Migration) Reversed {
	t.Helper()

	return s.reversal(t, m)
}

// reversal reads the migration's own shape, captures what it has, and reports what it cannot do.
func (s Stage) reversal(r reporter, m migr.Migration) Reversed {
	r.Helper()

	undo, reason := migr.Undone(m)
	reversed := Reversed{Undo: undo, Reason: reason, Up: s.up(r, m)}

	if undo == migr.UndoAuthored {
		reversed.Down = s.down(r, m)
	}

	reversed.Problem = underivable(m, undo, reversed.Up.Statements)

	if reversed.Problem != "" {
		r.Errorf("%s", reversed.Problem)
		reportHeldStatements(r)
	}

	return reversed
}

// underivable reports the one rollback an SDK can see is impossible, and says nothing otherwise.
func underivable(m migr.Migration, undo migr.Undo, held []Statement) string {
	if undo != migr.UndoDerived || len(held) == 0 {
		return ""
	}

	return fmt.Sprintf("%T authors no down, so the engine derives its inverse -- but its up sends "+
		"%d statement(s) of its own, the first being %q, and a statement has no inverse to derive",
		m, len(held), held[0].SQL)
}

// reportHeldStatements says why that refusal is the engine's rather than this helper's opinion.
func reportHeldStatements(r reporter) {
	r.Helper()

	r.Log("An exec is committed on its own, before the transaction the operations")
	r.Log("run in, and a later failure does not undo it. The engine refuses to derive a down")
	r.Log("over one, so write a Down, or declare the migration irreversible with a reason.")
}
