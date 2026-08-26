// Package migr is the Go SDK for writing migr migrations.
//
// A migration is a type with an Up method. Everything else is optional, and each
// optional part is one method:
//
//	func init() { migr.Register(AddUsers{}) }
//
//	type AddUsers struct{}
//
//	func (m AddUsers) Up(ctx context.Context, s *migr.Schema) error {
//		return s.Apply(ctx, migr.Raw{Driver: "postgres", SQL: "CREATE TABLE users (id bigint)"})
//	}
//
//	func (m AddUsers) Down(ctx context.Context, s *migr.Schema) error { ... }  // optional
//	func (m AddUsers) Irreversible() string                           { ... }  // optional
//	func (m AddUsers) Validate() error                                { ... }  // optional
//	func (m AddUsers) Dependencies() []string                         { ... }  // optional
//
// Raw is the only operation this version emits. The schema builder that covers the
// whole v1 operation set arrives in M6-T8; until then a migration reaches its engine
// by name, which trades away the database independence the builder exists to give.
//
// # Import path and package name
//
// The import path ends in "go" and the package is named migr, because go is a
// reserved keyword and cannot be a package name. Import it plain and refer to migr:
//
//	import migr "github.com/emrebdr/migr/sdk/go"
//
// # Down, or a reason there is none
//
// A migration that implements neither Down nor Irreversible has its undo derived:
// the engine asks what Up does and inverts each operation. A migration that
// implements both is a registration error, because both cannot be true and only the
// declaration would ever be read.
//
// One thing has no inverse to derive. Schema.Exec sends a statement the migration
// wrote itself, which the engine commits on its own -- before the transaction the
// operations run in, and not undone by a later failure. So a migration that calls
// Exec and authors no Down is refused outright rather than half-reverted; write a
// Down, or declare the migration irreversible with a reason (ADR 0036).
//
// # Registration
//
// Register reads its caller's filename and parses it with the migration grammar, so
// a migration's version is the one its file already carries and cannot disagree with
// what the engine discovered. Nothing is reported from init: a filename the grammar
// refuses, a duplicate version, and a contradictory pair of methods are all recorded
// and answered at migration.discover, which is a request with an error response.
//
// # Cancellation
//
// The ctx is canceled when the engine abandons the request. A migration that works in
// chunks should check it; every callback on the Schema refuses once it is canceled,
// so one that does not check still stops at its next call rather than running the
// rest of them into a database nothing is waiting on.
//
// # Separate module
//
// This is its own module so that importing the SDK does not drag the CLI's dependency
// tree -- pgx, the MySQL and SQLite drivers, cobra, yaml -- into a user's application.
// It also declares a lower Go floor than the CLI, since it compiles inside other
// people's projects. The wire types are therefore written twice; both halves are held
// to docs/protocol.md rather than to each other.
//
// # One thing stdout cannot be protected from
//
// Run points the language's default print target at stderr before the first message,
// so a fmt.Println inside a migration cannot corrupt the protocol stream. It cannot
// cover a print from a migration's own init, which runs before main. That fails
// loudly rather than silently -- the engine reports a line it could not frame, naming
// it -- and the alternative, redirecting from this package's init, would hijack
// stdout for every program that imports the SDK, including the user's own tests.
//
// Status: the migration contract and the protocol client (M6-T7). The schema builder
// is M6-T8 and the runtime conformance fixtures are M6-T9 (see TASKS.md).
package migr
