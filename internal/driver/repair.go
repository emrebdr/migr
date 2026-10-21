package driver

import (
	"context"

	"github.com/emrebdr/migr/internal/ir"
)

// RepairTime is when a repair is asked for: straight after a statement outside any transaction failed, or before a resume reruns it.
type RepairTime string

// The two moments the engine asks a session to clean up after a statement no transaction rolled back.
const (
	// AfterFailure follows a failed statement in the run that sent it, which may then try it again.
	AfterFailure RepairTime = "after_failure"
	// BeforeResume precedes a resume's rerun of the statement a stopped run, a killed one included, last sent.
	BeforeResume RepairTime = "before_resume"
)

// Repaired is what a repair did about what one statement left behind.
type Repaired struct {
	// Action says what the repair ran, and is empty where the statement left nothing to repair.
	Action string
	// Rerunnable reports that sending the statement again is now safe.
	Rerunnable bool
	// Retry reports that what the statement failed on was the leftover itself, so it is worth sending again at once.
	Retry bool
	// Done reports that what the statement does is already in place, so a resume skips it.
	Done bool
}

// Repairer is a session that cleans up what a statement outside any transaction left behind, which no rollback can.
type Repairer interface {
	// Repair inspects and cleans up what stmt, compiled from op, left behind, given what it failed with, nil before a resume.
	Repair(ctx context.Context, op ir.Operation, stmt Statement, when RepairTime, cause error) (Repaired, error)
}

// InvalidIndex is an index the server keeps up to date on every write and lets no query use.
type InvalidIndex struct {
	// Table is the table it is on.
	Table string
	// Index is its name.
	Index string
	// Definition is the server's own rendering of it.
	Definition string
}

// InvalidIndexReader is a session that can list the unusable indexes a failed concurrent build left.
type InvalidIndexReader interface {
	// InvalidIndexes lists every unusable index in this session's namespace, none where there are none.
	InvalidIndexes(ctx context.Context) ([]InvalidIndex, error)
}
