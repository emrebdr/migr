package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// versionQuery reads the library's version and, in doing so, proves the session works.
const versionQuery = `SELECT sqlite_version()`

// The oldest release this driver is written against, named by its parts so a message can render it.
//
// 3.25 is where ALTER TABLE ... RENAME COLUMN arrived and where RENAME TO started rewriting the
// references that follow a table -- which is the behavior M8-T4's rebuild has to work around, so
// reasoning about anything older would be reasoning about a different engine.
const (
	minimumMajor = 3
	minimumMinor = 25
)

// versionParts is how many numbers sqlite_version() reports, being major, minor and patch.
const versionParts = 3

// serverVersion is the SQLite release behind a session, in the form comparisons need.
type serverVersion struct {
	major int
	minor int
	patch int
}

// String renders the version the way SQLite names its own releases.
func (v serverVersion) String() string {
	return fmt.Sprintf("%d.%d.%d", v.major, v.minor, v.patch)
}

// AtLeast reports whether the library is at or past a release, which is how M8-T3 asks about 3.35.
func (v serverVersion) AtLeast(major, minor int) bool {
	if v.major != major {
		return v.major > major
	}

	return v.minor >= minor
}

// detectVersion asks the library which release it is, which is also this session's health check.
func detectVersion(ctx context.Context, c *sqliteConn) (serverVersion, error) {
	var reported string

	if err := c.runner().QueryRowContext(ctx, versionQuery).Scan(&reported); err != nil {
		return serverVersion{}, fmt.Errorf("%w: reading the SQLite version: %w", ErrConnect, err)
	}

	return interpretVersion(reported)
}

// interpretVersion turns what the library reported into a version, or refuses to read it.
func interpretVersion(reported string) (serverVersion, error) {
	version, err := parseVersion(reported)
	if err != nil {
		return serverVersion{}, err
	}

	if !version.AtLeast(minimumMajor, minimumMinor) {
		return serverVersion{}, &VersionError{Reported: reported,
			Minimum: fmt.Sprintf("%d.%d", minimumMajor, minimumMinor)}
	}

	return version, nil
}

// parseVersion reads the three numbers sqlite_version() reports, refusing anything else.
func parseVersion(reported string) (serverVersion, error) {
	parts := strings.Split(strings.TrimSpace(reported), ".")
	if len(parts) != versionParts {
		return serverVersion{}, unreadableVersion(reported)
	}

	numbers := make([]int, versionParts)

	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return serverVersion{}, unreadableVersion(reported)
		}

		numbers[i] = number
	}

	return serverVersion{major: numbers[0], minor: numbers[1], patch: numbers[2]}, nil
}

// unreadableVersion reports a version string this driver cannot compare, which it will not guess at.
func unreadableVersion(reported string) error {
	return fmt.Errorf("%w: sqlite_version() reported %q, and a version is three numbers separated "+
		"by dots: a verdict this driver keys to a release cannot be resolved against something it "+
		"cannot read", ErrConnect, reported)
}
