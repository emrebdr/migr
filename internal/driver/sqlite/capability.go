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

// facts renders what this session is, for a capability set that reports itself.
func facts(version serverVersion) capability.Facts {
	return capability.Facts{
		Driver:           Name,
		Server:           version.String(),
		TransactionalDDL: transactionalDDL,
	}
}

// rules declares every operation from what the compiler makes of it, so the two cannot disagree.
func rules() map[ir.Kind]capability.Rule {
	declared := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		declared[kind] = verdict
	}

	return declared
}

// verdict answers both axes for one operation, which until M8-T2 is one answer for all of them.
func verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	return capability.Native(capability.LockUnknown)
}

// describeRefusal renders a compiler failure as a reason, treating anything unexpected as one.
func describeRefusal(err error) string {
	var refusal *driver.UnsupportedError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}

	return fmt.Sprintf("%s cannot compile this operation: %v", Name, err)
}
