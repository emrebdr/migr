package driver

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// expandingConn is a session that can produce statements for an operation its compiler cannot.
type expandingConn struct {
	*fakeConn
}

// Expand returns one statement, which is all this fake needs to satisfy the interface.
func (c *expandingConn) Expand(context.Context, ir.Operation) ([]driver.Statement, error) {
	return []driver.Statement{{SQL: "-- rebuilt"}}, nil
}

// emulating returns a driver that compiles everything but one operation, which it emulates.
func emulating(name string, kind ir.Kind) *fakeDriver {
	d := newFake(name)
	d.refusals[kind] = driver.NeedsSession(name, kind, "it is performed by a table rebuild")

	return d
}

// verdicts returns a rule set declaring one operation the given way and everything else native.
func verdicts(t *testing.T, name string, kind ir.Kind, verdict capability.Verdict) capability.Set {
	t.Helper()

	rules := declaring(name, native)
	rules[kind] = capability.Fixed(verdict)

	return mustSet(t, name, rules)
}

func TestAnOperationCompiledAgainstASessionMayBeDeclaredEmulated(t *testing.T) {
	d := emulating("fake", ir.KindAlterColumn)
	caps := verdicts(t, "fake", ir.KindAlterColumn,
		capability.Emulated("table rebuild", capability.TableRewrite))

	problems := Suite{Driver: d, Conn: &expandingConn{&fakeConn{caps: caps}}}.check()
	if len(problems) != 0 {
		t.Errorf("a well-formed emulation was reported as a disagreement: %v", problems)
	}

	t.Log("Allowed() is false exactly when Compile refuses *and* does not ask for a session. An")
	t.Log("emulation is the third answer: the driver performs the operation, and what it needs")
	t.Log("first is the live schema, which Compile has no connection to read.")
}

func TestAnEmulationTheDeclarationDoesNotClaimIsADisagreement(t *testing.T) {
	d := emulating("fake", ir.KindAlterColumn)

	for name, verdict := range map[string]capability.Verdict{
		"native":      capability.Native(capability.BriefExclusive),
		"unsupported": capability.Unsupported("this driver does not do that"),
	} {
		t.Run(name, func(t *testing.T) {
			caps := verdicts(t, "fake", ir.KindAlterColumn, verdict)

			problems := Suite{Driver: d, Conn: &expandingConn{&fakeConn{caps: caps}}}.check()
			if !reports(problems, "asks for a session") {
				t.Errorf("declaring it %s went unreported: %v", name, problems)
			}
		})
	}

	t.Log("The compiler and the declaration are two statements of one fact, and this is the")
	t.Log("direction that reaches production: a driver that declares an emulation unsupported")
	t.Log("refuses a migration it could have run, and one that declares it native passes")
	t.Log("validation and then has nothing to produce the statements with.")
}

func TestAnEmulationWithNoSessionToExpandItIsADisagreement(t *testing.T) {
	d := emulating("fake", ir.KindAlterColumn)
	caps := verdicts(t, "fake", ir.KindAlterColumn,
		capability.Emulated("table rebuild", capability.TableRewrite))

	problems := Suite{Driver: d, Conn: &fakeConn{caps: caps}}.check()
	if !reports(problems, "driver.Expander") {
		t.Errorf("a session that cannot expand went unreported: %v", problems)
	}

	t.Log("The declaration promises statements that something has to produce. A session that is")
	t.Log("no Expander is the one case where both halves agree with each other and neither can")
	t.Log("be kept, which is why the suite asks the session rather than only the two answers.")
}

func TestARequestForASessionMustNameItselfProperly(t *testing.T) {
	caps := verdicts(t, "fake", ir.KindAlterColumn,
		capability.Emulated("table rebuild", capability.TableRewrite))

	for name, err := range map[string]error{
		"without a reason": driver.NeedsSession("fake", ir.KindAlterColumn, "  "),
		"another op":       driver.NeedsSession("fake", ir.KindAddIndex, "a reason"),
		"another driver":   driver.NeedsSession("mysql", ir.KindAlterColumn, "a reason"),
	} {
		t.Run(name, func(t *testing.T) {
			d := newFake("fake")
			d.refusals[ir.KindAlterColumn] = err

			problems := Suite{Driver: d, Conn: &expandingConn{&fakeConn{caps: caps}}}.check()
			if len(problems) == 0 {
				t.Error("it was accepted as this driver's honest answer")
			}
		})
	}

	t.Log("driver.NeedsSession validates none of this, exactly as driver.Unsupported validates")
	t.Log("none of the equivalent, so the suite is what holds an answer to naming itself.")
}

// reports says whether any problem mentions a fragment, for a test about which one was found.
func reports(problems []string, fragment string) bool {
	for _, problem := range problems {
		if strings.Contains(problem, fragment) {
			return true
		}
	}

	return false
}
