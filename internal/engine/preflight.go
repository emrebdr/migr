package engine

import (
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Refusal is one operation the connected server declares it will not perform.
type Refusal struct {
	// Operation is the operation's place in the half it was emitted for, counting from one.
	Operation int32
	// Kind is the operation at fault.
	Kind ir.Kind
	// Subject is the object it acts on, named the way the operation names it.
	Subject string
	// Reason is the driver's own stated reason, carried as the driver wrote it.
	Reason string
}

// Describe renders one refusal as a line, naming the operation, its kind, and the driver's reason.
func (r Refusal) Describe() string {
	return fmt.Sprintf("operation %d (%s) on %q: %s", r.Operation, r.Kind, r.Subject, r.Reason)
}

// Unsupported returns every operation in a list this server refuses, in the order they were emitted.
func Unsupported(ops ir.Operations, caps capability.Set) []Refusal {
	var (
		found []Refusal
		index int32
	)

	for _, op := range ops {
		index++

		verdict := caps.Resolve(op)
		if verdict.Allowed() {
			continue
		}

		found = append(found, Refusal{
			Operation: index,
			Kind:      kindOf(op),
			Subject:   Subject(op),
			Reason:    verdict.Reason(),
		})
	}

	return found
}

// kindOf names the operation a refusal is about, which an empty one in a list has none of.
func kindOf(op ir.Operation) ir.Kind {
	if op == nil {
		return ""
	}

	return op.Kind()
}

// capable refuses a migration this server declares it cannot perform, when something is being run.
func (e *executor) capable(m migration.Migration, dir ir.Direction, ops ir.Operations) error {
	if e.mode != ModeApply {
		return nil
	}

	caps := e.conn.Capabilities()

	found := Unsupported(ops, caps)
	if len(found) == 0 {
		return nil
	}

	return &UnsupportedError{
		Migration: m,
		Version:   m.Version,
		Direction: dir,
		Driver:    caps.Driver(),
		Server:    caps.Server(),
		Found:     found,
	}
}

// describeServer names what resolved these verdicts, which a driver that detected nothing has none of.
func describeServer(driverName, server string) string {
	if server == "" {
		return fmt.Sprintf("driver %q", driverName)
	}

	return fmt.Sprintf("driver %q against server %s", driverName, server)
}

// describeRefusals lists what each operation was refused for, one indented line apiece.
func describeRefusals(found []Refusal) string {
	lines := make([]string, 0, len(found))
	for _, r := range found {
		lines = append(lines, "  "+r.Describe())
	}

	return strings.Join(lines, "\n")
}
