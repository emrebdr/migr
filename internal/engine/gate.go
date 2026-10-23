package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/remedy"
)

// ErrNoRelease refuses a run reaching a step of a change across deploys without naming the deploy it is.
var ErrNoRelease = errors.New("this run reaches a step of a change carried out across deploys, " +
	"and names no release")

// swapPhase is the step after which the replacing column is the one an app reads, as ReplaceColumn names it.
const swapPhase = "swap"

// splitSwap is what a swap is on an engine whose DDL commits as it goes, which a gate waiting on the app owes.
const splitSwap = "On this engine every DDL statement commits on its own, so the swap is not one " +
	"moment: between its renames the column an app reads does not exist, and the app serving then " +
	"must not read it by name."

// Held is a step that waits for a later release, which stops a run at it without failing the run.
type Held struct {
	// Migration is the step that waits.
	Migration migration.Migration
	// Phase is what it declares itself a step of.
	Phase migration.Phase
	// Prior is the migration carrying the step it waits on.
	Prior migration.Migration
	// PriorRelease is the release that step was applied under, empty where it has not been applied.
	PriorRelease string
	// Reason says why it waits and what lets it run.
	Reason string
}

// Gated reports whether one waiting step must wait, given the history before this run and its release.
func Gated(gate migration.Gate, statuses []history.Status, releases []history.Release, current string,
	transactional bool) (Held, bool) {
	held := Held{Phase: gate.Phase, Prior: gate.Prior}

	prior, found := reconciled(statuses, gate.Prior.Version)
	if !found || prior.State != history.StateApplied {
		held.Reason = fmt.Sprintf("%s, its %q step, is not applied yet, and this one runs only under a "+
			"release after the one that applies it, once the app that release deploys is serving",
			gate.Prior.Filename(), gate.Phase.After)

		return withSwap(held, transactional), true
	}

	name, named := history.ReleaseOf(releases, prior.Recorded.Batch)
	if !named || name != current {
		return Held{}, false
	}

	held.PriorRelease = name
	held.Reason = fmt.Sprintf("%s, its %q step, belongs to release %q, which is this one; this step "+
		"runs under a later release, once the app %q deploys is serving everywhere",
		gate.Prior.Filename(), gate.Phase.After, name, name)

	return withSwap(held, transactional), true
}

// withSwap adds what a swap is where DDL commits as it goes, to a hold at a swap or at the step after one.
func withSwap(held Held, transactional bool) Held {
	if transactional || (held.Phase.Name != swapPhase && held.Phase.After != swapPhase) {
		return held
	}

	held.Reason += ". " + splitSwap

	return held
}

// reconciled finds one version's reconciled state.
func reconciled(statuses []history.Status, version string) (history.Status, bool) {
	for _, s := range statuses {
		if s.Version == version {
			return s, true
		}
	}

	return history.Status{}, false
}

// A batch with no recorded release is an earlier release with no name. Only a run before releases were
// recorded can leave one beside a step, since a run whose queue holds any step must name its release
// -- so it is never this run's, and treating it as a wait nothing could ever end would strand every
// change begun before migr recorded deploys.

// needsRelease reports whether a queue holds a step of a change across deploys, which only a named release may run.
func (e *executor) needsRelease(queue []queued) error {
	if e.opts.Release != "" {
		return nil
	}

	for _, item := range queue {
		if _, declared := e.opts.Phases[item.migration.Version]; declared && item.resume == nil {
			return remedy.With(fmt.Errorf("%w: %s declares a step of %q, which runs only a deploy after "+
				"the step before it", ErrNoRelease, item.migration.Filename(),
				e.opts.Phases[item.migration.Version].Change),
				"name the deploy this run belongs to: migr up --release <build id>, or MIGR_RELEASE set")
		}
	}

	return nil
}

// releases reads which deploy each batch ran under, where any step in the queue waits on one.
func (e *executor) releases(ctx context.Context, queue []queued) ([]history.Release, error) {
	if !e.waits(queue) {
		return nil, nil
	}

	releaser, records := e.store.(history.Releaser)
	if !records {
		return nil, remedy.With(ErrNoReleases, "this driver's history store records no releases, which "+
			"is a bug in the driver: report it at https://github.com/emrebdr/migr/issues")
	}

	return releaser.Releases(ctx)
}

// ErrNoReleases refuses a waiting step over a store that cannot say which deploy any batch ran under.
var ErrNoReleases = errors.New("a step waits on an earlier release, and this history records no releases")

// waits reports whether any migration in a queue is a step that waits on another.
func (e *executor) waits(queue []queued) bool {
	for _, item := range queue {
		if _, gated := e.opts.Gates[item.migration.Version]; gated {
			return true
		}
	}

	return false
}

// held reports the step a run must stop at, before it asks the step anything.
func (e *executor) held(item queued, statuses []history.Status, releases []history.Release) (*Held, bool) {
	gate, gated := e.opts.Gates[item.migration.Version]
	if !gated || item.resume != nil {
		return nil, false
	}

	found, waits := Gated(gate, statuses, releases, e.opts.Release, e.conn.Capabilities().TransactionalDDL())
	if !waits {
		return nil, false
	}

	found.Migration = item.migration

	return &found, true
}

// recordRelease records this batch's release in the transaction of its first applied row, and once.
func (e *executor) recordRelease(ctx context.Context) error {
	if e.opts.Release == "" || e.released {
		return nil
	}

	releaser, records := e.store.(history.Releaser)
	if !records {
		return nil
	}

	if err := releaser.RecordRelease(ctx, history.Release{Batch: e.batch, Name: e.opts.Release,
		RecordedAt: e.opts.Clock()}); err != nil {
		return err
	}

	e.released = true

	return nil
}
