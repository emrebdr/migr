package postgres

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// lockPending is the honest second axis until impacts are measured per server version (M3-T3).
const lockPending = capability.LockUnknown

// rules declares every operation by asking the compiler, so the two answers cannot disagree.
func rules() map[ir.Kind]capability.Rule {
	declared := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		declared[kind] = verdict
	}

	return declared
}

// verdict answers both axes for one operation: supported when it compiles, refused when it does not.
func verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	return capability.Native(lockPending)
}

// describeRefusal renders a compiler failure as a reason, treating anything unexpected as one.
func describeRefusal(err error) string {
	var refusal *driver.UnsupportedError
	if errors.As(err, &refusal) {
		return refusal.Reason
	}

	return fmt.Sprintf("%s cannot compile this operation: %v", Name, err)
}
