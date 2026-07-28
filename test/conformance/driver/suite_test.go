package driver

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// rebuildReason is the refusal both sides of an agreeing driver give for drop_column.
const rebuildReason = "requires a full table rebuild before 3.35"

// refusing returns rules that answer native everywhere but the one operation named.
func refusing(name string, kind ir.Kind, reason string) map[ir.Kind]capability.Rule {
	rules := declaring(name, native)
	rules[kind] = capability.Fixed(capability.Unsupported(reason))

	return rules
}

func TestEveryOperationIsCompilableOrRefusedWithAReason(t *testing.T) {
	compiling := suiteOf(t, newFake("fake"), native)
	if problems := compiling.check(); problems != nil {
		t.Errorf("a driver that compiles everything it declares does not pass: %v", problems)
	}

	d := newFake("fake")
	d.refusals[ir.KindDropColumn] = driver.Unsupported("fake", ir.KindDropColumn, rebuildReason)

	refused := suiteDeclaring(t, d, refusing("fake", ir.KindDropColumn, rebuildReason))
	if problems := refused.check(); problems != nil {
		t.Errorf("a driver that refuses what it declares unsupported does not pass: %v", problems)
	}
}

func TestCompilingAnOperationDeclaredUnsupportedFails(t *testing.T) {
	s := suiteDeclaring(t, newFake("fake"), refusing("fake", ir.KindDropColumn, rebuildReason))

	problems := joined(s.check())
	if !strings.Contains(problems, "drop_column: it compiles, but the driver declares it") {
		t.Errorf("problems %q do not report the disagreement", problems)
		t.Log("The declaration is what the validator reads before execution, so an operator")
		t.Log("would be told the operation is impossible while the driver would have run it.")
	}
}

func TestRefusingAnOperationDeclaredSupportedFails(t *testing.T) {
	d := newFake("fake")
	d.refusals[ir.KindDropColumn] = driver.Unsupported("fake", ir.KindDropColumn, rebuildReason)

	s := suiteOf(t, d, native)

	problems := joined(s.check())
	if !strings.Contains(problems, "drop_column: Compile refuses it, but the driver declares it") {
		t.Errorf("problems %q do not report the disagreement", problems)
		t.Log("This is the direction that reaches production: migr plan says the operation is")
		t.Log("native, validation lets it through, and it fails at apply time instead.")
	}
}

func TestAFailureThatIsNotARefusalFails(t *testing.T) {
	d := newFake("fake")
	d.refusals[ir.KindDropTable] = errors.New("syntax error near CREATE")

	s := suiteOf(t, d, native)

	problems := joined(s.check())

	for _, want := range []string{"drop_table: compiling failed", "no third possibility"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q do not mention %q", problems, want)
			t.Log("An operation is either compilable or refused with a reason. A compiler bug is")
			t.Log("neither, and accepting it as a refusal would make the claim true by")
			t.Log("construction and prove nothing.")
		}
	}
}

func TestARefusalWithNoReasonFails(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "blank reason", err: driver.Unsupported("fake", ir.KindDropColumn, "  \t ")},
		{name: "bare sentinel", err: driver.ErrUnsupported},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newFake("fake")
			d.refusals[ir.KindDropColumn] = tt.err

			s := suiteDeclaring(t, d, refusing("fake", ir.KindDropColumn, rebuildReason))

			problems := joined(s.check())
			if !strings.Contains(problems, "refused without a reason an operator can act on") {
				t.Errorf("problems %q accept a refusal that explains nothing", problems)
				t.Log("driver.Unsupported validates none of its three arguments, so a refusal")
				t.Log("carrying no reason is something only this suite and the golden harness")
				t.Log("ever notice.")
			}
		})
	}

	t.Run("the declaration side", func(t *testing.T) {
		d := newFake("fake")
		d.refusals[ir.KindDropColumn] = driver.Unsupported("fake", ir.KindDropColumn, rebuildReason)

		s := suiteDeclaring(t, d, refusing("fake", ir.KindDropColumn, ""))

		if problems := s.check(); problems != nil {
			t.Errorf("a declared refusal with no reason was reported: %v", problems)
			t.Log("capability.Unsupported substitutes text for a blank reason, so this passes")
			t.Log("today and the check below it cannot fire. The suite makes it anyway: if that")
			t.Log("substitution is ever removed, this test is what fails.")
		}
	})
}

func TestARefusalMustNameTheOperationAndTheDriver(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "another operation",
			err:  driver.Unsupported("fake", ir.KindDropTable, rebuildReason),
			want: "the refusal names drop_table, which is not the operation it was asked about",
		},
		{
			name: "another driver",
			err:  driver.Unsupported("sqlite", ir.KindDropColumn, rebuildReason),
			want: `the refusal names driver "sqlite", but this suite runs "fake"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newFake("fake")
			d.refusals[ir.KindDropColumn] = tt.err

			s := suiteDeclaring(t, d, refusing("fake", ir.KindDropColumn, rebuildReason))

			if problems := joined(s.check()); !strings.Contains(problems, tt.want) {
				t.Errorf("problems %q do not report %q", problems, tt.want)
				t.Log("An operator reads the refusal to learn which engine refused and what it")
				t.Log("refused. A refusal naming neither correctly is a message about nothing.")
			}
		})
	}
}

func TestCompilingToNoStatementsFails(t *testing.T) {
	tests := []struct {
		name  string
		stmts []driver.Statement
		want  string
	}{
		{
			name:  "nothing at all",
			stmts: nil,
			want:  "it compiled to no statements at all, and reported no refusal",
		},
		{
			name:  "a statement with no SQL",
			stmts: []driver.Statement{{SQL: "-- drop_table"}, {SQL: "  \n "}},
			want:  "statement 2 carries no SQL",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := newFake("fake")
			d.silent[ir.KindDropTable] = tt.stmts

			s := suiteOf(t, d, native)

			if problems := joined(s.check()); !strings.Contains(problems, tt.want) {
				t.Errorf("problems %q accept a migration that changes nothing", problems)
				t.Log("An operation that compiles to nothing and refuses nothing is the other")
				t.Log("third possibility: migr up reports success and the schema is untouched.")
			}
		})
	}
}

func TestAnUnrecognizedLockImpactFails(t *testing.T) {
	for _, impact := range []capability.LockImpact{"", "banana"} {
		t.Run("impact "+impact.String(), func(t *testing.T) {
			s := suiteOf(t, newFake("fake"), capability.Native(impact))

			problems := joined(s.check())
			if !strings.Contains(problems, "names no verdict this build recognizes") {
				t.Errorf("problems %q accept a lock verdict nothing can compare", problems)
				t.Log("An unrecognized impact satisfies no ceiling at all (ADR 0012), so")
				t.Log("--require-safe and max_lock_impact both fail open for that operation.")
				t.Log("The zero LockImpact is the case a driver reaches by forgetting the field.")
			}
		})
	}
}

func TestTheSetMustBelongToTheDriverUnderTest(t *testing.T) {
	d := newFake("fake")

	other := Suite{Driver: d, Conn: &fakeConn{caps: mustSet(t, "sqlite", declaring("sqlite", native))}}

	problems := joined(other.check())
	if !strings.Contains(problems, `this connection declares capabilities for "sqlite"`) {
		t.Errorf("problems %q accept a session describing a different engine", problems)
		t.Log("Facts.Driver is what every refusal message names, so a set built with the wrong")
		t.Log("one tells an operator that the wrong engine cannot do something.")
	}

	unbuilt := Suite{Driver: d, Conn: &fakeConn{}}

	problems = joined(unbuilt.check())
	if !strings.Contains(problems, "no driver at all") {
		t.Errorf("problems %q accept a capability set that was never built", problems)
		t.Log("The zero Set refuses every operation while naming nobody, so without this the")
		t.Log("suite would demand the driver refuse all twenty and call that conformance.")
	}
}

func TestARawOperationAddressedToAnotherEngineMustBeRefused(t *testing.T) {
	d := newFake("fake")
	d.acceptsForeignRaw = true

	rules := declaring("fake", native)
	rules[ir.KindRaw] = capability.Fixed(native)

	s := suiteDeclaring(t, d, rules)

	problems := joined(s.check())
	if !strings.Contains(problems, "every other engine must refuse it") {
		t.Errorf("problems %q let one engine run another engine's SQL", problems)
		t.Log("ir.Raw.Validate only checks both strings are non-blank, so nothing below the")
		t.Log("driver enforces this. It is also where a Rule taking the whole operation is")
		t.Log("load-bearing: raw resolves native or unsupported by payload, not by kind.")
	}
}

func TestAnEmulatedOperationIsAllowed(t *testing.T) {
	s := suiteOf(t, newFake("fake"), capability.Emulated("table rebuild", capability.TableRewrite))

	if problems := s.check(); problems != nil {
		t.Errorf("a driver that emulates every operation does not pass: %v", problems)
		t.Log("Emulated is a way of performing an operation, not a way of refusing it. A suite")
		t.Log("that tested for native would fail SQLite on every rebuild it declares honestly.")
	}
}

func TestTheSuiteAsksAboutEveryOperation(t *testing.T) {
	d := newFake("fake")
	s := suiteOf(t, d, native)

	if problems := s.check(); problems != nil {
		t.Fatalf("a clean driver reported problems: %v", problems)
	}

	counts := make(map[ir.Kind]int, len(d.asked))
	for _, kind := range d.asked {
		counts[kind]++
	}

	for _, kind := range ir.Kinds() {
		want := 1
		if kind == ir.KindRaw {
			want = 2
		}

		if counts[kind] != want {
			t.Errorf("the suite asked about %s %d times, want %d", kind, counts[kind], want)
			t.Log("The suite resolves its operations from ir.Kinds() rather than from what a")
			t.Log("driver offers: an operation nobody asks about can disagree with nothing.")
			t.Log("raw is asked twice -- once addressed here, once addressed elsewhere.")
		}
	}
}

func TestAnOperationTheSuiteNeverAsksAboutIsReported(t *testing.T) {
	problems := joined(checkCoverage([]ir.Kind{ir.KindCreateTable, ir.KindCreateTable}))

	for _, want := range []string{
		"create_table: the suite asks about it 2 times",
		"drop_table: the suite never asks the driver about this operation",
	} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q do not report %q", problems, want)
		}
	}
}

func TestAnUnusableSuiteSaysSo(t *testing.T) {
	tests := []struct {
		name  string
		suite Suite
		want  string
	}{
		{name: "no driver", suite: Suite{Conn: &fakeConn{}}, want: "the suite has no driver"},
		{name: "no connection", suite: Suite{Driver: newFake("fake")}, want: "no open connection"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if problems := joined(tt.suite.check()); !strings.Contains(problems, tt.want) {
				t.Errorf("an unusable suite was accepted: %q", problems)
			}
		})
	}
}

func TestAMalformedFixtureIsATestBugNotADriverBug(t *testing.T) {
	s := suiteOf(t, newFake("fake"), native)

	problems := joined(s.checkOperation(s.Conn.Capabilities(), ir.DropTable{}, "drop_table"))
	if !strings.Contains(problems, "the fixture is not a well-formed operation") {
		t.Errorf("problems %q blame the driver for a broken fixture", problems)
		t.Log("drivertest.Operations is shared, so a regression there would otherwise be")
		t.Log("reported as every driver failing conformance at once.")
	}
}
