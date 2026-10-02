package history

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

// stoppedAt returns a dirty forward run of a file, naming the statement it reached.
func stoppedAt(f File, batch int64, last int32) Record {
	rec := up(f, batch, Dirty)
	rec.LastStatement = last
	rec.Error = "statement 3: the server refused this statement"

	return rec
}

// settled returns a run an operator asserted settled the migration, in the direction they named.
func settled(f File, batch int64, direction ir.Direction) Record {
	rec := runAt(f, batch, 1, direction, Resolved)
	rec.Error = "finished it by hand during the incident"

	return rec
}

func TestAResolvedRunSettlesWhatADirtyOneLeftBetweenStates(t *testing.T) {
	for _, c := range []struct {
		direction ir.Direction
		want      State
	}{
		{direction: ir.Up, want: StateApplied},
		{direction: ir.Down, want: StatePending},
	} {
		t.Run(string(c.direction), func(t *testing.T) {
			f := file(0, "add_users", "create table users")
			status := resolve(t, f, stoppedAt(f, 1, 2), settled(f, 2, c.direction))

			if status.State != c.want {
				t.Errorf("the migration reconciled to %q, want %q", status.State, c.want)
				t.Log("Clearing the dirty state is the whole reason this outcome exists: the fold")
				t.Log("is the only thing that decides whether the next up may proceed.")
			}

			if status.Recorded.Outcome != Resolved {
				t.Errorf("the anchor is %q, want the run that settled it", status.Recorded.Outcome)
				t.Log("A report reads the anchor to say a state was asserted rather than run.")
			}
		})
	}
}

func TestADirtyRunAfterAResolvedOneIsDirtyAgain(t *testing.T) {
	f := file(0, "add_users", "create table users")
	status := resolve(t, f, stoppedAt(f, 1, 2), settled(f, 2, ir.Up), stoppedAt(f, 3, 1))

	if status.State != StateDirty {
		t.Errorf("the migration reconciled to %q, want dirty", status.State)
		t.Log("The fold replays runs in order, so a resolution settles only what came before it.")
	}
}

func TestAResolvedRunDoesNotBlockTheNextMigration(t *testing.T) {
	if Resolved.Blocking() {
		t.Error("a resolved run blocks the next migration")
		t.Log("Dirty is the only blocking outcome. A resolution that still blocked would leave the")
		t.Log("database in exactly the state the command exists to clear.")
	}
}

func TestAResolvedRunMustSayWhyItWasResolved(t *testing.T) {
	f := file(0, "add_users", "create table users")

	rec := settled(f, 2, ir.Up)
	rec.Error = ""

	err := rec.Validate()
	if err == nil {
		t.Fatal("a resolved run carrying no reason validated")
	}

	if !strings.Contains(err.Error(), "operator's reason") {
		t.Errorf("the refusal does not say what is missing: %v", err)
		t.Log("The row is the only thing that will still say why this migration reads as settled")
		t.Log("when nobody remembers the incident.")
	}
}

func TestAResolvedRunNamesNoStatementBecauseItRanNone(t *testing.T) {
	f := file(0, "add_users", "create table users")

	rec := settled(f, 2, ir.Up)
	rec.LastStatement = 2

	if err := rec.Validate(); err == nil {
		t.Error("a resolved run naming a statement validated")
		t.Log("last_statement is the last statement that succeeded in that run, and an assertion")
		t.Log("ran none -- which is also why a resume records applied rather than resolved.")
	}
}
