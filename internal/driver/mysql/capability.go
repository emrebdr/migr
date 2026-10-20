package mysql

import (
	"context"
	"errors"
	"fmt"
	"strings"

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

// redefineStrategy names how this driver achieves a column change, which migr plan displays.
const redefineStrategy = "column redefinition"

// The release where MySQL began enforcing a check constraint rather than parsing and discarding it.
//
// Below it the syntax is accepted, the constraint does not exist, and the migration reports success --
// which is the worst answer available, so it is refused instead. MariaDB has enforced them since
// 10.2.1, below this driver's floor.
const (
	enforcedCheckMajor = 8
	enforcedCheckMinor = 0
	enforcedCheckPatch = 16
)

// The release where MySQL gained DROP CONSTRAINT, which is the one spelling both servers drop a check by.
//
// MariaDB has no DROP CHECK at all -- error 1064 on 11.4.13, measured -- and MySQL has had DROP CHECK
// since 8.0.16 but DROP CONSTRAINT only since 8.0.19. A pure Compile cannot pick by variant, so one
// spelling is emitted and the three releases that enforce a check without being able to drop one that
// way are refused.
const (
	dropConstraintMajor = 8
	dropConstraintMinor = 0
	dropConstraintPatch = 19
)

// The MariaDB release that gained RENAME COLUMN, which is just above this driver's floor.
//
// 10.5.0 and 10.5.1 are inside the supported range and have only CHANGE COLUMN, which needs the
// column's full definition and so would make a rename an operation compiled against the database.
const (
	renameColumnMajor = 10
	renameColumnMinor = 5
	renameColumnPatch = 2
)

// errUnsafeSystemFunction is ER_BINLOG_UNSAFE_SYSTEM_FUNCTION, refused while a binary log is written.
const errUnsafeSystemFunction = 1674

// binaryLogQuery reads whether this server writes a binary log, which one refusal depends on.
const binaryLogQuery = `SELECT @@global.log_bin`

// serverFacts is everything a verdict is resolved against, detected once when the session opens.
type serverFacts struct {
	// version is the variant and release the server reported, which most verdicts are keyed to.
	version serverVersion
	// logBin is whether the server writes a binary log, which decides one support-axis refusal.
	logBin bool
	// capture is whether the server permits a binlog reader, and what it lacks where it does not.
	capture capability.Capture
}

// readBinaryLogging asks whether this server writes a binary log, refusing an answer it cannot read.
func readBinaryLogging(ctx context.Context, c *mysqlConn) (bool, error) {
	var reported string

	if err := c.runner().QueryRowContext(ctx, binaryLogQuery).Scan(&reported); err != nil {
		return false, wrapConnect("reading log_bin", err)
	}

	return strings.TrimSpace(reported) == variableOn, nil
}

// facts renders what this session is, for a capability set that reports itself.
func (f serverFacts) facts() capability.Facts {
	return capability.Facts{
		Driver:           Name,
		Server:           f.version.String(),
		TransactionalDDL: transactionalDDL,
		Capture:          f.capture,
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
	_, err := compile(op)

	if err != nil && !errors.Is(err, driver.ErrNeedsSession) {
		return capability.Unsupported(describeRefusal(err))
	}

	if problem := f.checkServer(op); problem != "" {
		return capability.Unsupported(problem)
	}

	if errors.Is(err, driver.ErrNeedsSession) {
		return capability.Emulated(redefineStrategy, f.lockImpact(op))
	}

	return f.dependOnTable(op, capability.Native(f.lockImpact(op)))
}

// checkServer reports what this server will not do, by its release and by how it is configured.
func (f serverFacts) checkServer(op ir.Operation) string {
	if problem := f.version.checkVersion(op); problem != "" {
		return problem
	}

	return f.checkReplicationSafety(op)
}

// checkReplicationSafety reports a column MySQL refuses to add while it writes a binary log.
func (f serverFacts) checkReplicationSafety(op ir.Operation) string {
	added, ok := op.(ir.AddColumn)
	if !ok || f.version.variant == MariaDB || !f.logBin ||
		!perRowDefault(added.Column.Default) {
		return ""
	}

	return fmt.Sprintf("column %q is added with a %s default, which MySQL writes into every existing "+
		"row and therefore refuses outright with error %d while it writes a binary log: the value "+
		"would differ on a replica. This server has log_bin on. A new table may declare the same "+
		"default, and so may a later alter_column, so adding the column without one and setting it "+
		"afterwards is two operations that do work -- or generate the value in the application",
		added.Column.Name, ir.FuncUUID, errUnsafeSystemFunction)
}

// checkVersion reports what this server cannot do, which the version-blind compiler cannot know.
func (v serverVersion) checkVersion(op ir.Operation) string {
	switch o := op.(type) {
	case ir.CreateTable:
		return v.checkColumnDefaults(o)
	case ir.AddColumn:
		return v.checkAddedDefault(o)
	case ir.AddCheck:
		return v.checkEnforcedCheck()
	case ir.DropCheck:
		return v.checkDropConstraint()
	case ir.RenameColumn:
		return v.checkRenameColumn()
	default:
		return ""
	}
}

// checkColumnDefaults reports a table whose columns need a default syntax this server does not have.
func (v serverVersion) checkColumnDefaults(op ir.CreateTable) string {
	for _, column := range op.Columns {
		if !isExpressionDefault(column) {
			continue
		}

		if problem := v.checkExpressionDefault(column.Name); problem != "" {
			return problem
		}
	}

	return ""
}

// checkAddedDefault reports a column added with a default syntax this server does not have.
func (v serverVersion) checkAddedDefault(op ir.AddColumn) string {
	if !isExpressionDefault(op.Column) {
		return ""
	}

	return v.checkExpressionDefault(op.Column.Name)
}

// checkEnforcedCheck reports a server that would accept a check constraint and not enforce it.
func (v serverVersion) checkEnforcedCheck() string {
	if v.AtLeastPatch(MySQL, enforcedCheckMajor, enforcedCheckMinor, enforcedCheckPatch) ||
		v.variant == MariaDB {
		return ""
	}

	return fmt.Sprintf("MySQL began enforcing check constraints in %d.%d.%d and this server is %s: "+
		"below that release the syntax is accepted, the constraint does not exist, and the migration "+
		"reports success. Refusing is the only answer that is not a lie", enforcedCheckMajor,
		enforcedCheckMinor, enforcedCheckPatch, v)
}

// checkDropConstraint reports a server with no spelling of a check drop that works on both variants.
func (v serverVersion) checkDropConstraint() string {
	if v.AtLeastPatch(MySQL, dropConstraintMajor, dropConstraintMinor, dropConstraintPatch) ||
		v.variant == MariaDB {
		return ""
	}

	return fmt.Sprintf("a check is dropped by ALTER TABLE ... DROP CONSTRAINT, which MySQL gained in "+
		"%d.%d.%d and this server is %s: MariaDB has no DROP CHECK at all, so that spelling is not "+
		"portable and Compile cannot choose by variant", dropConstraintMajor, dropConstraintMinor,
		dropConstraintPatch, v)
}

// checkRenameColumn reports a MariaDB with only CHANGE COLUMN, which needs the column's definition.
func (v serverVersion) checkRenameColumn() string {
	if v.variant != MariaDB ||
		v.AtLeastPatch(MariaDB, renameColumnMajor, renameColumnMinor, renameColumnPatch) {
		return ""
	}

	return fmt.Sprintf("MariaDB gained RENAME COLUMN in %d.%d.%d and this server is %s: below that "+
		"release a rename is CHANGE COLUMN, which restates the whole definition and would make "+
		"renaming a column an operation compiled against the database", renameColumnMajor,
		renameColumnMinor, renameColumnPatch, v)
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
