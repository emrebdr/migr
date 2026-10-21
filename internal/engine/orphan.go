package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/migration"
)

// A change stream's server-side resource that nothing reads is not litter: PostgreSQL keeps every
// byte of WAL for a replication slot until something advances it, so an abandoned one fills the
// disk (ADR 0094). A resource is orphaned when no copying or syncing shadow-table state owns it.
// It is this project's own -- and migr up releases it under the migration lock -- where its state
// cut over, so only a kill between the swap and the release can have left it, or where its derived
// name is a version this project carries with no state at all, so only a start killed before its
// record can have. Anything else carries a name like migr's and is someone else's to drop.

// capturePrefix is how every derived capture name begins, which is what a keeper lists by.
const capturePrefix = "migr_"

// ErrCaptureRelease marks a run that could not release a change-stream resource it found orphaned.
var ErrCaptureRelease = errors.New("an orphaned change-stream resource could not be released")

// Orphan is a change-stream resource on the server that no live shadow-table migration owns.
type Orphan struct {
	// Resource is what the server reports about it.
	Resource driver.CaptureResource
	// Migration is the migration of this project its name derives from, zero where it names none.
	Migration migration.Migration
	// Ours reports a resource only this project's runs can have left, which migr up releases.
	Ours bool
	// Why says how it came to be left, as a sentence reads it.
	Why string
}

// Orphans returns every resource no copying or syncing state owns, saying which are this project's own.
func Orphans(resources []driver.CaptureResource, states []history.ShadowState,
	migrations []migration.Migration) []Orphan {
	var found []Orphan

	for _, resource := range resources {
		if orphan, left := orphanOf(resource, states, migrations); left {
			found = append(found, orphan)
		}
	}

	return found
}

// orphanOf judges one resource against the states recorded and the migrations this project carries.
func orphanOf(resource driver.CaptureResource, states []history.ShadowState,
	migrations []migration.Migration) (Orphan, bool) {
	for _, state := range states {
		if state.Capture != resource.Name {
			continue
		}

		if state.Stage != history.StageCutOver {
			return Orphan{}, false
		}

		return Orphan{Resource: resource, Migration: migrationOf(migrations, state.Version), Ours: true,
			Why: "its migration cut over, and the run that swapped the copy in stopped before releasing it"}, true
	}

	if m, named := namedFor(resource.Name, migrations); named {
		return Orphan{Resource: resource, Migration: m, Ours: true,
			Why: "it is named for this migration, which records no shadow-table state -- a start " +
				"stopped before writing one"}, true
	}

	return Orphan{Resource: resource, Why: "it is named like migr's, for no migration this project " +
		"carries -- another project's, or a history this database no longer holds"}, true
}

// namedFor returns the migration of this project a derived capture name belongs to, where it belongs to one.
func namedFor(name string, migrations []migration.Migration) (migration.Migration, bool) {
	for _, m := range migrations {
		if strings.HasPrefix(name, capturePrefix+strings.ToLower(m.Version)+"_") {
			return m, true
		}
	}

	return migration.Migration{}, false
}

// migrationOf returns the file a version was found in, and zero where none was.
func migrationOf(migrations []migration.Migration, version string) migration.Migration {
	for _, m := range migrations {
		if m.Version == version {
			return m
		}
	}

	return migration.Migration{}
}

// Carried returns the migrations a reconciled report found files for, which is what a capture name is matched against.
func Carried(report history.Report) []migration.Migration {
	statuses, _ := report.Resolved()

	var found []migration.Migration

	for _, st := range statuses {
		if st.Migration != (migration.Migration{}) {
			found = append(found, st.Migration)
		}
	}

	return found
}

// ReleasedCapture is an orphaned resource a run released, or found in use and left.
type ReleasedCapture struct {
	// Orphan is the resource and why it was orphaned.
	Orphan Orphan
	// InUse reports a resource some connection was reading through, which was left for the next run.
	InUse bool
}

// releaseOrphans releases every orphaned resource this project left, before anything else runs.
func (e *executor) releaseOrphans(ctx context.Context, report history.Report,
	states []history.ShadowState) ([]ReleasedCapture, error) {
	keeper, keeps := e.conn.(driver.CaptureKeeper)
	if !keeps {
		return nil, nil
	}

	resources, err := keeper.CaptureResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading which change-stream resources the server holds: %w", err)
	}

	var released []ReleasedCapture

	for _, orphan := range Orphans(resources, states, Carried(report)) {
		if !orphan.Ours {
			continue
		}

		done := ReleasedCapture{Orphan: orphan, InUse: orphan.Resource.Active}
		if !done.InUse {
			if err := keeper.ReleaseCapture(ctx, orphan.Resource.Name); err != nil {
				return released, fmt.Errorf("%w: %s: %w", ErrCaptureRelease, orphan.Resource.Name, err)
			}
		}

		if e.opts.Released != nil {
			e.opts.Released(done)
		}

		released = append(released, done)
	}

	return released, nil
}
