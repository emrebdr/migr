package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// versionScale is the multiplier PostgreSQL 10 and later use to encode the major release.
const versionScale = 10000

// minimumVersion is PostgreSQL 10, the oldest release the versionScale encoding applies to.
const minimumVersion = 10 * versionScale

// versionQuery reads the server's version and, in doing so, proves the session works.
const versionQuery = `SELECT current_setting('server_version_num')::int`

// serverVersion is a connected server's version, in the numeric form comparisons need.
type serverVersion int

// Major returns the release number a capability rule keys its verdicts to.
func (v serverVersion) Major() int {
	return int(v) / versionScale
}

// AtLeast reports whether the server is at or past a major release.
func (v serverVersion) AtLeast(major int) bool {
	return v.Major() >= major
}

// String renders the version the way PostgreSQL names its own releases.
func (v serverVersion) String() string {
	return fmt.Sprintf("%d.%d", v.Major(), int(v)%versionScale)
}

// detectVersion asks the server which release it is, which is also this session's health check.
func detectVersion(ctx context.Context, conn *pgx.Conn) (serverVersion, error) {
	var reported int

	if err := conn.QueryRow(ctx, versionQuery).Scan(&reported); err != nil {
		return 0, fmt.Errorf("%w: reading the server version: %w", ErrConnect, err)
	}

	return interpretVersion(reported)
}

// interpretVersion turns what a server reported into a version, or refuses to read it.
func interpretVersion(reported int) (serverVersion, error) {
	if reported < minimumVersion {
		return 0, &VersionError{Reported: reported, Minimum: minimumVersion}
	}

	return serverVersion(reported), nil
}
