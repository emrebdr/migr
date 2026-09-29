package mysql

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// transactionalDDL is false for MySQL, and it is the premise every later M9 task is built on.
//
// A DDL statement commits the open transaction before it runs and then commits itself, so a rollback
// after one reports success and undoes nothing: measured on both servers, where a row inserted inside
// the transaction survived the ROLLBACK because the CREATE TABLE after it had already committed. From
// 8.0 a single DDL statement is itself atomic -- the data dictionary is transactional, so a failed
// ALTER leaves no half-built table -- which bounds the damage to one statement and does not make a
// migration of several recoverable. That is M9-T5's subject.
const transactionalDDL = false

// The release where MySQL gained parenthesised expression defaults, which three of the four functions need.
//
// Before it, DEFAULT (CURDATE()) is a syntax error and the bare form is one too, so there is no
// spelling of a current_date default that works -- and a literal default on a text, blob or json
// column has none either. MariaDB has had them since 10.2, below this driver's floor, so the gate
// is MySQL's alone. This is the version-dependent verdict invariant #13 asks for, resolved from the
// server rather than assumed.
const (
	expressionDefaultMajor = 8
	expressionDefaultMinor = 0
	expressionDefaultPatch = 13
)

// facts renders what this session is, for a capability set that reports itself.
func facts(version serverVersion) capability.Facts {
	return capability.Facts{
		Driver:           Name,
		Server:           version.String(),
		TransactionalDDL: transactionalDDL,
	}
}

// rules declares every operation against one detected server, so no verdict is version-blind.
func rules(version serverVersion) map[ir.Kind]capability.Rule {
	declared := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		declared[kind] = version.verdict
	}

	return declared
}

// verdict answers both axes for one operation, on the server this declaration was built against.
func (v serverVersion) verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	if problem := v.checkVersion(op); problem != "" {
		return capability.Unsupported(problem)
	}

	return capability.Native(capability.LockUnknown)
}

// checkVersion reports what this server cannot do, which the version-blind compiler cannot know.
func (v serverVersion) checkVersion(op ir.Operation) string {
	created, ok := op.(ir.CreateTable)
	if !ok {
		return ""
	}

	for _, column := range created.Columns {
		if !isExpressionDefault(column) {
			continue
		}

		if problem := v.checkExpressionDefault(column.Name); problem != "" {
			return problem
		}
	}

	return ""
}

// checkExpressionDefault reports a server too old for the only default syntax MySQL would accept.
func (v serverVersion) checkExpressionDefault(column string) string {
	if v.AtLeastPatch(MySQL, expressionDefaultMajor, expressionDefaultMinor,
		expressionDefaultPatch) || v.variant == MariaDB {
		return ""
	}

	return fmt.Sprintf("column %q needs a parenthesised expression default, which MySQL gained in "+
		"%d.%d.%d and this server is %s: a text, blob or json column takes no bare literal default, "+
		"and current_date, current_time and uuid have no bare spelling MySQL accepts at all",
		column, expressionDefaultMajor, expressionDefaultMinor, expressionDefaultPatch, v)
}

// describeRefusal renders a compiler failure as a reason, treating anything unexpected as one.
func describeRefusal(err error) string {
	var refusal *driver.UnsupportedError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}

	return fmt.Sprintf("%s cannot compile this operation: %v", Name, err)
}
