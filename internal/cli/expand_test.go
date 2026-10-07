package cli

import (
	"strings"
	"testing"
)

func TestARunCompilesAnEmulatedOperationAgainstTheSession(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if !f.asked("expand") {
		t.Fatal("the session was never asked to compile the operation")
	}

	if len(f.statements) == 0 || f.statements[0] != "-- read the schema" {
		t.Errorf("the run executed %v, not what the session produced", f.statements)
	}

	t.Log("internal/cli is what hands the session to the planner, and it is the only place that")
	t.Log("can: the planner holds a compiler, which needs no database, and the session is the")
	t.Log("thing that has one. A driver that emulates nothing is handed nothing (ADR 0053).")
}

func TestAPreviewShowsTheOneEmulationItCanCompileExactlyAndMarksTheRest(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	stdout, _, err := runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, fragment := range []string{
		"emulated: table rebuild",
		"-- read the schema",
		"\u2192 compiled against the database when it runs",
		"only the first operation of the first migration can be compiled exactly",
	} {
		if !strings.Contains(stdout, fragment) {
			t.Errorf("plan does not show %q:\n%s", fragment, stdout)
		}
	}

	if got := strings.Count(stdout, "-- read the schema"); got != 1 {
		t.Errorf("plan expanded %d operations, want only the one it can compile exactly", got)
	}

	t.Log("CLAUDE.md §4.2: a capability is never silently degraded. The label says a rebuild is")
	t.Log("about to happen, and the first operation's statements say what one is -- so an operator")
	t.Log("can still read a rebuild before anything runs. What a preview cannot do is render the")
	t.Log("ones after it, because a preview applies nothing and each is rendered from the catalog")
	t.Log("the operations ahead of it leave (ADR 0071).")
}

func TestAPreviewReportsAnEmulationItDeferredRatherThanCountingItAsNoStatements(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	stdout, _, err := runWith(t, dir, noon, drivers, "--json", "plan")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	ops := decodePlan(t, stdout).Migrations[0].Operations

	if ops[0].Deferred || len(ops[0].Statements) != 2 {
		t.Errorf("the first operation reads %+v, want the expansion the session produced", ops[0])
	}

	if !ops[1].Deferred || len(ops[1].Statements) != 0 {
		t.Errorf("the second operation reads %+v, want it marked and empty", ops[1])
	}

	stdout, _, err = runWith(t, dir, noon, drivers, "up", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}

	if !strings.Contains(stdout, "1 operation compiled as it runs") {
		t.Errorf("the dry run counts statements as if the plan were complete:\n%s", stdout)
	}

	stdout, _, err = runWith(t, dir, noon, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !strings.Contains(stdout, "2 statements from the operations it could compile") {
		t.Errorf("the plan presents a partial count as the whole:\n%s", stdout)
	}

	t.Log("A count that silently omitted the deferred operations would read as a smaller migration")
	t.Log("than the one about to run, which is the one number a dry run exists to give.")
}

func TestARunCompilesEveryEmulatedOperationAndAPreviewDoesNot(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)
	f.expands = true
	f.emulates = "table rebuild"

	if _, _, err := runWith(t, dir, noon, drivers, "plan"); err != nil {
		t.Fatalf("plan: %v", err)
	}

	previews := f.count("expand")

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if runs := f.count("expand") - previews; runs != 2 {
		t.Errorf("the run expanded %d operations, want both of them", runs)
	}

	if previews != 1 {
		t.Errorf("the preview expanded %d operations, want only the exact one", previews)
	}
}

func TestADriverThatEmulatesNothingIsHandedNoSession(t *testing.T) {
	dir, f, drivers := project(t, twoTables()[:1]...)

	if _, _, err := runWith(t, dir, noon, drivers, "up"); err != nil {
		t.Fatalf("up: %v", err)
	}

	if f.asked("expand") {
		t.Error("a driver that compiles everything was asked to expand something")
	}
}
