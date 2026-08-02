package migration

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// at builds a migration at the given minute of one afternoon, so a fixture's order is readable.
func at(minute int, name string) Migration {
	return Migration{
		Version: fmt.Sprintf("20260727T15%02d00", minute),
		Name:    name,
		Ext:     ".sql",
	}
}

// order runs the resolver over a set that is expected to resolve, returning the filenames it gives.
func order(t *testing.T, found []Migration, declared Declared) []string {
	t.Helper()

	ordered, err := Order(found, declared)
	if err != nil {
		t.Fatalf("Order: %v", err)
	}

	return filenames(ordered)
}

// refuse runs the resolver over a set that cannot resolve, returning the report it produced.
func refuse(t *testing.T, found []Migration, declared Declared) *GraphError {
	t.Helper()

	ordered, err := Order(found, declared)
	if err == nil {
		t.Fatalf("Order returned %v, want a refusal", filenames(ordered))
	}

	if !errors.Is(err, ErrUnresolved) {
		t.Fatalf("Order failed with %v, want ErrUnresolved", err)
	}

	var graph *GraphError
	if !errors.As(err, &graph) {
		t.Fatalf("Order failed with %T, want a *GraphError a caller can read", err)
	}

	return graph
}

func TestWithNothingDeclaredTheTimestampIsTheOrder(t *testing.T) {
	found := []Migration{at(3, "third"), at(1, "first"), at(2, "second")}
	want := []string{
		at(1, "first").Filename(),
		at(2, "second").Filename(),
		at(3, "third").Filename(),
	}

	for _, declared := range []Declared{nil, {}} {
		if got := order(t, found, declared); !slices.Equal(got, want) {
			t.Errorf("Order(found, %v) = %v, want %v", declared, got, want)
		}
	}

	t.Log("This is the case every command takes until an SDK exists to declare anything, and")
	t.Log("it is also the one that proves Order sorts rather than trusting what it was handed.")
}

func TestADependencyReordersMigrationsTheTimestampsPutBackwards(t *testing.T) {
	earlier, later := at(1, "from_one_branch"), at(2, "from_another")

	found := []Migration{earlier, later}
	want := []string{later.Filename(), earlier.Filename()}

	got := order(t, found, Declared{earlier.Version: {later.Version}})
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}

	t.Log("A dependency pointing backwards in time is not a mistake, it is the whole use case:")
	t.Log("two branches each timestamp a migration, they merge, and the earlier one has to run")
	t.Log("second. CLAUDE.md §9 calls this parallel branches and merged feature work.")
}

func TestADependencyTheTimestampsAlreadySatisfyChangesNothing(t *testing.T) {
	first, second := at(1, "first"), at(2, "second")

	found := []Migration{first, second}
	want := []string{first.Filename(), second.Filename()}

	got := order(t, found, Declared{second.Version: {first.Version}})
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestTheTimestampIsTheTiebreakAmongMigrationsNothingConstrains(t *testing.T) {
	base := at(1, "base")
	left, right := at(3, "left_branch"), at(2, "right_branch")
	merge := at(4, "merge")

	found := []Migration{merge, left, right, base}
	declared := Declared{
		left.Version:  {base.Version},
		right.Version: {base.Version},
		merge.Version: {left.Version, right.Version},
	}

	want := []string{
		base.Filename(), right.Filename(), left.Filename(), merge.Filename(),
	}

	if got := order(t, found, declared); !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
		t.Log("Nothing orders the two branches against each other, so their timestamps do, and")
		t.Log("the later-timestamped one comes second. Without that rule the sequence M4-T5")
		t.Log("runs and M4-T8 steps through would depend on which branch was read first.")
	}
}

func TestAMigrationFreedLateStillTakesItsPlaceByTimestamp(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")
	c, d, e := at(3, "c"), at(4, "d"), at(5, "e")

	found := []Migration{a, b, c, d, e}
	want := []string{a.Filename(), b.Filename(), c.Filename(), d.Filename(), e.Filename()}

	got := order(t, found, Declared{b.Version: {a.Version}})
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
		t.Log("c, d, and e are ready from the start; b only becomes ready once a has run. A")
		t.Log("queue that took whatever became ready most recently would run c, d, and e first")
		t.Log("and leave b last, even though its own timestamp puts it second and its one")
		t.Log("dependency was already satisfied. The ready set is drained in version order.")
	}
}

func TestALongChainRunsInTheOrderItDeclares(t *testing.T) {
	a, b, c, d := at(1, "a"), at(2, "b"), at(3, "c"), at(4, "d")

	found := []Migration{a, b, c, d}
	declared := Declared{
		a.Version: {b.Version},
		b.Version: {c.Version},
		c.Version: {d.Version},
	}

	want := []string{d.Filename(), c.Filename(), b.Filename(), a.Filename()}

	if got := order(t, found, declared); !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestADependencyDeclaredTwiceIsNotAProblem(t *testing.T) {
	first, second := at(1, "first"), at(2, "second")

	found := []Migration{first, second}
	want := []string{first.Filename(), second.Filename()}

	got := order(t, found, Declared{second.Version: {first.Version, first.Version}})
	if !slices.Equal(got, want) {
		t.Errorf("Order = %v, want %v", got, want)
	}
}

func TestAMigrationDependingOnItselfIsACycleOfOne(t *testing.T) {
	only := at(1, "only")

	graph := refuse(t, []Migration{only}, Declared{only.Version: {only.Version}})

	want := [][]string{{only.Filename()}}
	if !equalCycles(graph.Cycles, want) {
		t.Fatalf("Cycles = %v, want %v", graph.Cycles, want)
	}

	if path := only.Filename() + " → " + only.Filename(); !strings.Contains(graph.Error(), path) {
		t.Errorf("the report does not render the loop as %q:\n%v", path, graph)
	}
}

func TestACycleIsReportedWithTheWholePathAroundIt(t *testing.T) {
	a, b, c := at(1, "a"), at(2, "b"), at(3, "c")

	graph := refuse(t, []Migration{a, b, c}, Declared{
		a.Version: {b.Version},
		b.Version: {c.Version},
		c.Version: {a.Version},
	})

	want := [][]string{{a.Filename(), b.Filename(), c.Filename()}}
	if !equalCycles(graph.Cycles, want) {
		t.Fatalf("Cycles = %v, want %v", graph.Cycles, want)
	}

	path := strings.Join([]string{a.Filename(), b.Filename(), c.Filename(), a.Filename()}, " → ")
	if !strings.Contains(graph.Error(), path) {
		t.Errorf("the report does not render the full path %q:\n%v", path, graph)
		t.Log("Naming the migrations in a cycle without saying which depends on which leaves an")
		t.Log("operator to reconstruct the loop by hand. The done-when asks for the path.")
	}
}

func TestTwoSeparateCyclesAreBothReported(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")
	y, z := at(3, "y"), at(4, "z")

	graph := refuse(t, []Migration{a, b, y, z}, Declared{
		a.Version: {b.Version},
		b.Version: {a.Version},
		y.Version: {z.Version},
		z.Version: {y.Version},
	})

	want := [][]string{
		{a.Filename(), b.Filename()},
		{y.Filename(), z.Filename()},
	}

	if !equalCycles(graph.Cycles, want) {
		t.Errorf("Cycles = %v, want both loops", graph.Cycles)
		t.Log("Two tangles that share no migration are two independent problems, and reporting")
		t.Log("one at a time is two commands to fix what one pass can see.")
	}
}

func TestOneLoopIsReportedOnceRatherThanOncePerMigrationInIt(t *testing.T) {
	a, b, c := at(1, "a"), at(2, "b"), at(3, "c")

	graph := refuse(t, []Migration{a, b, c}, Declared{
		a.Version: {b.Version},
		b.Version: {c.Version},
		c.Version: {a.Version},
	})

	if len(graph.Cycles) != 1 {
		t.Errorf("one loop was reported %d times: %v", len(graph.Cycles), graph.Cycles)
	}
}

func TestAMigrationBehindACycleIsNotItselfReportedAsOne(t *testing.T) {
	waiting := at(1, "waiting")
	a, b := at(2, "a"), at(3, "b")

	graph := refuse(t, []Migration{waiting, a, b}, Declared{
		waiting.Version: {a.Version},
		a.Version:       {b.Version},
		b.Version:       {a.Version},
	})

	want := [][]string{{a.Filename(), b.Filename()}}
	if !equalCycles(graph.Cycles, want) {
		t.Errorf("Cycles = %v, want only the loop itself", graph.Cycles)
		t.Log("A migration waiting on a cycle never orders either, but it is not in a loop and")
		t.Log("nothing about it can be edited to fix one.")
		t.Log("It is timestamped first deliberately, so the walk reaches the loop from outside")
		t.Log("it -- the case where reporting the whole path walked, rather than the part that")
		t.Log("closed back on itself, puts a migration in a cycle it is merely behind.")
	}
}

func TestOneLoopWithTwoMigrationsWaitingOnItIsStillReportedOnce(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")
	x, y := at(3, "x"), at(4, "y")

	graph := refuse(t, []Migration{a, b, x, y}, Declared{
		a.Version: {b.Version},
		b.Version: {a.Version},
		x.Version: {a.Version},
		y.Version: {a.Version},
	})

	want := [][]string{{a.Filename(), b.Filename()}}
	if !equalCycles(graph.Cycles, want) {
		t.Errorf("Cycles = %v, want the one loop", graph.Cycles)
		t.Log("x and y each reach the loop, and neither is in it. A walk that re-entered a")
		t.Log("migration it had already explored would report the same loop once per way in;")
		t.Log("one that left a migration marked as still on the path would find a loop that is")
		t.Log("not there at all.")
	}
}

func TestALoopIsReportedFromItsEarliestMigrationHoweverItWasReached(t *testing.T) {
	head := at(1, "head")
	a, b := at(2, "a"), at(3, "b")

	graph := refuse(t, []Migration{head, a, b}, Declared{
		head.Version: {b.Version},
		b.Version:    {a.Version},
		a.Version:    {b.Version},
	})

	want := [][]string{{a.Filename(), b.Filename()}}
	if !equalCycles(graph.Cycles, want) {
		t.Errorf("Cycles = %v, want the loop turned to start at %s", graph.Cycles, a.Filename())
		t.Log("The walk enters this loop at b, so the path it closes reads b then a. Rotating")
		t.Log("to the earliest migration is what keeps the message the same when something")
		t.Log("elsewhere in the graph changes which end the walk happened to arrive from.")
	}
}

func TestTwoLoopsSharingAMigrationAreBothReported(t *testing.T) {
	a, b, c := at(1, "a"), at(2, "b"), at(3, "c")

	graph := refuse(t, []Migration{a, b, c}, Declared{
		a.Version: {b.Version, c.Version},
		b.Version: {a.Version},
		c.Version: {a.Version},
	})

	want := [][]string{
		{a.Filename(), b.Filename()},
		{a.Filename(), c.Filename()},
	}

	if !equalCycles(graph.Cycles, want) {
		t.Errorf("Cycles = %v, want both loops through %s", graph.Cycles, a.Filename())
		t.Log("These are two loops, not one: breaking a → b leaves a → c → a intact. One report")
		t.Log("per edge that closes a loop is bounded by the number of edges, so reporting both")
		t.Log("costs nothing and reporting one sends an operator back for a second run.")
	}
}

func TestAMissingDependencyIsReportedByName(t *testing.T) {
	only := at(2, "only")
	absent := at(1, "never_written")

	graph := refuse(t, []Migration{only}, Declared{only.Version: {absent.Version}})

	if len(graph.Unknown) != 1 {
		t.Fatalf("Unknown = %v, want the one missing dependency", graph.Unknown)
	}

	if graph.Unknown[0].From != only.Filename() || graph.Unknown[0].Version != absent.Version {
		t.Errorf("Unknown[0] = %+v, want %s wanting %s",
			graph.Unknown[0], only.Filename(), absent.Version)
	}

	for _, want := range []string{only.Filename(), absent.Version} {
		if !strings.Contains(graph.Error(), want) {
			t.Errorf("the report does not name %q:\n%v", want, graph)
		}
	}
}

func TestADeclarationForAMigrationNobodyHasIsReportedToo(t *testing.T) {
	present := at(2, "present")
	deleted := at(1, "deleted")

	graph := refuse(t, []Migration{present}, Declared{deleted.Version: {present.Version}})

	if len(graph.Unknown) != 1 {
		t.Fatalf("Unknown = %v, want the orphaned declaration", graph.Unknown)
	}

	if graph.Unknown[0].From != "" || graph.Unknown[0].Version != deleted.Version {
		t.Errorf("Unknown[0] = %+v, want an orphan naming %s", graph.Unknown[0], deleted.Version)
	}

	if !strings.Contains(graph.Error(), deleted.Version) {
		t.Errorf("the report does not name the version nothing carries:\n%v", graph)
	}

	t.Log("Declarations are collected from somewhere this package does not own, so a key that")
	t.Log("names no discovered migration is that source disagreeing with discovery. Ignored,")
	t.Log("it is a dependency an author wrote and the engine silently never applied.")
}

func TestAMissingDependencyAndACycleAreReportedTogether(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")
	lonely := at(3, "lonely")
	absent := at(9, "never_written")

	graph := refuse(t, []Migration{a, b, lonely}, Declared{
		a.Version:      {b.Version},
		b.Version:      {a.Version},
		lonely.Version: {absent.Version},
	})

	if len(graph.Unknown) != 1 || len(graph.Cycles) != 1 {
		t.Fatalf("got %d unknown and %d cycles, want one of each: %v", len(graph.Unknown),
			len(graph.Cycles), graph)
	}

	t.Log("An edge to a migration that does not exist is dropped before the sort runs, and")
	t.Log("dropping an edge cannot create a loop -- so every cycle left is genuine and neither")
	t.Log("report sends an operator chasing the other. M5-T5 wants every problem in one pass.")
}

func TestTheReportCountsWhatItFound(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")

	one := refuse(t, []Migration{a, b}, Declared{a.Version: {b.Version}, b.Version: {a.Version}})
	if !strings.Contains(one.Error(), "1 problem") {
		t.Errorf("a single problem is not counted as one:\n%v", one)
	}

	y, z := at(3, "y"), at(4, "z")

	two := refuse(t, []Migration{a, b, y, z}, Declared{
		a.Version: {b.Version}, b.Version: {a.Version},
		y.Version: {z.Version}, z.Version: {y.Version},
	})

	if !strings.Contains(two.Error(), "2 problems") {
		t.Errorf("two problems are not counted as two:\n%v", two)
	}
}

func TestTheReportReadsAsOneMessage(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")
	lonely := at(3, "lonely")

	graph := refuse(t, []Migration{a, b, lonely}, Declared{
		a.Version:      {b.Version},
		b.Version:      {a.Version},
		lonely.Version: {"20260101T000000"},
	})

	want := strings.Join([]string{
		"the declared dependencies cannot be ordered (2 problems):",
		"  20260727T150300_lonely.sql depends on version 20260101T000000, which no migration carries",
		"  a cycle, each depending on the next: 20260727T150100_a.sql → 20260727T150200_b.sql → " +
			"20260727T150100_a.sql",
	}, "\n")

	if graph.Error() != want {
		t.Errorf("the report reads\n%s\n\nwant\n%s", graph.Error(), want)
	}

	t.Log("Pinned whole rather than probed for substrings, because this is what an operator")
	t.Log("actually sees and every part of it is a decision: the count, the two orderings, the")
	t.Log("arrow direction, and the loop closing back on the migration it opened with.")
}

func TestEveryMessageNamesFilenamesRatherThanBareVersions(t *testing.T) {
	a, b := at(1, "a"), at(2, "b")

	graph := refuse(t, []Migration{a, b}, Declared{a.Version: {b.Version}, b.Version: {a.Version}})

	for _, want := range []string{a.Filename(), b.Filename()} {
		if !strings.Contains(graph.Error(), want) {
			t.Errorf("the report does not name %q:\n%v", want, graph)
		}
	}

	t.Log("A version is what a declaration names, because it is unique and stable under a")
	t.Log("rename. It is also fifteen digits, and a loop printed as three of them is the least")
	t.Log("useful form of the one error this task exists to produce.")
}

func TestOrderingIsDeterministic(t *testing.T) {
	var found []Migration

	declared := Declared{}

	for i := 1; i <= 12; i++ {
		found = append(found, at(i, fmt.Sprintf("step_%02d", i)))
	}

	for i := 1; i < 12; i++ {
		declared[found[0].Version] = append(declared[found[0].Version], found[i].Version)
	}

	first := order(t, found, declared)

	for run := range 16 {
		if again := order(t, found, declared); !slices.Equal(again, first) {
			t.Fatalf("run %d = %v, want %v", run, again, first)
		}
	}

	t.Log("Declared is a map, and invariant #8 forbids a plan that depends on how one iterates.")
	t.Log("Every ordering decision reads the sorted migration list and looks up; the one place")
	t.Log("a map must be walked -- collecting orphaned declarations -- sorts before reporting.")
}

func TestTheReportIsTheSameEveryTimeItIsProduced(t *testing.T) {
	only := at(9, "only")

	declared := Declared{}
	for i := 1; i <= 6; i++ {
		declared[at(i, "deleted").Version] = []string{only.Version}
	}

	first := refuse(t, []Migration{only}, declared).Error()

	for run := range 32 {
		if again := refuse(t, []Migration{only}, declared).Error(); again != first {
			t.Fatalf("run %d reported\n%s\n\nwant\n%s", run, again, first)
		}
	}

	if !strings.Contains(first, "6 problems") {
		t.Errorf("the report does not count all six orphaned declarations:\n%s", first)
	}

	t.Log("Collecting declarations whose key names no migration is the one place a map has to")
	t.Log("be walked, so it is the one place an unsorted result would leak Go's randomized map")
	t.Log("iteration into a message an operator reads (invariant #8).")
}

func TestTheOrderOfADeclarationDoesNotDecideWhichCycleIsReported(t *testing.T) {
	a, b, c := at(1, "a"), at(2, "b"), at(3, "c")

	forward := refuse(t, []Migration{a, b, c}, Declared{
		a.Version: {b.Version, c.Version},
		b.Version: {a.Version},
		c.Version: {a.Version},
	})

	backward := refuse(t, []Migration{a, b, c}, Declared{
		a.Version: {c.Version, b.Version},
		b.Version: {a.Version},
		c.Version: {a.Version},
	})

	if forward.Error() != backward.Error() {
		t.Errorf("the same graph reported two ways:\n%v\n%v", forward, backward)
		t.Log("A caller builds its dependency slice however it likes. If that order chose which")
		t.Log("loop was reported, the message would move for a reason nobody changed.")
	}
}

func TestOrderDoesNotDisturbWhatItWasGiven(t *testing.T) {
	found := []Migration{at(3, "third"), at(1, "first"), at(2, "second")}
	before := slices.Clone(found)

	if _, err := Order(found, nil); err != nil {
		t.Fatalf("Order: %v", err)
	}

	if !slices.Equal(found, before) {
		t.Errorf("Order sorted its caller's slice in place: %v", filenames(found))
	}
}

// equalCycles compares two cycle reports, which are ordered slices of ordered filenames.
func equalCycles(got, want [][]string) bool {
	if len(got) != len(want) {
		return false
	}

	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			return false
		}
	}

	return true
}
