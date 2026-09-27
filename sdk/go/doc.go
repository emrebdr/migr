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
//		return s.CreateTable("users").
//			Column("id", migr.UUID()).
//			Column("email", migr.String(255)).
//			PrimaryKey("id").
//			Apply(ctx)
//	}
//
//	func (m AddUsers) Down(ctx context.Context, s *migr.Schema) error { ... }  // optional
//	func (m AddUsers) Irreversible() string                           { ... }  // optional
//	func (m AddUsers) Validate() error                                { ... }  // optional
//	func (m AddUsers) Dependencies() []string                         { ... }  // optional
//
// # The builder
//
// Schema.CreateTable, Schema.Table, Schema.View, and Schema.Raw each open a change and
// return a chain; every v1 operation is a method on one of them. Nothing is emitted
// until Apply, which sends the whole chain in one call and empties it, so applying twice
// does not emit twice.
//
// A chain built and never applied emits nothing, and a migration that reports success
// having changed nothing is the worst failure this format has. So the window a migration
// acts through remembers what it handed out: a chain still holding operations when Up or
// Down returns fails the migration, naming what was never applied. A chain nothing was
// added to is not one.
//
// Errors are deferred to Apply rather than returned at every link, because a chain
// cannot return one. That includes a logical type: migr.String(0) has no canonical
// spelling, and carries the reason it has none until the operation using it is emitted.
//
// A column is NOT NULL unless the chain says Null, which is what an unqualified column
// means in every SDK -- see docs/ir.md §7.2, where the convention lives because it has
// to be one convention rather than one per language.
//
// The structs the builder produces are exported and can be constructed by hand, and
// Schema.Apply takes them directly. The builder is sugar over that, never a second path:
// a chain and the operation written out emit the same bytes.
//
// Schema.Raw and Schema.Exec are not the same escape hatch. Raw emits an operation: the
// engine records it, compiles it, shows it in migr plan, and runs it in the migration's
// transaction, and it names one engine so no other will run it. Exec reaches the database
// directly, before that transaction opens, and is the one thing below that no Down can be
// derived over.
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
// people's projects. The wire types and the IR structs are therefore written twice;
// both halves are held to docs/protocol.md and docs/ir.md rather than to each other,
// and each has a test that reads its document and checks every vector in it.
//
// One consequence is a scanner this copy owns as the engine's does: encoding/json
// escapes U+2028 and U+2029 whatever SetEscapeHTML is told, and docs/ir.md §3.3 emits
// them as UTF-8, so marshal rewrites them and must tell an escape from the text of one
// (ADR 0047). A fix applied to one copy and not the other passes on that side, which is
// the standing cost of writing the IR three times.
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
// Status: the migration contract, the protocol client, and the whole v1 schema builder
// (M6-T7, M6-T8). The runtime conformance fixtures are M6-T9 (see TASKS.md).
package migr
