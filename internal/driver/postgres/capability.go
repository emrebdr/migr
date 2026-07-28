package postgres

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// facts renders what this server is, for a capability set that reports itself.
func (f serverFacts) facts() capability.Facts {
	return capability.Facts{
		Driver:           Name,
		Server:           f.version.String(),
		TransactionalDDL: transactionalDDL,
	}
}

// rules declares every operation against one detected server, so no verdict is version-blind.
func (f serverFacts) rules() map[ir.Kind]capability.Rule {
	declared := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		declared[kind] = f.verdict
	}

	return declared
}

// verdict answers both axes for one operation, on the server this declaration was built against.
func (f serverFacts) verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	if problem := f.checkServer(op); problem != "" {
		return capability.Unsupported(problem)
	}

	return capability.Native(f.lockImpact(op))
}

// checkServer reports what this server cannot do, which the version-blind compiler cannot know.
func (f serverFacts) checkServer(op ir.Operation) string {
	switch o := op.(type) {
	case ir.CreateTable:
		return f.checkTableDefaults(o)
	case ir.AddColumn:
		return f.checkDefaultFunction(o.Column.Default)
	case ir.AlterColumn:
		return f.checkDefaultFunction(o.Default)
	default:
		return ""
	}
}

// checkTableDefaults reports the first column default a new table declares that this server lacks.
func (f serverFacts) checkTableDefaults(op ir.CreateTable) string {
	for _, column := range op.Columns {
		if problem := f.checkDefaultFunction(column.Default); problem != "" {
			return problem
		}
	}

	return ""
}

// checkDefaultFunction reports a default function this server has no implementation of.
func (f serverFacts) checkDefaultFunction(d *ir.Default) string {
	if d == nil || d.Function != ir.FuncUUID || f.uuidDefault.exists {
		return ""
	}

	return fmt.Sprintf("the %s default compiles to %s, which this server does not have: it moved "+
		"into core in PostgreSQL %d and this server reports %s. Install pgcrypto, or generate the "+
		"value in the application", ir.FuncUUID, uuidFunction, uuidInCoreRelease, f.version)
}

// describeRefusal renders a compiler failure as a reason, treating anything unexpected as one.
func describeRefusal(err error) string {
	var refusal *driver.UnsupportedError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}

	return fmt.Sprintf("%s cannot compile this operation: %v", Name, err)
}
