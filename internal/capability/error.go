package capability

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
)

// ErrNoDriver reports a capability set built without naming the driver it describes.
var ErrNoDriver = errors.New("driver name is required")

// ErrIncomplete marks a capability set that does not answer for every operation, for errors.Is.
var ErrIncomplete = errors.New("capability set is incomplete")

// IncompleteError reports a driver that did not answer for every operation, and only those.
type IncompleteError struct {
	// Driver is the driver whose set was rejected.
	Driver string
	// Missing lists the operations the set declares no rule for, in IR declaration order.
	Missing []ir.Kind
	// Unknown lists rules declared for something that is not an operation, sorted.
	Unknown []ir.Kind
}

// Error names the driver and everything wrong with what it declared.
func (e *IncompleteError) Error() string {
	return fmt.Sprintf("driver %q %s", e.Driver, e.describe())
}

// describe reports the operations left undeclared and any declared for a name that is not one.
func (e *IncompleteError) describe() string {
	faults := make([]string, 0, 2)

	if len(e.Missing) > 0 {
		faults = append(faults, "declares no capability for "+joinKinds(e.Missing))
	}

	if len(e.Unknown) > 0 {
		faults = append(faults, "declares a capability for "+joinKinds(e.Unknown)+
			", which name no operation")
	}

	return strings.Join(faults, "; and ")
}

// Unwrap reports the sentinel so errors.Is(err, ErrIncomplete) succeeds.
func (e *IncompleteError) Unwrap() error {
	return ErrIncomplete
}

// joinKinds renders an operation set for an error message.
func joinKinds(kinds []ir.Kind) string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = k.String()
	}

	return strings.Join(names, ", ")
}
