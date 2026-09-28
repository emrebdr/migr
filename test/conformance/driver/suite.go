package driver

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
)

// foreignDriver names an engine that is not the one under test, whatever the one under test is.
const foreignDriver = "another-engine"

// foreignRawFixture is a comment rather than a statement, so no engine SQL is authored here.
const foreignRawFixture = "-- a raw statement addressed to a different engine"

// Suite is one driver and one open session, held to what every driver must satisfy.
type Suite struct {
	// Driver is the driver under test: it names itself and it compiles.
	Driver driver.Driver
	// Conn is an open session, which is where the capability declaration lives.
	Conn driver.Conn
}

// Run walks the full v1 operation set and reports every disagreement it finds.
func (s Suite) Run(t *testing.T) {
	t.Helper()

	for _, problem := range s.check() {
		t.Error(problem)
	}
}

// check reports every place a driver's compiler and its capability declaration disagree.
func (s Suite) check() []string {
	if problems := s.checkSuite(); problems != nil {
		return problems
	}

	name := s.Driver.Name()

	caps := s.Conn.Capabilities()
	if problem := describeForeignSet(name, caps); problem != "" {
		return []string{problem}
	}

	problems, asked := s.walk(caps, drivertest.Operations(name))
	problems = append(problems, checkCoverage(asked)...)

	return append(problems, s.checkForeignRaw(caps)...)
}

// checkSuite reports a suite that cannot ask a driver anything at all.
func (s Suite) checkSuite() []string {
	if s.Driver == nil {
		return []string{"the suite has no driver, so there is nothing to hold to the contract"}
	}

	if s.Conn == nil {
		return []string{s.Driver.Name() + ": the suite has no open connection, and a driver's " +
			"capability declaration lives on the session"}
	}

	return nil
}

// describeForeignSet reports a session whose capability declaration belongs to another driver.
func describeForeignSet(name string, caps capability.Set) string {
	if caps.Driver() == name {
		return ""
	}

	return fmt.Sprintf("%s: this connection declares capabilities for %s, so every verdict the "+
		"suite resolved would describe a different engine", name, describeDeclaredDriver(caps))
}

// describeDeclaredDriver names the driver a set belongs to, saying so plainly when it has none.
func describeDeclaredDriver(caps capability.Set) string {
	if caps.Driver() == "" {
		return "no driver at all, which is what a capability set that was never built reports"
	}

	return fmt.Sprintf("%q", caps.Driver())
}

// walk checks every operation in turn and reports which kinds it actually asked the driver about.
func (s Suite) walk(caps capability.Set, ops ir.Operations) ([]string, []ir.Kind) {
	var problems []string

	asked := make([]ir.Kind, 0, len(ops))

	for _, op := range ops {
		asked = append(asked, op.Kind())
		problems = append(problems, s.checkOperation(caps, op, op.Kind().String())...)
	}

	return problems, asked
}

// checkOperation holds one operation's compiled result against what the driver declared for it.
func (s Suite) checkOperation(caps capability.Set, op ir.Operation, label string) []string {
	if err := op.Validate(); err != nil {
		return []string{fmt.Sprintf("%s: the fixture is not a well-formed operation: %v", label, err)}
	}

	verdict := caps.Resolve(op)

	stmts, err := s.Driver.Compile(op)

	switch {
	case errors.Is(err, driver.ErrNeedsSession):
		return s.checkEmulated(op, label, verdict, err)
	case err != nil:
		return s.checkRefused(op, label, verdict, err)
	default:
		return checkCompiled(label, verdict, stmts)
	}
}

// checkEmulated reports a driver that asks for a session for an operation it did not declare emulated.
func (s Suite) checkEmulated(op ir.Operation, label string, verdict capability.Verdict,
	err error,
) []string {
	var needs *driver.NeedsSessionError
	if !errors.As(err, &needs) {
		return []string{describeFailure(label, err)}
	}

	problems := s.checkNeedsSession(op, label, needs)

	if verdict.Support() != capability.SupportEmulated {
		problems = append(problems, fmt.Sprintf("%s: Compile asks for a session, which is what an "+
			"emulation needs, but the driver declares it %q; the two are one statement of one fact",
			label, verdict))
	}

	if _, expands := s.Conn.(driver.Expander); !expands {
		problems = append(problems, label+": the driver compiles this operation against a session "+
			"and its session is no driver.Expander, so nothing can produce the statements the "+
			"declaration promises")
	}

	if verdict.Allowed() && !verdict.Lock().Valid() {
		problems = append(problems, fmt.Sprintf("%s: the driver declares lock impact %q, which "+
			"names no verdict this build recognizes, so it satisfies no safety ceiling at all",
			label, verdict.Lock()))
	}

	return problems
}

// checkNeedsSession reports a request for a session naming no reason, the wrong op, or another driver.
func (s Suite) checkNeedsSession(op ir.Operation, label string,
	needs *driver.NeedsSessionError,
) []string {
	var problems []string

	if strings.TrimSpace(needs.Reason) == "" {
		problems = append(problems, label+": it asks for a session without saying what for")
	}

	if needs.Kind != op.Kind() {
		problems = append(problems, fmt.Sprintf("%s: it names %s, which is not the operation it "+
			"was asked about", label, needs.Kind))
	}

	if needs.Driver != s.Driver.Name() {
		problems = append(problems, fmt.Sprintf("%s: it names driver %q, and this suite is "+
			"compiling for %q", label, needs.Driver, s.Driver.Name()))
	}

	return problems
}

// checkCompiled reports an operation that compiled but disagrees with what the driver declared.
func checkCompiled(label string, verdict capability.Verdict, stmts []driver.Statement) []string {
	if !verdict.Allowed() {
		return []string{fmt.Sprintf("%s: it compiles, but the driver declares it %q; the "+
			"declaration is authoritative, so one of the two is wrong", label, verdict)}
	}

	var problems []string

	if problem := describeEmptyOutput(label, stmts); problem != "" {
		problems = append(problems, problem)
	}

	if !verdict.Lock().Valid() {
		problems = append(problems, fmt.Sprintf("%s: the driver declares lock impact %q, which "+
			"names no verdict this build recognizes, so it satisfies no safety ceiling at all",
			label, verdict.Lock()))
	}

	return problems
}

// describeEmptyOutput reports a compile that runs a migration nowhere while reporting no refusal.
func describeEmptyOutput(label string, stmts []driver.Statement) string {
	if len(stmts) == 0 {
		return label + ": it compiled to no statements at all, and reported no refusal"
	}

	for i, stmt := range stmts {
		if strings.TrimSpace(stmt.SQL) == "" {
			return fmt.Sprintf("%s: statement %d carries no SQL, so it changes nothing while "+
				"reporting success", label, i+1)
		}
	}

	return ""
}

// checkRefused reports a failure that is not a named refusal, or one the declaration contradicts.
func (s Suite) checkRefused(op ir.Operation, label string, verdict capability.Verdict, err error) []string {
	var refusal *driver.UnsupportedError
	if !errors.As(err, &refusal) {
		return []string{describeFailure(label, err)}
	}

	problems := s.checkRefusal(op, label, refusal)

	if verdict.Allowed() {
		return append(problems, fmt.Sprintf("%s: Compile refuses it, but the driver declares it "+
			"%q; the declaration is authoritative, so one of the two is wrong", label, verdict))
	}

	if strings.TrimSpace(verdict.Reason()) == "" {
		problems = append(problems, label+": the driver declares it unsupported without giving a "+
			"reason an operator can act on")
	}

	return problems
}

// describeFailure separates a refusal carrying no reason from an ordinary compiler failure.
func describeFailure(label string, err error) string {
	if errors.Is(err, driver.ErrUnsupported) {
		return fmt.Sprintf("%s: refused without a reason an operator can act on: %v", label, err)
	}

	return fmt.Sprintf("%s: compiling failed: %v; an operation is either compilable or refused "+
		"with a reason, and there is no third possibility", label, err)
}

// checkRefusal reports a refusal naming no reason, the wrong operation, or another driver.
func (s Suite) checkRefusal(op ir.Operation, label string, refusal *driver.UnsupportedError) []string {
	var problems []string

	if strings.TrimSpace(refusal.Reason) == "" {
		problems = append(problems, label+": refused without a reason an operator can act on")
	}

	if refusal.Kind != op.Kind() {
		problems = append(problems, fmt.Sprintf("%s: the refusal names %s, which is not the "+
			"operation it was asked about", label, refusal.Kind))
	}

	if refusal.Driver != s.Driver.Name() {
		problems = append(problems, fmt.Sprintf("%s: the refusal names driver %q, but this suite "+
			"runs %q", label, refusal.Driver, s.Driver.Name()))
	}

	return problems
}

// checkCoverage reports operations the walk never asked about, which can disagree with nothing.
func checkCoverage(asked []ir.Kind) []string {
	counts := make(map[ir.Kind]int, len(asked))
	for _, kind := range asked {
		counts[kind]++
	}

	var problems []string

	for _, kind := range ir.Kinds() {
		if count := counts[kind]; count != 1 {
			problems = append(problems, describeCoverage(kind, count))
		}
	}

	return problems
}

// describeCoverage reports an operation the suite asked about no times, or more than one.
func describeCoverage(kind ir.Kind, count int) string {
	if count == 0 {
		return fmt.Sprintf("%s: the suite never asks the driver about this operation, so it "+
			"cannot disagree with what the driver declared for it", kind)
	}

	return fmt.Sprintf("%s: the suite asks about it %d times, where the operation set holds "+
		"exactly one of each", kind, count)
}

// checkForeignRaw reports a driver that will run a raw operation addressed to another engine.
func (s Suite) checkForeignRaw(caps capability.Set) []string {
	if s.Driver.Name() == foreignDriver {
		return []string{fmt.Sprintf("this driver is named %q, which the suite uses as the engine "+
			"a raw operation is addressed to; rename one of the two", foreignDriver)}
	}

	op := ir.Raw{Driver: foreignDriver, SQL: foreignRawFixture}
	label := fmt.Sprintf("%s addressed to %q", op.Kind(), foreignDriver)

	problems := s.checkOperation(caps, op, label)

	if caps.Resolve(op).Allowed() {
		problems = append(problems, label+": a raw operation names the engine it targets, and "+
			"every other engine must refuse it")
	}

	return problems
}
