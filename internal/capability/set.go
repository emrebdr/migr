package capability

import (
	"fmt"
	"maps"
	"slices"

	"github.com/emrebdr/migr/internal/ir"
)

// Rule answers both axes for one concrete operation, after the server is already known.
type Rule func(op ir.Operation) Verdict

// Fixed returns a Rule whose verdict does not depend on the operation's payload.
func Fixed(v Verdict) Rule {
	return func(ir.Operation) Verdict { return v }
}

// Facts are what holds for a whole connection, independent of any one operation.
type Facts struct {
	// Driver is the driver that answered, spelled as it is registered.
	Driver string
	// Server is the connected server's version, as the driver detected it.
	Server string
	// TransactionalDDL reports whether a failed migration leaves the schema untouched.
	TransactionalDDL bool
}

// Set is one driver's capabilities, already resolved against one connected server.
type Set struct {
	facts Facts
	rules map[ir.Kind]Rule
}

// NewSet builds a capability set, refusing one that does not answer for every operation.
func NewSet(facts Facts, rules map[ir.Kind]Rule) (Set, error) {
	if facts.Driver == "" {
		return Set{}, ErrNoDriver
	}

	missing, unknown := undeclared(rules), unrecognized(rules)
	if len(missing) > 0 || len(unknown) > 0 {
		return Set{}, &IncompleteError{Driver: facts.Driver, Missing: missing, Unknown: unknown}
	}

	return Set{facts: facts, rules: maps.Clone(rules)}, nil
}

// undeclared lists the operations a rule set omits, in the order the IR declares them.
func undeclared(rules map[ir.Kind]Rule) []ir.Kind {
	var missing []ir.Kind

	for _, kind := range ir.Kinds() {
		if rules[kind] == nil {
			missing = append(missing, kind)
		}
	}

	return missing
}

// unrecognized lists rules declared for something that is not an operation, sorted.
func unrecognized(rules map[ir.Kind]Rule) []ir.Kind {
	var extra []ir.Kind

	for kind := range rules {
		if !kind.Valid() {
			extra = append(extra, kind)
		}
	}

	slices.Sort(extra)

	return extra
}

// Driver returns the name of the driver whose capabilities these are.
func (s Set) Driver() string {
	return s.facts.Driver
}

// Server returns the server version these verdicts were resolved against.
func (s Set) Server() string {
	return s.facts.Server
}

// TransactionalDDL reports whether a failed migration leaves this server's schema untouched.
func (s Set) TransactionalDDL() bool {
	return s.facts.TransactionalDDL
}

// Resolve answers both axes for one operation, refusing rather than guessing.
func (s Set) Resolve(op ir.Operation) Verdict {
	if op == nil {
		return Unsupported(fmt.Sprintf("%s was asked about no operation at all", s.describeDriver()))
	}

	rule := s.rules[op.Kind()]
	if rule == nil {
		return Unsupported(fmt.Sprintf("%s declares no capability for %s",
			s.describeDriver(), op.Kind()))
	}

	return s.checked(op.Kind(), rule(op))
}

// checked replaces a verdict a rule failed to build with a refusal naming the driver.
func (s Set) checked(kind ir.Kind, v Verdict) Verdict {
	if v.support.Valid() {
		return v
	}

	return Unsupported(fmt.Sprintf("%s declares an unusable capability for %s",
		s.describeDriver(), kind))
}

// describeDriver names the driver, saying so plainly when a set was never built.
func (s Set) describeDriver() string {
	if s.facts.Driver == "" {
		return "an unbuilt capability set"
	}

	return fmt.Sprintf("driver %q", s.facts.Driver)
}
