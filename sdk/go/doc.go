// Package migr is the Go SDK for writing migr migrations.
//
// It exposes the migration contract — Up, Down, Validate, Dependencies, and
// metadata — plus a schema builder that mirrors the Operation IR one-for-one,
// and a protocol client that speaks to the engine over stdin and stdout.
//
// # Import path and package name
//
// The import path ends in "go" but the package is named migr, because go is a
// reserved keyword and cannot be a package name:
//
//	import "github.com/emrebdr/migr/sdk/go"
//
//	func (m AddUsers) Up(s *migr.Schema) error {
//		s.CreateTable("users", func(t *migr.Table) { ... })
//	}
//
// # Separate module
//
// This is its own module so that importing the SDK does not drag the CLI's
// dependency tree — pgx, the MySQL and SQLite drivers, cobra, yaml — into a
// user's application. It also declares a lower Go floor than the CLI, since it
// compiles inside other people's projects.
//
// Status: scaffolding. Implemented in M6-T7 and M6-T8 (see TASKS.md).
package migr
