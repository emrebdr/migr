package migrtest

import (
	"strings"
	"testing"
)

// The documents migr prints under --json, read as types rather than scraped as tables.
//
// Every shape here is one internal/cli already specifies and tests -- runSummary in summary.go,
// validation in validate.go, and the one-line failure ADR 0029 puts on stderr. Reading the table
// instead would make this helper depend on column widths, and --json switches both streams so a run
// that printed a summary and then failed leaves exactly one parseable document on each.

// Run is what one migr up or migr down did.
type Run struct {
	// Database is the name migr.yaml holds the target under.
	Database string `json:"database"`
	// Direction is which way the invocation ran.
	Direction string `json:"direction"`
	// Batch is what every run in this invocation was recorded under, and zero when nothing ran.
	Batch int64 `json:"batch"`
	// Runs is one entry per migration performed, in the order they were performed.
	Runs []Applied `json:"runs"`
	// Skipped is one entry per migration this invocation did not perform, and the state it is in.
	Skipped []Skipped `json:"skipped"`
}

// Versions names each migration this invocation performed, in the order it performed them.
func (r Run) Versions() []string {
	versions := make([]string, 0, len(r.Runs))

	for _, applied := range r.Runs {
		versions = append(versions, applied.Version)
	}

	return versions
}

// Applied is one migration an invocation performed, and what it cost.
type Applied struct {
	// Ordinal is its position within the batch, counting from one.
	Ordinal int32 `json:"ordinal"`
	// Version is the migration's UTC timestamp, which --to and a history row name it by.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Statements is how many statements it executed.
	Statements int32 `json:"statements"`
}

// Skipped is one migration an invocation did not act on, and the state it was left in.
type Skipped struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in, and empty when only the history names it.
	File string `json:"file"`
	// State is what reconciliation resolved it to.
	State string `json:"state"`
}

// Report is what migr validate found, which is its product rather than its failure.
type Report struct {
	// Driver is the engine every migration was compiled for.
	Driver string `json:"driver"`
	// Connected reports whether the checks needing the target database were made at all.
	Connected bool `json:"connected"`
	// Language is what this project's migrations are written in.
	Language string `json:"language"`
	// Checked is how many migrations were looked at.
	Checked int `json:"migrations"`
	// Problems is everything found wrong, which exits 2.
	Problems []Finding `json:"problems"`
	// Warnings is everything a rule set to warn found, which is judged and fails nothing.
	Warnings []Finding `json:"warnings"`
	// Unchecked is every half nothing could judge, which is not the same as one that passed.
	Unchecked []Unchecked `json:"unchecked"`
}

// NoProblems holds a report to having found nothing wrong, naming everything it did find.
func (r Report) NoProblems(t *testing.T) {
	t.Helper()

	if len(r.Problems) == 0 {
		return
	}

	for _, problem := range r.Problems {
		t.Errorf("%s %s: %s (%s)", problem.File, problem.Direction, problem.Detail, problem.Check)
	}
}

// Rules names the lint rules that warned, which fail nothing and are counted on their own.
func (r Report) Rules() []string {
	fired := make([]string, 0, len(r.Warnings))

	for _, warning := range r.Warnings {
		if warning.Rule != "" {
			fired = append(fired, warning.Rule)
		}
	}

	return fired
}

// Finding is one problem or warning a validation run reported.
type Finding struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Check is what found the problem.
	Check string `json:"check"`
	// Rule is the lint rule that found it, and empty for a check that is not a rule.
	Rule string `json:"rule"`
	// Direction is which of the migration's two halves the problem is in.
	Direction string `json:"direction"`
	// Operation is its place in that half, counting from one, and zero when it is not about one.
	Operation int32 `json:"operation"`
	// Kind is the operation at fault, and empty when the problem is not about one.
	Kind string `json:"kind"`
	// Detail states what is wrong, in terms its author can act on.
	Detail string `json:"detail"`
}

// Unchecked is one half of one migration a run could not judge, and what stopped it.
type Unchecked struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in.
	File string `json:"file"`
	// Direction is which half went unchecked.
	Direction string `json:"direction"`
	// Operation is the one operation this names, counting from one, and zero for a whole half.
	Operation int32 `json:"operation"`
	// Kind is that operation, and empty where a whole half went unchecked.
	Kind string `json:"kind"`
	// Reason states what stopped the check.
	Reason string `json:"reason"`
}

// Status is what migr status reported, which takes no migration lock and promises no action.
type Status struct {
	// HistoryTable reports whether this database has a migration history table at all.
	HistoryTable bool `json:"history_table"`
	// Files is how many migrations the directory holds.
	Files int `json:"files"`
	// Order is how the migrations were sequenced, which a report names rather than resolves.
	Order string `json:"order"`
	// Entries is one row per migration, in the order they were printed.
	Entries []Entry `json:"statuses"`
	// Counts is how many migrations are in each of the five states.
	Counts Counts `json:"counts"`
}

// Entry is one migration migr status printed a row for.
type Entry struct {
	// Version is the migration's UTC timestamp.
	Version string `json:"version"`
	// File is the filename it was found in, and empty when only the history names it.
	File string `json:"file"`
	// State is what reconciliation resolved it to.
	State string `json:"state"`
}

// Counts is how many migrations are in each state, which is what an assertion usually reads.
type Counts struct {
	// Applied is how many have run and are recorded as applied.
	Applied int `json:"applied"`
	// Dirty is how many a stopped run left between states.
	Dirty int `json:"dirty"`
	// Drifted is how many no longer hash to what was recorded.
	Drifted int `json:"drifted"`
	// Missing is how many the history names and the directory does not hold.
	Missing int `json:"missing"`
	// Pending is how many have not run.
	Pending int `json:"pending"`
}

// Failure is what migr reported when a command failed, which --json makes one document on stderr.
type Failure struct {
	// Code is the exit code, which CLAUDE.md §5 gives five meanings.
	Code int `json:"code"`
	// Error is what went wrong, as the one line a failing command writes.
	Error string `json:"error"`
}

// Says reports whether the failure mentions a phrase, for a test asserting what it was told.
func (f Failure) Says(phrase string) bool {
	return strings.Contains(f.Error, phrase)
}
