package migration

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrPhase marks phase declarations that do not describe a change a deploy can carry out in order.
var ErrPhase = errors.New("these migrations declare phases that cannot be carried out in order")

// Phase is one step of a change spanning several deploys, as the migration carrying it declares it.
type Phase struct {
	// Change names the change this step belongs to, such as the table and column it replaces.
	Change string
	// Name is the step itself, unique within its change.
	Name string
	// After is the step of the same change that must be applied under an earlier release, empty for none.
	After string
}

// Validate reports a declaration naming no change or no step, or a step that waits on itself.
func (p Phase) Validate() error {
	switch {
	case strings.TrimSpace(p.Change) == "":
		return errors.New("its phase names no change, and a step belongs to one")
	case strings.TrimSpace(p.Name) == "":
		return fmt.Errorf("its phase of %q names no step", p.Change)
	case p.After == p.Name:
		return fmt.Errorf("its phase %q of %q waits on itself", p.Name, p.Change)
	}

	return nil
}

// Phases is what each migration declares about the change it is a step of, keyed by version.
type Phases map[string]Phase

// Gate is one step that waits, and the migration that must be applied under an earlier release first.
type Gate struct {
	// Phase is the step that waits, as its migration declared it.
	Phase Phase
	// Prior is the migration carrying the step it waits on.
	Prior Migration
}

// Gates resolves every waiting step to the migration it waits on, over migrations in the order a run uses.
func Gates(ordered []Migration, phases Phases) (map[string]Gate, error) {
	at := make(map[string]int, len(ordered))
	for i, m := range ordered {
		at[m.Version] = i
	}

	steps, problems := indexSteps(ordered, phases)
	gates := make(map[string]Gate, len(phases))

	for _, m := range ordered {
		phase, declared := phases[m.Version]
		if !declared || phase.After == "" || phase.Validate() != nil {
			continue
		}

		prior, found := steps[stepKey{phase.Change, phase.After}]

		switch {
		case !found:
			problems = append(problems, fmt.Errorf("%s: its phase %q of %q waits on %q, and no migration "+
				"here declares that step", m.Filename(), phase.Name, phase.Change, phase.After))
		case at[prior.Version] > at[m.Version]:
			problems = append(problems, fmt.Errorf("%s: its phase %q of %q waits on %q, which %s declares "+
				"and which runs after it", m.Filename(), phase.Name, phase.Change, phase.After,
				prior.Filename()))
		default:
			gates[m.Version] = Gate{Phase: phase, Prior: prior}
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w:\n%w", ErrPhase, errors.Join(problems...))
	}

	return gates, nil
}

// stepKey is one step of one change, which at most one migration may declare.
type stepKey struct {
	// change is the change the step belongs to.
	change string
	// name is the step.
	name string
}

// indexSteps finds the migration carrying each step, reporting a declaration that is malformed or repeated.
func indexSteps(ordered []Migration, phases Phases) (map[stepKey]Migration, []error) {
	steps := make(map[stepKey]Migration, len(phases))

	var problems []error

	for _, m := range ordered {
		phase, declared := phases[m.Version]
		if !declared {
			continue
		}

		if err := phase.Validate(); err != nil {
			problems = append(problems, fmt.Errorf("%s: %w", m.Filename(), err))

			continue
		}

		key := stepKey{phase.Change, phase.Name}
		if other, taken := steps[key]; taken {
			problems = append(problems, fmt.Errorf("%s: its phase %q of %q is also declared by %s, and a "+
				"step is one migration", m.Filename(), phase.Name, phase.Change, other.Filename()))

			continue
		}

		steps[key] = m
	}

	return steps, slices.Clip(problems)
}
