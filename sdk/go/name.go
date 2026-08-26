package migr

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// versionLayout is the reference time for the timestamp a migration filename carries.
const versionLayout = "20060102T150405"

// versionShape is that layout written for someone reading an error message.
const versionShape = "YYYYMMDDThhmmss"

// separator divides a migration's version from its name, and its name's own words from each other.
const separator = "_"

// ext is the extension a Go migration is stored under, and the only one this SDK loads.
const ext = ".go"

// grammar is the shape of every migration filename, as a diagnostic states it.
const grammar = "<" + versionShape + ">" + separator + "<snake_case name>" + ext

// named is what a filename says about the migration it holds.
type named struct {
	// version is the UTC timestamp the filename carries, which every message and history row uses.
	version string
	// name is the snake_case name, for a message a human reads.
	name string
	// file is the filename itself, which is what migration.discover reports.
	file string
}

// parseName reads a migration filename, or reports what about it is not one.
func parseName(filename string) (named, error) {
	found := path.Ext(filename)
	if found != ext {
		return named{}, malformed(filename,
			fmt.Sprintf("a Go migration ends in %s, and this ends in %q", ext, found))
	}

	version, name, cut := strings.Cut(strings.TrimSuffix(filename, found), separator)
	if !cut {
		return named{}, malformed(filename,
			"it has no "+separator+" separating a version from a name")
	}

	if err := checkVersion(filename, version); err != nil {
		return named{}, err
	}

	if err := checkName(filename, name); err != nil {
		return named{}, err
	}

	return named{version: version, name: name, file: filename}, nil
}

// checkVersion reports a version that is not the UTC timestamp the grammar requires.
func checkVersion(filename, version string) error {
	if version == "" {
		return malformed(filename, "it has no version before its first "+separator)
	}

	if _, err := time.Parse(versionLayout, version); err != nil {
		return malformed(filename, fmt.Sprintf("%q is not a %s UTC timestamp: %s",
			version, versionShape, describeTimeError(err)))
	}

	return nil
}

// describeTimeError renders what time.Parse objected to, without repeating the value back.
func describeTimeError(err error) string {
	var parseErr *time.ParseError
	if errors.As(err, &parseErr) && parseErr.Message != "" {
		return strings.TrimPrefix(parseErr.Message, ": ")
	}

	return "it is not " + versionShape
}

// checkName reports a name that is not snake_case: lowercase words joined by single separators.
func checkName(filename, name string) error {
	if name == "" {
		return malformed(filename, "it has no name after its version")
	}

	for _, word := range strings.Split(name, separator) {
		if word == "" {
			return malformed(filename, fmt.Sprintf(
				"its name %q has a leading, trailing, or doubled %s", name, separator))
		}

		if bad, found := firstNotLowerAlnum(word); found {
			return malformed(filename, fmt.Sprintf(
				"its name %q carries %q, and a name is lowercase letters and digits", name, bad))
		}
	}

	return nil
}

// firstNotLowerAlnum returns the first character a snake_case word may not carry.
func firstNotLowerAlnum(word string) (string, bool) {
	for _, r := range word {
		lower := r >= 'a' && r <= 'z'
		digit := r >= '0' && r <= '9'

		if !lower && !digit {
			return string(r), true
		}
	}

	return "", false
}

// malformed reports a filename the grammar refuses, naming the grammar it was held to.
func malformed(filename, why string) error {
	return fmt.Errorf("%w: %s is not %s, because %s", ErrMalformedName, filename, grammar, why)
}
