package sqlite

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// transactionalDDL is true for SQLite: a schema change is a write to the same file, under the same
// transaction as the data, and M8-T6's round trip is what proves it against a real database.
const transactionalDDL = true

// The release that gained ALTER TABLE ... DROP COLUMN, which the compiler emits and cannot check.
const (
	dropColumnMajor = 3
	dropColumnMinor = 35
)

// facts renders what this session is, for a capability set that reports itself.
func facts(version serverVersion) capability.Facts {
	return capability.Facts{
		Driver:           Name,
		Server:           version.String(),
		TransactionalDDL: transactionalDDL,
	}
}

// rules declares every operation against one detected library, so no verdict is version-blind.
func rules(version serverVersion) map[ir.Kind]capability.Rule {
	declared := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		declared[kind] = version.verdict
	}

	return declared
}

// verdict answers both axes for one operation, on the library this declaration was built against.
func (v serverVersion) verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	if problem := v.checkVersion(op); problem != "" {
		return capability.Unsupported(problem)
	}

	return capability.Native(lockImpact(op))
}

// checkVersion reports what this library cannot do, which the version-blind compiler cannot know.
func (v serverVersion) checkVersion(op ir.Operation) string {
	switch op.(type) {
	case ir.DropColumn:
		return v.checkDropColumn()
	default:
		return ""
	}
}

// checkDropColumn reports a library older than the release whose statement the compiler emits.
func (v serverVersion) checkDropColumn() string {
	if v.AtLeast(dropColumnMajor, dropColumnMinor) {
		return ""
	}

	return fmt.Sprintf("SQLite gained ALTER TABLE ... DROP COLUMN in %d.%d and this library is %s, "+
		"so the statement this driver compiles would be a syntax error. Below that release a column "+
		"can only go by %s, which M8-T4 implements",
		dropColumnMajor, dropColumnMinor, v, rebuildStrategy)
}

// describeRefusal renders a compiler failure as a reason, treating anything unexpected as one.
func describeRefusal(err error) string {
	var refusal *driver.UnsupportedError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}

	return fmt.Sprintf("%s cannot compile this operation: %v", Name, err)
}
