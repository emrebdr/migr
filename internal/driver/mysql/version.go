package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// versionQuery reads what the server is and, in doing so, proves the session works.
//
// Two columns because one of them lies by design. MariaDB prepends "5.5.5-" to the version it
// announces so that clients too old to read a two-digit major do not misparse 10.x, and some builds
// carry that prefix into VERSION() as well -- so a driver reading the first number alone would take
// MariaDB 11.4 for MySQL 5.5. version_comment is the second witness: it names the distribution
// ("MySQL Community Server - GPL", "mariadb.org binary distribution") and is what settles the
// variant when the version string does not spell it out. Both measured on 8.0.46 and 11.4.13.
const versionQuery = `SELECT VERSION(), @@version_comment`

// mariaMarker is what both the version string and the version comment spell when the server is one.
const mariaMarker = "mariadb"

// legacyMariaPrefix is the fake major MariaDB announces so that pre-10 clients do not misparse it.
const legacyMariaPrefix = "5.5.5-"

// versionParts is how many numbers this driver reads out of a version, being major, minor and patch.
const versionParts = 3

// Variant is which of the two compatible engines a session is connected to.
type Variant string

// The variants this driver tells apart, which is the done-when M9-T1 names.
const (
	// MySQL is Oracle's, and is what a server that does not say otherwise is taken to be.
	MySQL Variant = "MySQL"
	// MariaDB is the fork, which differs in version numbering and in what it clamps.
	MariaDB Variant = "MariaDB"
)

// String renders the variant the way its own distribution spells it.
func (v Variant) String() string {
	return string(v)
}

// The oldest release of each variant this driver is written against.
//
// MySQL 8.0 is where M9-T3's gates become askable at all -- CHECK constraints arrive at 8.0.16 and
// ALGORITHM=INSTANT at 8.0.12 -- and it is the release whose data dictionary makes a single DDL
// statement atomic, which is the one thing that softens M9's premise. MariaDB 10.5 is
// go-sql-driver/mysql's own floor, so reasoning below it would be reasoning about a server the
// library beneath this driver does not claim to speak to.
var minimums = map[Variant][2]int{
	MySQL:   {8, 0},
	MariaDB: {10, 5},
}

// serverVersion is a connected server's flavor and release, in the form comparisons need.
type serverVersion struct {
	variant Variant
	major   int
	minor   int
	patch   int
}

// String renders the variant and release, which is what a migration reads out of host.info.
func (v serverVersion) String() string {
	return fmt.Sprintf("%s %d.%d.%d", v.variant, v.major, v.minor, v.patch)
}

// Variant reports which engine this is, so a verdict can differ between the two.
func (v serverVersion) Variant() Variant {
	return v.variant
}

// AtLeast reports whether the server is at or past a release of the variant it already is.
func (v serverVersion) AtLeast(variant Variant, major, minor int) bool {
	return v.AtLeastPatch(variant, major, minor, 0)
}

// AtLeastPatch is AtLeast where the release that matters is named down to its patch number.
func (v serverVersion) AtLeastPatch(variant Variant, major, minor, patch int) bool {
	if v.variant != variant {
		return false
	}

	if v.major != major {
		return v.major > major
	}

	if v.minor != minor {
		return v.minor > minor
	}

	return v.patch >= patch
}

// detectVersion asks the server what it is, which is also this session's health check.
func detectVersion(ctx context.Context, c *mysqlConn) (serverVersion, error) {
	var reported, comment string

	if err := c.runner().QueryRowContext(ctx, versionQuery).Scan(&reported, &comment); err != nil {
		return serverVersion{}, fmt.Errorf("%w: reading the server version: %w", ErrConnect, err)
	}

	return interpretVersion(reported, comment)
}

// interpretVersion turns what a server reported into a version, or refuses to read it.
func interpretVersion(reported, comment string) (serverVersion, error) {
	version, err := parseVersion(reported, comment)
	if err != nil {
		return serverVersion{}, err
	}

	return version, checkMinimum(version, reported)
}

// checkMinimum refuses a server older than the release of its own variant this driver reasons about.
func checkMinimum(version serverVersion, reported string) error {
	floor := minimums[version.variant]
	if version.AtLeast(version.variant, floor[0], floor[1]) {
		return nil
	}

	return &VersionError{Reported: reported, Variant: version.variant,
		Minimum: fmt.Sprintf("%d.%d", floor[0], floor[1])}
}

// parseVersion reads the variant and the three numbers out of what a server announced.
func parseVersion(reported, comment string) (serverVersion, error) {
	variant := variantOf(reported, comment)

	numbers, err := parseNumbers(release(reported, variant), reported)
	if err != nil {
		return serverVersion{}, err
	}

	return serverVersion{variant: variant, major: numbers[0], minor: numbers[1],
		patch: numbers[2]}, nil
}

// variantOf reads which engine announced itself, taking anything that does not say MariaDB as MySQL.
func variantOf(reported, comment string) Variant {
	if names(reported, mariaMarker) || names(comment, mariaMarker) {
		return MariaDB
	}

	return MySQL
}

// names reports whether a version string or comment spells a marker, in whatever case it used.
func names(announced, marker string) bool {
	return strings.Contains(strings.ToLower(announced), marker)
}

// release strips what a variant puts in front of its real numbers, and what it puts after them.
func release(reported string, variant Variant) string {
	trimmed := strings.TrimSpace(reported)

	if variant == MariaDB {
		trimmed = strings.TrimPrefix(trimmed, legacyMariaPrefix)
	}

	numbers, _, _ := strings.Cut(trimmed, "-")

	return numbers
}

// parseNumbers reads major, minor and patch, refusing a version it would have to guess at.
func parseNumbers(release, reported string) ([versionParts]int, error) {
	var numbers [versionParts]int

	parts := strings.Split(release, ".")
	if len(parts) != versionParts {
		return numbers, &UnreadableVersionError{Reported: reported, Detail: fmt.Sprintf(
			"the release part of it is %q, where a version is three numbers separated by dots",
			release)}
	}

	for i, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return numbers, &UnreadableVersionError{Reported: reported, Detail: fmt.Sprintf(
				"%q is not a number this driver can compare", part)}
		}

		numbers[i] = number
	}

	return numbers, nil
}
