package engine

import (
	"slices"

	"github.com/emrebdr/migr/internal/history"
)

// scopeKind names how a scope decides where the prefix it selects ends.
type scopeKind string

// The four selectors. The zero kind is scopeAll, so options nobody narrowed act on everything.
const (
	scopeAll   scopeKind = ""
	scopeSteps scopeKind = "steps"
	scopeTo    scopeKind = "to"
	scopeBatch scopeKind = "batch"
)

// Scope narrows what a run acts on, and its zero value is everything that direction can act on.
type Scope struct {
	// kind is which selector this is, and the zero value is the one that narrows nothing.
	kind scopeKind
	// steps is how many migrations to act on, for a scope that counts them.
	steps int32
	// version is the version to leave the database at, for a scope that names one.
	version string
}

// All acts on every migration the direction can, which is what an unnarrowed run already does.
func All() Scope {
	return Scope{kind: scopeAll}
}

// Steps acts on the first n queued: the first n pending going up, the last n applied going down.
func Steps(n int32) Scope {
	return Scope{kind: scopeSteps, steps: n}
}

// To leaves the database at one version, applying through it or undoing everything newer than it.
func To(version string) Scope {
	return Scope{kind: scopeTo, version: version}
}

// LastBatch undoes the most recent batch the database still carries, and is a rollback's alone.
func LastBatch() Scope {
	return Scope{kind: scopeBatch}
}

// check refuses a selector that can act on nothing, before the run takes a lock it could not use.
func (s Scope) check() error {
	switch s.kind {
	case scopeAll, scopeBatch:
		return nil
	case scopeSteps:
		return s.checkSteps()
	case scopeTo:
		return s.checkVersion()
	}

	return unknownScopeError(string(s.kind))
}

// checkUp refuses a selector only a rollback can act on, before an apply takes a lock for nothing.
func (s Scope) checkUp() error {
	if s.kind == scopeBatch {
		return noBatchError()
	}

	return nil
}

// checkSteps refuses a count naming no migration, which is a typo rather than a request for none.
func (s Scope) checkSteps() error {
	if s.steps < 1 {
		return stepsError(s.steps)
	}

	return nil
}

// checkVersion refuses a target naming no version at all, which nothing could be compared against.
func (s Scope) checkVersion() error {
	if s.version == "" {
		return blankVersionError()
	}

	return nil
}

// narrowUp cuts an apply queue to the prefix this scope selects, reporting what it stopped short of.
func (s Scope) narrowUp(queue []queued, statuses []history.Status) ([]queued, []Skipped, error) {
	switch s.kind {
	case scopeAll:
		return queue, nil, nil
	case scopeSteps:
		kept, dropped := cut(queue, min(int(s.steps), len(queue)))

		return kept, dropped, nil
	case scopeTo:
		return s.through(queue, statuses)
	case scopeBatch:
		return nil, nil, noBatchError()
	}

	return nil, nil, unknownScopeError(string(s.kind))
}

// narrowDown cuts a rollback queue, which newestFirst has already put in the order it will run.
func (s Scope) narrowDown(queue []queued) ([]queued, []Skipped, error) {
	switch s.kind {
	case scopeAll:
		return queue, nil, nil
	case scopeSteps:
		kept, dropped := cut(queue, min(int(s.steps), len(queue)))

		return kept, dropped, nil
	case scopeTo:
		return s.newerThan(queue)
	case scopeBatch:
		kept, dropped := cut(queue, len(leading(queue)))

		return kept, dropped, nil
	}

	return nil, nil, unknownScopeError(string(s.kind))
}

// through keeps every queued migration the report orders at or before the version to end at.
func (s Scope) through(queue []queued, statuses []history.Status) ([]queued, []Skipped, error) {
	target := slices.IndexFunc(statuses, func(st history.Status) bool {
		return st.Version == s.version
	})

	if target < 0 {
		return nil, nil, unknownVersionError(s.version, versionsOf(statuses))
	}

	kept, dropped := cut(queue, countIn(queue, versionSet(statuses[:target+1])))

	return kept, dropped, nil
}

// newerThan keeps every migration applied after the version to end at, leaving that one applied.
func (s Scope) newerThan(queue []queued) ([]queued, []Skipped, error) {
	target := slices.IndexFunc(queue, func(item queued) bool {
		return item.migration.Version == s.version
	})

	if target < 0 {
		return nil, nil, notAppliedError(s.version, versionsIn(queue))
	}

	kept, dropped := cut(queue, target)

	return kept, dropped, nil
}

// leading returns the run of migrations at the front of a rollback queue sharing one applied batch.
func leading(queue []queued) []queued {
	if len(queue) == 0 {
		return nil
	}

	batch := queue[0].recorded.Batch

	for i, item := range queue {
		if item.recorded.Batch != batch {
			return queue[:i]
		}
	}

	return queue
}

// cut keeps the first n of a queue and reports the rest as migrations this run stopped short of.
func cut(queue []queued, n int) ([]queued, []Skipped) {
	var dropped []Skipped

	for _, item := range queue[n:] {
		dropped = append(dropped, shortOf(item))
	}

	return queue[:n], dropped
}

// shortOf records a migration a narrowed run never reached, and the state it is still in.
func shortOf(item queued) Skipped {
	return Skipped{
		Version:   item.migration.Version,
		Migration: item.migration,
		State:     item.state,
	}
}

// countIn reports how many of a queue the reached versions cover, which is a prefix of it.
func countIn(queue []queued, reached map[string]struct{}) int {
	kept := 0

	for _, item := range queue {
		if _, within := reached[item.migration.Version]; within {
			kept++
		}
	}

	return kept
}

// versionSet collects the versions a prefix of the report covers, for a membership test.
func versionSet(statuses []history.Status) map[string]struct{} {
	reached := make(map[string]struct{}, len(statuses))

	for _, st := range statuses {
		reached[st.Version] = struct{}{}
	}

	return reached
}

// versionsOf lists every version a run reconciled, so a target naming none can say what it had.
func versionsOf(statuses []history.Status) []string {
	versions := make([]string, 0, len(statuses))

	for _, st := range statuses {
		versions = append(versions, st.Version)
	}

	return versions
}

// versionsIn lists the versions a queue holds, in the order that queue will act on them.
func versionsIn(queue []queued) []string {
	versions := make([]string, 0, len(queue))

	for _, item := range queue {
		versions = append(versions, item.migration.Version)
	}

	return versions
}
