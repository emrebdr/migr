package history

import (
	"slices"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// File is one discovered migration together with what its source hashes to now.
type File struct {
	// migration is the file discovery found, whose Filename is what a message renders.
	migration migration.Migration
	// source is the digest of the bytes it currently holds, the half that decides drift.
	source checksum.Digest
}

// NewFile pairs a discovered migration with the digest of the source bytes it currently holds.
func NewFile(m migration.Migration, source []byte) File {
	return File{migration: m, source: checksum.OfSource(source)}
}

// found reports whether a file was discovered for this version at all.
func (f File) found() bool {
	return f.migration.Version != ""
}

// Status is what one version reconciled to, and everything that answer was derived from.
type Status struct {
	// Version is the migration's UTC timestamp, and the key the file and the runs were joined on.
	Version string
	// Migration is the file this version was found in, zero when no file carries the version.
	Migration migration.Migration
	// Source is what that file hashes to now, zero when no file carries the version.
	Source checksum.Digest
	// Recorded is the run that left the migration in this state, zero when none did.
	Recorded Record
	// State is what this version reconciled to, and it is exactly one of the six.
	State State
}

// Report is what a whole migrations directory reconciled to against one database's history.
type Report struct {
	// statuses is one entry per version that resolved to a state, in the order they are reported.
	statuses []Status
	// present distinguishes a report over a real history table from one over a database with none.
	present bool
}

// Resolved returns every version's state, and whether this database has a history table at all.
func (r Report) Resolved() ([]Status, bool) {
	return slices.Clone(r.statuses), r.present
}

// Reconcile folds a history log against the migrations on disk, one state per version.
func Reconcile(files []File, log Log) Report {
	if !log.Present() {
		return Report{}
	}

	ordered := log.Runs()
	runs := group(ordered)

	return Report{
		statuses: awaited(append(described(files, runs), undescribed(ordered, files, runs)...), log),
		present:  true,
	}
}

// awaited names a dirty forward run whose shadow table is held in sync for what it is: waiting for a cutover.
func awaited(statuses []Status, log Log) []Status {
	for i, st := range statuses {
		if st.State == StateDirty && st.Recorded.Direction == ir.Up && log.waiting(st.Version) {
			statuses[i].State = StateWaiting
		}
	}

	return statuses
}

// group collects each version's runs, and is the only map here, built to be looked up and never walked.
func group(ordered []Record) map[string][]Record {
	runs := make(map[string][]Record, len(ordered))
	for _, rec := range ordered {
		runs[rec.Version] = append(runs[rec.Version], rec)
	}

	return runs
}

// described resolves every discovered migration, keeping the order the caller gave them in.
func described(files []File, runs map[string][]Record) []Status {
	statuses := make([]Status, 0, len(files))
	for _, file := range files {
		version := file.migration.Version
		statuses = append(statuses, status(version, file, fold(runs[version])))
	}

	return statuses
}

// undescribed resolves the versions the database still carries and no file describes.
func undescribed(ordered []Record, files []File, runs map[string][]Record) []Status {
	var statuses []Status

	for _, version := range unfiled(ordered, files) {
		folded := fold(runs[version])
		if !folded.outstanding() {
			continue
		}

		statuses = append(statuses, status(version, File{}, folded))
	}

	return statuses
}

// unfiled lists the recorded versions no file describes, walking the runs rather than iterating a map.
func unfiled(ordered []Record, files []File) []string {
	claimed := make(map[string]bool, len(files))
	for _, file := range files {
		claimed[file.migration.Version] = true
	}

	var versions []string

	for _, rec := range ordered {
		if claimed[rec.Version] {
			continue
		}

		claimed[rec.Version] = true

		versions = append(versions, rec.Version)
	}

	slices.Sort(versions)

	return versions
}

// status renders one version's answer from its file and from what its runs folded to.
func status(version string, file File, folded folded) Status {
	return Status{
		Version:   version,
		Migration: file.migration,
		Source:    file.source,
		Recorded:  folded.anchor,
		State:     stateOf(folded, file),
	}
}

// stateOf names what a fold and a file add up to, and it is exactly one of the five.
func stateOf(folded folded, file File) State {
	switch {
	case folded.dirty:
		return StateDirty
	case !folded.carried:
		return StatePending
	case !file.found():
		return StateMissing
	case file.source != folded.anchor.Checksum.Source:
		return StateDrifted
	default:
		return StateApplied
	}
}

// folded is what one version's runs add up to: whether the database carries it, and which run said so.
type folded struct {
	// carried reports whether these runs leave this migration's changes in the database.
	carried bool
	// dirty reports whether a run left the schema between states and nothing later resolved it.
	dirty bool
	// anchor is the run that put the migration in this state, zero when none did.
	anchor Record
}

// outstanding reports whether the database is left holding something only a file could explain.
func (f folded) outstanding() bool {
	return f.carried || f.dirty
}

// fold replays one version's runs, since what a run left behind depends on what the runs before it did.
func fold(runs []Record) folded {
	var f folded
	for _, rec := range runs {
		f = f.after(rec)
	}

	return f
}

// after returns what this fold becomes once one more run has happened.
func (f folded) after(rec Record) folded {
	switch rec.Direction {
	case ir.Up:
		return f.afterUp(rec)
	case ir.Down:
		return f.afterDown(rec)
	}

	return f
}

// afterUp applies a forward run, which either carries the migration, leaves it dirty, or changes nothing.
func (f folded) afterUp(rec Record) folded {
	switch rec.Outcome {
	case Applied, Resolved:
		return folded{carried: true, anchor: rec}
	case Dirty:
		return stoppedPartway(rec)
	case Failed:
		return f
	}

	return f
}

// afterDown applies a rollback, which either gives the changes back, leaves it dirty, or changes nothing.
func (f folded) afterDown(rec Record) folded {
	switch rec.Outcome {
	case Applied, Resolved:
		return folded{anchor: rec}
	case Dirty:
		return stoppedPartway(rec)
	case Failed:
		return f
	}

	return f
}

// stoppedPartway marks a run that left the schema between states, which no other answer survives.
func stoppedPartway(rec Record) folded {
	return folded{dirty: true, anchor: rec}
}
