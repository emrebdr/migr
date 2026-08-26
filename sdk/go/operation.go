package migr

import (
	"errors"
	"fmt"
)

// Kind names one operation in the v1 IR vocabulary, spelled as docs/ir.md spells it.
type Kind string

// KindRaw is the one operation this SDK ships today; M6-T8 brings the other nineteen.
const KindRaw Kind = "raw"

// Operation is one schema change a migration emits, which the engine compiles for its driver.
type Operation interface {
	// Kind names the operation, which is the one member every IR document carries.
	Kind() Kind
	// check reports an operation the engine would refuse, before it is sent.
	check() error
}

// Raw is SQL addressed to one engine by name, and the escape hatch the IR cannot express.
type Raw struct {
	// Op is the discriminator, and is set for you when the operation is emitted.
	Op Kind `json:"op"`
	// Driver is the engine this SQL is written for, which no other engine will run.
	Driver string `json:"driver"`
	// SQL is the statement itself, in that engine's own dialect.
	SQL string `json:"sql"`
}

// Kind names this operation, which is raw.
func (r Raw) Kind() Kind {
	return KindRaw
}

// check reports a raw operation naming no engine or carrying no SQL.
func (r Raw) check() error {
	if r.Driver == "" {
		return fmt.Errorf("%w: raw SQL names the engine it is written for", ErrInvalidOperation)
	}

	if r.SQL == "" {
		return fmt.Errorf("%w: raw carries no SQL", ErrInvalidOperation)
	}

	return nil
}

// ErrInvalidOperation marks an operation the engine would refuse as an IR document.
var ErrInvalidOperation = errors.New("this is not a valid operation")

// stamped returns the operation with its discriminator set, since an author never writes it.
func stamped(op Operation) (any, error) {
	if err := op.check(); err != nil {
		return nil, err
	}

	raw, ok := op.(Raw)
	if !ok {
		return nil, fmt.Errorf("%w: %T is not an operation this SDK version emits",
			ErrInvalidOperation, op)
	}

	raw.Op = raw.Kind()

	return raw, nil
}
