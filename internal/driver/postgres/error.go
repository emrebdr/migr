package postgres

import (
	"errors"
	"fmt"
)

// ErrDSN marks a connection string this driver cannot parse, for errors.Is.
var ErrDSN = errors.New("connection string cannot be parsed")

// ErrConnect marks a server this driver could not open a usable session against, for errors.Is.
var ErrConnect = errors.New("cannot open a PostgreSQL session")

// ErrServerTooOld marks a server older than the release this driver can reason about.
var ErrServerTooOld = errors.New("PostgreSQL server is older than this driver supports")

// ErrNoSchema marks a configured schema the connected server does not have, for errors.Is.
var ErrNoSchema = errors.New("the configured schema does not exist on this server")

// SchemaError reports a configured schema PostgreSQL would silently ignore.
type SchemaError struct {
	// Schema is the name migr.yaml configured, which the server does not have.
	Schema string
}

// Error names the schema and what PostgreSQL would have done with it.
func (e *SchemaError) Error() string {
	return fmt.Sprintf("%v: databases.<name>.schema is %q, and PostgreSQL drops an entry naming no "+
		"schema from search_path rather than reporting it, so the first migration would fail with "+
		"\"no schema has been selected to create in\" and name neither", ErrNoSchema, e.Schema)
}

// Unwrap reports the sentinel so errors.Is(err, ErrNoSchema) succeeds.
func (e *SchemaError) Unwrap() error {
	return ErrNoSchema
}

// DSNError reports an unparsable connection string without ever rendering it.
type DSNError struct {
	// Err is the underlying parse failure, unwrapped for errors.Is but never shown.
	Err error
}

// Error states that the connection string will not parse, and deliberately not what it was.
func (e *DSNError) Error() string {
	return fmt.Sprintf("%v (its text is withheld: it may carry a password)", ErrDSN)
}

// Unwrap reports the sentinel and the underlying cause, so errors.Is finds either.
func (e *DSNError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrDSN}
	}

	return []error{ErrDSN, e.Err}
}

// VersionError reports a server whose version this driver will not work against.
type VersionError struct {
	// Reported is the raw server_version_num, unformatted: see this package's doc.
	Reported int
	// Minimum is the oldest server_version_num this driver accepts.
	Minimum int
}

// Error names the version the server reported and the oldest one this driver accepts.
func (e *VersionError) Error() string {
	return fmt.Sprintf("%v: the server reports server_version_num %d, and this driver requires "+
		"at least %d (PostgreSQL %d)", ErrServerTooOld, e.Reported, e.Minimum, e.Minimum/versionScale)
}

// Unwrap reports the sentinel so errors.Is(err, ErrServerTooOld) succeeds.
func (e *VersionError) Unwrap() error {
	return ErrServerTooOld
}
