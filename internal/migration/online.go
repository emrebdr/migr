package migration

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// ErrOnline marks a migration declaring a way of running online that migr does not know.
var ErrOnline = errors.New("these migrations declare an online strategy migr does not know")

// Strategy is how a migration declares it runs against a live table, rather than as a plain ALTER.
type Strategy string

// ShadowTable is a migration changing one table through a copy kept current from the replication log.
const ShadowTable Strategy = "shadow_table"

// Strategies lists every strategy a migration may declare, in the order a refusal names them.
func Strategies() []Strategy {
	return []Strategy{ShadowTable}
}

// Valid reports whether s is a strategy migr knows.
func (s Strategy) Valid() bool {
	return slices.Contains(Strategies(), s)
}

// Online is what each migration declares about running online, keyed by version.
type Online map[string]Strategy

// Check refuses every declaration of a strategy migr does not know, naming the migration and the ones it does.
func (o Online) Check(found []Migration) error {
	byVersion := make(map[string]Migration, len(found))
	for _, m := range found {
		byVersion[m.Version] = m
	}

	var problems []error

	for _, version := range slices.Sorted(maps.Keys(o)) {
		if strategy := o[version]; !strategy.Valid() {
			problems = append(problems, fmt.Errorf("%s: it declares the online strategy %q, and the "+
				"one migr knows is %q", byVersion[version].Filename(), strategy, ShadowTable))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%w:\n%w", ErrOnline, errors.Join(problems...))
	}

	return nil
}

// Declarations are what every loaded migration declared about itself, which only a started runtime reports.
type Declarations struct {
	// Dependencies are what each migration says it must follow.
	Dependencies Declared
	// Phases are what each migration says it is a step of.
	Phases Phases
	// Online are how each migration says it runs against a live table.
	Online Online
}

// NewDeclarations returns an empty set of declarations, ready to be filled.
func NewDeclarations() Declarations {
	return Declarations{Dependencies: make(Declared), Phases: make(Phases), Online: make(Online)}
}

// Merge folds another runtime's declarations into these, which the routes keep from colliding.
func (d Declarations) Merge(from Declarations) {
	maps.Copy(d.Dependencies, from.Dependencies)
	maps.Copy(d.Phases, from.Phases)
	maps.Copy(d.Online, from.Online)
}
