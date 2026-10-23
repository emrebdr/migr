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

func TestAnEmulationDeclaredNativeIsADisagreement(t *testing.T) {
	d := emulating("fake", ir.KindAlterColumn)
	caps := verdicts(t, "fake", ir.KindAlterColumn, capability.Native(capability.BriefExclusive))

	problems := Suite{Driver: d, Conn: &expandingConn{&fakeConn{caps: caps}}}.check()
	if !reports(problems, "asks for a session") {
		t.Errorf("declaring an emulation native went unreported: %v", problems)
	}

	t.Log("This is the direction that reaches production: migr plan reports the operation as")
	t.Log("performed directly, and a table rebuild happens anyway. A capability is never")
	t.Log("silently degraded (CLAUDE.md §4.2), so the strategy has to be declared.")
}

func TestAnEmulationThisServerRefusesIsAllowed(t *testing.T) {
	d := emulating("fake", ir.KindAlterColumn)
	caps := verdicts(t, "fake", ir.KindAlterColumn, capability.Unsupported(versionReason))

	problems := Suite{Driver: d, Conn: &expandingConn{&fakeConn{caps: caps}}}.check()
	if problems != nil {
		t.Errorf("a server refusing an emulation was reported as a disagreement: %v", problems)
	}

	t.Log("An emulation is compiled against the database and still declared against the server, so")
	t.Log("a release that cannot perform it refuses it here rather than halfway through a rebuild.")
	t.Log("No driver reaches this today -- MySQL's version gates name no operation it emulates --")
	t.Log("and the next one to grow such a gate must not have to edit this suite.")
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
