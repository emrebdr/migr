package migr

import (
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/sdk/go/internal/wire"
)

// ErrInvalidPhase marks a migration declaring a phase that names no change, no step, or itself.
var ErrInvalidPhase = errors.New("a migration declares a phase no deploy could carry out")

// Phase is one step of a change carried out across several deploys, as the migration carrying it declares it.
type Phase struct {
	// Change names the change this step belongs to, unique to it across the project.
	Change string
	// Name is the step, unique within its change.
	Name string
	// After is the step of the same change that must be applied under an earlier release, empty for none.
	After string
}

// Phased is a migration that is one step of a change across deploys, which migr runs a release apart.
type Phased interface {
	// Phase returns the step this migration is, as a fixed declaration.
	Phase() Phase
}

// phaseOf returns the step a migration declares itself, and whether it declares one.
func phaseOf(m Migration) (Phase, bool) {
	phased, ok := m.(Phased)
	if !ok {
		return Phase{}, false
	}

	return phased.Phase(), true
}

// checkPhase refuses a declared step that names no change or no step, or waits on itself.
func checkPhase(m Migration, file string) error {
	p, declares := phaseOf(m)
	if !declares {
		return nil
	}

	switch {
	case strings.TrimSpace(p.Change) == "":
		return fmt.Errorf("%w: %s declares a phase of no change", ErrInvalidPhase, file)
	case strings.TrimSpace(p.Name) == "":
		return fmt.Errorf("%w: %s declares a phase of %q that names no step", ErrInvalidPhase, file,
			p.Change)
	case p.After == p.Name:
		return fmt.Errorf("%w: %s declares the phase %q of %q waiting on itself", ErrInvalidPhase, file,
			p.Name, p.Change)
	}

	return nil
}

// wirePhase renders the step a migration declares itself as the wire carries it, and nothing where it declares none.
func wirePhase(m Migration) *wire.Phase {
	p, declares := phaseOf(m)
	if !declares {
		return nil
	}

	return &wire.Phase{Change: p.Change, Name: p.Name, After: p.After}
}
