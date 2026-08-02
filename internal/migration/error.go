package migration

import (
	"errors"
	"fmt"
	"strings"
)

// ErrMalformed marks a file in the migrations directory that is not named as a migration.
var ErrMalformed = errors.New("not a migration filename")

// ErrDuplicateVersion marks two migrations claiming one version, for errors.Is.
var ErrDuplicateVersion = errors.New("two migrations claim one version")

// ErrNoDirectory marks a migrations directory that is not there at all, for errors.Is.
var ErrNoDirectory = errors.New("the migrations directory does not exist")

// remedy is what an operator does about a file this package refuses, and it never varies.
const remedy = "rename it, or move it out of the migrations directory"

// NameError reports a file that is not named as a migration, and what about the name is not.
type NameError struct {
	// File is the filename at fault, as it appears in the migrations directory.
	File string
	// Detail states what about the name is wrong, in terms its author can act on.
	Detail string
}

// Error names the file, what is wrong with its name, the grammar it missed, and what to do.
func (e *NameError) Error() string {
	return fmt.Sprintf("%s is not a migration: %s. Migrations are named %s, as %s — %s",
		e.File, e.Detail, grammar, example, remedy)
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
		"records or which one --to names: rename one of them",
		strings.Join(e.Files, " and "), e.Version)
}

// Unwrap reports the sentinel so errors.Is(err, ErrDuplicateVersion) succeeds.
func (e *DuplicateError) Unwrap() error {
	return ErrDuplicateVersion
}
