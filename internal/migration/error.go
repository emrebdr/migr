package migration

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// ErrMalformed marks a file in the migrations directory that is not named as a migration.
var ErrMalformed = errors.New("not a migration filename")

// ErrDuplicateVersion marks two migrations claiming one version, for errors.Is.
var ErrDuplicateVersion = errors.New("two migrations claim one version")

// ErrNoDirectory marks a migrations directory that is not there at all, for errors.Is.
var ErrNoDirectory = errors.New("the migrations directory does not exist")

// renameOrMove is what an operator does about a file this package refuses, and it never varies.
const renameOrMove = "rename it, or move it out of the migrations directory"

// NameError reports a file that is not named as a migration, and what about the name is not.
type NameError struct {
	// File is the filename at fault, as it appears in the migrations directory.
	File string
	// Detail states what about the name is wrong, in terms its author can act on.
	Detail string
}

// Error names the file, what is wrong with its name, and the grammar it missed.
func (e *NameError) Error() string {
	return fmt.Sprintf("%s is not a migration: %s. Migrations are named %s, as %s",
		e.File, e.Detail, grammar, example)
}

// Next says to rename the file or move it, the directory holding migrations alone.
func (e *NameError) Next() string {
	return renameOrMove
}

// Unwrap reports the sentinel so errors.Is(err, ErrMalformed) succeeds.
func (e *NameError) Unwrap() error {
	return ErrMalformed
}

// malformed builds the verdict for a filename this package refuses, naming the file first.
func malformed(filename, detail string) error {
	return &NameError{File: filename, Detail: detail}
}

// DuplicateError reports two migrations that claim one version, naming both of them.
type DuplicateError struct {
	// Version is the version both files claim.
	Version string
	// Files are the filenames claiming it, in the order the sorted directory holds them.
	Files []string
}

// Error names both files, the version they share, and the two questions that leaves unanswerable.
func (e *DuplicateError) Error() string {
	return fmt.Sprintf("%s both claim version %s, so nothing can say which one a history row "+
		"records or which one --to names", strings.Join(e.Files, " and "), e.Version)
}

// Next says to give one of them a version of its own.
func (e *DuplicateError) Next() string {
	return "rename the one not yet applied to a version of its own, as migr create would stamp it"
}

// Unwrap reports the sentinel so errors.Is(err, ErrDuplicateVersion) succeeds.
func (e *DuplicateError) Unwrap() error {
	return ErrDuplicateVersion
}

// ErrUnresolved marks a dependency graph that cannot be put in an order, for errors.Is.
var ErrUnresolved = errors.New("the declared dependencies cannot be ordered")

// UnknownDependency reports a declared edge naming a version no discovered migration carries.
type UnknownDependency struct {
	// From is the migration that declared it, or empty when the declaration's key names nobody.
	From string
	// Version is the version it named.
	Version string
}

// Error names who wanted the missing version, or says the declaration itself is the orphan.
func (e UnknownDependency) Error() string {
	if e.From == "" {
		return fmt.Sprintf("a dependency is declared for version %s, which no migration carries",
			e.Version)
	}

	return fmt.Sprintf("%s depends on version %s, which no migration carries", e.From, e.Version)
}

// GraphError reports everything wrong with a set of declared dependencies rather than the first.
type GraphError struct {
	// Unknown are the declared edges naming a version nothing discovered carries.
	Unknown []UnknownDependency
	// Cycles are the dependency loops, each as the path around it, in filenames.
	Cycles [][]string
}

// Error lists every unknown dependency and every cycle, one per line.
func (e *GraphError) Error() string {
	var b strings.Builder

	b.WriteString(ErrUnresolved.Error())
	fmt.Fprintf(&b, " (%s):", pluralProblems(len(e.Unknown)+len(e.Cycles)))

	for _, unknown := range e.Unknown {
		fmt.Fprintf(&b, "\n  %s", unknown.Error())
	}

	for _, cycle := range e.Cycles {
		fmt.Fprintf(&b, "\n  a cycle, each depending on the next: %s", renderCycle(cycle))
	}

	return b.String()
}

// Next says to make each declaration name a migration that exists, and to break each loop.
func (e *GraphError) Next() string {
	return "make each dependency name the version of a migration in this directory, and break each " +
		"cycle by removing one of its dependencies"
}

// Unwrap reports the sentinel so errors.Is(err, ErrUnresolved) succeeds.
func (e *GraphError) Unwrap() error {
	return ErrUnresolved
}

// renderCycle closes a loop back onto the migration it started from, which is what makes it read.
func renderCycle(cycle []string) string {
	return strings.Join(append(slices.Clone(cycle), cycle[0]), " → ")
}

// pluralProblems renders the problem count with the right noun.
func pluralProblems(n int) string {
	if n == 1 {
		return "1 problem"
	}

	return fmt.Sprintf("%d problems", n)
}
