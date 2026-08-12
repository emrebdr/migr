package migration

import (
	"errors"
	"fmt"
	"path"
	"strings"
	"time"
)

// versionLayout is the reference time for the timestamp a migration filename carries.
//
// It is Go's reference instant, so it reads as nonsense to anyone who has not met that convention;
// versionShape is the same format written the way a diagnostic has to say it. Both are here rather
// than in the CLI because M5-T1's `migr create` has to produce exactly what this package accepts,
// and a layout string written down twice is a format with two definitions.
const versionLayout = "20060102T150405"

// versionShape is that layout written for someone reading an error message.
const versionShape = "YYYYMMDDThhmmss"

// separator divides a migration's version from its name, and its name's own words from each other.
const separator = "_"

// grammar is the shape of every migration filename, as a diagnostic states it.
const grammar = "<" + versionShape + ">_<snake_case name>.<ext>"

// example is a well-formed filename, shown alongside the grammar because a shape alone is abstract.
const example = "20260727T153000_add_users.sql"

// Migration is one migration file, identified by the name it is stored under.
type Migration struct {
	// Version is the UTC timestamp the filename carries, and the version a history row records.
	Version string
	// Name is the snake_case name, and the rest of that row.
	Name string
	// Ext is the extension including its dot, naming the runtime that will eventually run the file.
	Ext string
}

// Filename renders the name this migration is stored under, which is also its whole identity.
func (m Migration) Filename() string {
	return m.Version + separator + m.Name + m.Ext
}

// New builds the migration a file created at that instant would be, refusing a name the grammar does not accept.
func New(at time.Time, name, ext string) (Migration, error) {
	return Parse(Migration{Version: at.UTC().Format(versionLayout), Name: name, Ext: ext}.Filename())
}

// Parse reads a migration filename, or reports what about it is not one.
func Parse(filename string) (Migration, error) {
	ext := path.Ext(filename)
	if err := checkExt(filename, ext); err != nil {
		return Migration{}, err
	}

	version, name, found := strings.Cut(strings.TrimSuffix(filename, ext), separator)
	if !found {
		return Migration{}, malformed(filename,
			"it has no "+separator+" separating a version from a name")
	}

	if err := checkVersion(filename, version); err != nil {
		return Migration{}, err
	}

	if err := checkName(filename, name); err != nil {
		return Migration{}, err
	}

	return Migration{Version: version, Name: name, Ext: ext}, nil
}

// checkExt reports an extension that names no file type, which is every one shorter than a dot.
func checkExt(filename, ext string) error {
	if ext == "" {
		return malformed(filename, "it has no extension")
	}

	if ext == "." {
		return malformed(filename, "its extension is a bare dot")
	}

	return nil
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
				"its name %q is not snake_case: %q is not a lowercase letter or digit", name, bad))
		}
	}

	return nil
}

// firstNotLowerAlnum returns the first character of a word that snake_case does not allow.
func firstNotLowerAlnum(word string) (string, bool) {
	for _, r := range word {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return string(r), true
		}
	}

	return "", false
}
