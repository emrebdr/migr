package runtime

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// emitting is a fixture whose up adds one column and whose down drops it.
func emitting(t *testing.T) Fixture {
	t.Helper()

	var m manifest
	if err := strictUnmarshal([]byte(`{
		"reads":[{"sql":"SELECT 1","columns":["n"],"rows":[["1"]]}],
		"writes":[{"sql":"UPDATE t SET a = 1","rowsAffected":"1"}],
		"up":[{"op":"add_column","table":"t","column":{"name":"a","type":"int32","nullable":false}}],
		"down":[{"op":"drop_column","table":"t","column":"a"}]}`), &m); err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	f, err := build(Fixture{Version: "20260727T120001", Name: "fixture"}, m)
	if err != nil {
		t.Fatalf("building the fixture: %v", err)
	}

	return f
}

// refusing is a fixture that states §7.4's declaration rather than an undo.
func refusing(t *testing.T, code, reason string) Fixture {
	t.Helper()

	f := emitting(t)
	f.Down, f.down = nil, nil
	f.Refusal = &Refusal{Code: code, Reason: reason}

	return f
}

// emitted renders one answer a runtime could give, so judge can be asked about it directly.
func emitted(t *testing.T, document string, held ...driver.Query) engine.Emitted {
	t.Helper()

	var ops ir.Operations
	if err := ir.Unmarshal([]byte(document), &ops); err != nil {
		t.Fatalf("reading the answer: %v", err)
	}

	return engine.Emitted{Operations: ops, Direct: held}
}

func TestJudgeReportsOperationsThatAreNotTheOnesStated(t *testing.T) {
	f := emitting(t)

	problems := judge("here", f, ir.Up, engine.ModeApply,
		emitted(t, `[{"op":"add_column","table":"t","column":{"name":"b","type":"int32","nullable":false}}]`),
		nil, nil)

	assertReports(t, problems, "are not the ones this fixture states")

	t.Log("This is the assertion the whole suite exists to make, and in a repository where every")
	t.Log("language already agrees, nothing else would ever exercise it.")
}

func TestJudgeAcceptsTheOperationsTheFixtureStates(t *testing.T) {
	f := emitting(t)

	for _, dir := range []ir.Direction{ir.Up, ir.Down} {
		document := string(f.Expected(dir))

		held := []driver.Query{{SQL: "UPDATE t SET a = 1", Args: []string{}}}
		if dir == ir.Down {
			held = nil
		}

		if problems := judge("here", f, dir, engine.ModePlan,
			emitted(t, document, held...), nil, nil); len(problems) != 0 {
			t.Errorf("%s: the fixture's own bytes were rejected: %v", dir, problems)
		}
	}
}

func TestJudgeReportsAHeldStatementThatIsNotStated(t *testing.T) {
	f := emitting(t)

	for _, held := range []struct {
		why  string
		is   []driver.Query
		mode engine.Mode
		says string
	}{
		{why: "a preview held nothing", mode: engine.ModePlan, says: "it held 0 statements"},
		{why: "a preview held the wrong statement", mode: engine.ModePlan,
			is:   []driver.Query{{SQL: "DELETE FROM t", Args: []string{}}},
			says: "held statement 1 is"},
		{why: "a run held one anyway", mode: engine.ModeApply,
			is:   []driver.Query{{SQL: "UPDATE t SET a = 1", Args: []string{}}},
			says: "it held 1 statements"},
	} {
		t.Run(held.why, func(t *testing.T) {
			problems := judge("here", f, ir.Up, held.mode,
				emitted(t, string(f.Expected(ir.Up)), held.is...), nil, nil)

			assertReports(t, problems, held.says)
		})
	}

	t.Log("ADR 0036: mode plan records host.db.exec and mode apply runs it, so which of the two")
	t.Log("holds the statements is the difference between the modes and the only one.")
}

func TestJudgeCarriesWhatTheSessionItselfSaw(t *testing.T) {
	f := emitting(t)

	problems := judge("here", f, ir.Up, engine.ModeApply,
		emitted(t, string(f.Expected(ir.Up))), nil, []string{"the session saw something"})

	assertReports(t, problems, "the session saw something")
}

func TestJudgeReportsARefusalThatIsNotTheOneStated(t *testing.T) {
	for _, refused := range []struct {
		why  string
		f    Fixture
		got  error
		says string
	}{
		{why: "a derivation came back as a declaration",
			f:    refusing(t, NoDownAuthored, ""),
			got:  &engine.DeclaredIrreversibleError{Reason: "because"},
			says: "authors no down of its own"},
		{why: "a declaration came back as a derivation",
			f:    refusing(t, DeclaredIrreversible, "because"),
			got:  engine.ErrNoDownAuthored,
			says: "declares that it cannot be undone"},
		{why: "a declaration lost the author's reason",
			f:    refusing(t, DeclaredIrreversible, "because"),
			got:  &engine.DeclaredIrreversibleError{Reason: "some other reason"},
			says: `it halts with "some other reason"`},
		{why: "a fixture that emits failed instead",
			f: emitting(t), got: errors.New("it fell over"),
			says: "this fixture states what it emits"},
	} {
		t.Run(refused.why, func(t *testing.T) {
			problems := judge("here", refused.f, ir.Down, engine.ModeApply,
				engine.Emitted{}, refused.got, nil)

			assertReports(t, problems, refused.says)
		})
	}
}

func TestJudgeAcceptsTheRefusalTheFixtureStates(t *testing.T) {
	for _, accepted := range []struct {
		f   Fixture
		got error
	}{
		{f: refusing(t, NoDownAuthored, ""), got: engine.ErrNoDownAuthored},
		{f: refusing(t, DeclaredIrreversible, "because"),
			got: &engine.DeclaredIrreversibleError{Reason: "because"}},
	} {
		if problems := judge("here", accepted.f, ir.Down, engine.ModeApply,
			engine.Emitted{}, accepted.got, nil); len(problems) != 0 {
			t.Errorf("the stated refusal was rejected: %v", problems)
		}
	}
}

func TestJudgeReportsAFixtureThatEmittedWhereItMustRefuse(t *testing.T) {
	f := refusing(t, NoDownAuthored, "")

	problems := judge("here", f, ir.Down, engine.ModeApply,
		emitted(t, `[{"op":"drop_column","table":"t","column":"a"}]`), nil, nil)

	assertReports(t, problems, "answers "+NoDownAuthored)
}

func TestMatchDiscoveredReportsWhatOnlyOneSideHas(t *testing.T) {
	f := emitting(t)

	for _, mismatched := range []struct {
		why   string
		files map[string]migration.Migration
		says  string
	}{
		{why: "the runtime loaded nothing for a fixture",
			files: map[string]migration.Migration{},
			says:  "loaded no migration for the fixture"},
		{why: "the runtime loaded it under another name",
			files: map[string]migration.Migration{
				f.Version: {Version: f.Version, Name: "something_else", Ext: ".go"},
			},
			says: "carries one name across every language"},
		{why: "the runtime loaded a migration no fixture defines",
			files: map[string]migration.Migration{
				f.Version:         {Version: f.Version, Name: f.Name, Ext: ".go"},
				"20991231T235959": {Version: "20991231T235959", Name: "extra", Ext: ".go"},
			},
			says: "which no fixture defines"},
	} {
		t.Run(mismatched.why, func(t *testing.T) {
			assertReports(t, matchDiscovered(mismatched.files, []Fixture{f}), mismatched.says)
		})
	}
}

func TestMatchDiscoveredAcceptsASetLoadedExactly(t *testing.T) {
	f := emitting(t)

	problems := matchDiscovered(map[string]migration.Migration{
		f.Version: {Version: f.Version, Name: f.Name, Ext: ".go"},
	}, []Fixture{f})

	if len(problems) != 0 {
		t.Errorf("a set loaded exactly was reported: %v", problems)
	}
}

func TestCheckAskedReportsAFixtureNotPutEveryQuestion(t *testing.T) {
	f := emitting(t)

	if problems := checkAsked(f, 2*len(engine.Modes())); len(problems) != 0 {
		t.Errorf("a fixture asked everything was reported: %v", problems)
	}

	assertReports(t, checkAsked(f, 2), "both directions, in both modes")
}

func TestAnEmptyFixtureSetIsRefusedRatherThanPassed(t *testing.T) {
	if _, err := order(nil); !errors.Is(err, ErrNoFixtures) {
		t.Errorf("an empty set reported %v, want %v", err, ErrNoFixtures)
	}
}

func TestCoverageIsCheckedBeforeAnythingIsStarted(t *testing.T) {
	f := emitting(t)

	problems := Suite{fixtures: func() ([]Fixture, error) { return []Fixture{f}, nil }}.
		check(t.Context())

	assertReports(t, problems, "no fixture emits this operation")

	t.Log("Coverage is a fact about the fixture set, not about a language, so it is settled before")
	t.Log("a process is started -- which is also what lets this test exercise the call site.")
}

func TestAFixtureSetThatCannotBeReadIsReportedRatherThanSkipped(t *testing.T) {
	problems := Suite{fixtures: func() ([]Fixture, error) { return nil, ErrNoFixtures }}.
		check(t.Context())

	assertReports(t, problems, "the fixture set could not be read")
}

// assertReports reports a set of problems that does not carry the one an author needs to read.
func assertReports(t *testing.T, problems []string, says string) {
	t.Helper()

	if len(problems) == 0 {
		t.Fatalf("nothing was reported, and %q was expected", says)
	}

	if !strings.Contains(strings.Join(problems, "\n"), says) {
		t.Errorf("the problems do not say %q: %v", says, problems)
	}
}
