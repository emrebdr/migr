package sql

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/remedy"
)

// ErrMalformed marks a .sql file whose sections cannot be read as this format, for errors.Is.
var ErrMalformed = errors.New("this file is not a migr SQL migration")

// FormatError reports a file this package refuses, and what about its shape it refuses.
type FormatError struct {
	// File is the migration filename at fault, as it appears in the migrations directory.
	File string
	// Detail states what about the file is wrong, in terms its author can act on.
	Detail string
	// Remedy is what to change in the file.
	Remedy string
}

// Error names the file first, then what is wrong with it.
func (e *FormatError) Error() string {
	return fmt.Sprintf("%s: %s", e.File, e.Detail)
}

// Next says what to change in the file.
func (e *FormatError) Next() string {
	return e.Remedy
}

// Unwrap reports the sentinel so errors.Is(err, ErrMalformed) succeeds.
func (e *FormatError) Unwrap() error {
	return ErrMalformed
}

// malformed builds the verdict for a file this package refuses, naming the file first and what to change.
func malformed(file, detail, next string) error {
	return &FormatError{File: file, Detail: detail, Remedy: next}
}

// missingMarker refuses a file with no up section, rather than reading the whole file as one.
func missingMarker(file string) error {
	return malformed(file, fmt.Sprintf("it has no %q line, so nothing here can say which of its "+
		"statements are the migration and which are the undo. A file whose marker is misspelled "+
		"would otherwise run its down section during an apply", MarkerUp),
		fmt.Sprintf("put a %q line of its own above the statements that apply the migration", MarkerUp))
}

// repeatedMarker refuses a file carrying one marker twice, naming both lines.
func repeatedMarker(file, marker string, first, second int) error {
	return malformed(file, fmt.Sprintf("it carries %q on lines %d and %d, and a section has one "+
		"beginning: nothing here can say which of them opens it", marker, first, second),
		fmt.Sprintf("delete one of the two %q lines", marker))
}

// markersReversed refuses a file whose down section precedes its up section.
func markersReversed(file string) error {
	return malformed(file, fmt.Sprintf("its %q line comes before its %q line, and a migration is "+
		"applied before it is ever rolled back", MarkerDown, MarkerUp),
		fmt.Sprintf("move the %q section above the %q section", MarkerUp, MarkerDown))
}

// strayStatement refuses SQL above the first marker, which belongs to no direction.
func strayStatement(file string, line int) error {
	return malformed(file, fmt.Sprintf("line %d holds SQL above the %q line, which belongs to "+
		"neither direction and would never run", line, MarkerUp),
		fmt.Sprintf("move it into a section, or make it a %s comment", lineComment))
}

// declaredBesideDown refuses a file that both writes an undo and declares it has none.
func declaredBesideDown(file string, down, declared int) error {
	return malformed(file, fmt.Sprintf("it carries %q on line %d and %q on line %d, and a "+
		"migration either has an undo or has none: nothing here can say which one it meant",
		MarkerDown, down, MarkerIrreversible, declared),
		fmt.Sprintf("keep the %q section or the %q line, and delete the other", MarkerDown, MarkerIrreversible))
}

// declaredAboveUp refuses a declaration above the up marker, where a comment is ignored in silence.
func declaredAboveUp(file string, line int) error {
	return malformed(file, fmt.Sprintf("line %d declares %q above the %q line, where every "+
		"comment belongs to no section and is passed over", line, MarkerIrreversible, MarkerUp),
		"move it below the statements it describes")
}

// declaredWithoutReason refuses a declaration that states no reason, which invariant #10 requires.
func declaredWithoutReason(file string, line int) error {
	return malformed(file, fmt.Sprintf("line %d declares %q and gives no reason, and the reason "+
		"is the whole content of that claim", line, MarkerIrreversible),
		fmt.Sprintf("write after %q what makes this migration one-way", MarkerIrreversible))
}

// statementBelowDeclaration refuses SQL under a declaration, which belongs to no direction.
func statementBelowDeclaration(file string, line int) error {
	return malformed(file, fmt.Sprintf("line %d holds SQL below the %q line, which belongs to "+
		"neither direction and would never run: a migration that declares it cannot be undone "+
		"has no undo to write", line, MarkerIrreversible),
		fmt.Sprintf("move the SQL above the %q line, or delete it", MarkerIrreversible))
}

// misplacedMarker refuses a no-transaction marker that declares no statement, or not the one it appears to.
func misplacedMarker(file string, line int, why string) error {
	return malformed(file, fmt.Sprintf("line %d carries %q, and %s", line, MarkerNoTransaction, why),
		"put the marker on a line of its own directly above the one statement that cannot run inside "+
			"a transaction")
}

// unreadable reports a migration whose file could not be read at all.
func unreadable(file string, err error) error {
	return remedy.With(fmt.Errorf("reading %s: %w", file, err),
		"make the file readable by the user migr runs as, or move it out of the migrations directory")
}

// unknownDirection reports a direction that is neither of the two a migration runs.
func unknownDirection(file string, dir ir.Direction) error {
	return malformed(file, fmt.Sprintf("it was asked for direction %q, which is neither %q nor %q",
		dir, ir.Up, ir.Down), "this is a bug in migr itself: report it at https://github.com/emrebdr/migr/issues")
}
