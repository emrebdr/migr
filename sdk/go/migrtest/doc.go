// Package migrtest tests migrations written against the Go SDK.
//
// Three helpers, and the division between them is who actually knows the answer:
//
//	c := migrtest.Up(t, AddUsers{})                  // what IR did this half emit
//	r := migrtest.Reversal(t, AddUsers{})            // how is this undone
//	p := migrtest.New(t, migrtest.Project{...})      // does it run, and roll back
//
// The first two run in this process, reach no database, and start nothing. The third runs the real
// migr binary against a database it throws away, because nothing in this SDK can compile an
// operation into SQL -- the drivers are the engine's.
//
// # Asserting the operations a migration emits
//
// Up and Down run one half against a recording host and report everything that host was asked. An
// operation comes back as the canonical IR document the engine would have received, and Is renders
// what you expected through the same encoder and compares the bytes:
//
//	migrtest.Up(t, AddUsers{}).Is(t,
//		migr.CreateTable{Table: "users", Columns: []migr.Column{...}, PrimaryKey: []string{"id"}},
//	)
//
// Kinds is the looser assertion, for a test that cares which operations ran and not what they said.
//
// A migration may read the database and branch on the answer, so a Stage says what its questions are
// answered with. Three of them are refused rather than guessed when the stage does not answer them --
// a read, a capability question, and host.info -- because a zero value answered in place of a missing
// one sends the migration down a branch the test never meant to ask about:
//
//	migrtest.Stage{
//		Mode:  migr.ModePlan,
//		Reads: map[string]migr.Rows{"SELECT count(*) FROM users": migrtest.Answer(
//			[]string{"count"}, []string{"0"},
//		)},
//	}.Up(t, AddUsers{})
//
// Every other answer is a copy of the engine's own host. In particular Schema.Exec is recorded and
// never performed -- there is no database here -- and answered as the engine answers it: held under
// plan mode, and reported as performed under apply with whatever Stage.Affected says it changed.
//
// # Asserting rollback correctness
//
// Reversal reports which of the three answers docs/protocol.md §7.4 names a migration gives, and
// captures every half it has:
//
//	r := migrtest.Reversal(t, AddUsers{})
//
//	r.Undo                                      // authored, declared, or derived
//	r.Reason                                    // the author's reason, where one is declared
//	r.Down.Is(t, migr.DropTable{Table: "users"}) // empty exactly when the engine derives the inverse
//
// It also fails one migration shape outright: an up that sends statements of its own and authors no
// down. A statement has no inverse to derive, so the engine refuses that rollback rather than half
// reverting it, and this says so before a rollback does.
//
// What it does not do is invert anything. All twenty inverses live in the engine, so that two
// SDKs could not reach two answers for one migration -- a copy here would be a third, and the day
// it drifted it would pass a down the engine refuses. So whether every operation can be inverted is
// answered by the engine, through Running.Validate, and whether a rollback actually leaves the
// database where it found it is answered by Running.RoundTrip.
//
// # Running against a throwaway database
//
// New stages a project over a database of its own and drives the real binary:
//
//	p := migrtest.New(t, migrtest.Project{Dir: "./migrations"})
//
//	p.Up(t)
//	p.Validate(t).NoProblems(t)
//	p.RoundTrip(t)
//	p.Down(t, "--step", "1")
//
// The database is a SQLite file in the test's own t.TempDir(), because that engine needs no daemon;
// Driver and DSN together point it at a real PostgreSQL or MySQL instead. Each project
// records its runs in a history table named for the test, since the database is the thing that
// remembers and one table shared between cases makes the second case meet the first one's drift.
//
// The migr.yaml this helper writes lives under <project>/.migr/, not in a temporary directory, and
// that is measured: docs/protocol.md §9 makes the directory holding migr.yaml the runtime's working
// directory, and both adapters probe from it -- the Go one runs go list -m there. A config outside
// the module fails with "this project is not inside a Go module" before a migration is ever loaded.
// That directory ignores itself from the inside and what this helper puts there is
// removed when the test ends.
//
// The binary is MIGR_BIN, or migr on PATH. When there is neither, this fails and names both rather
// than skipping: a check that drops itself silently reports a pass it did not earn. Found reports
// the same answer without starting anything, for a suite that would rather skip deliberately.
//
// # Where the test file goes
//
// Not in the migrations directory. Discovery is strict and dotfiles are its only exemption (§9), so
// a migrations_test.go sitting beside the migrations is refused by name -- "migrations" is not a
// timestamp -- and every command fails before a migration is loaded. The first two helpers are
// unaffected, since they take a migration as a value; the third points at the directory from
// outside it:
//
//	migrations/20260301T090000_add_users.go
//	migrationtest/migrations_test.go        // migrtest.New(t, migrtest.Project{Dir: "../migrations"})
//
// Which also means a test imports the migrations package rather than being inside it, so the
// migration types it constructs are the exported ones.
//
// A command that exits non-zero fails the test with what migr wrote on stderr. One case is worth
// knowing because the exit code alone cannot settle it: migr validate exits 2 both for "the checks
// found problems", whose report is on stdout, and for "this command was given something it refuses",
// whose message is on stderr. Validate tells them apart by whether a document was printed, so a
// refused filename arrives as a failure naming the file rather than as an empty report.
//
// RoundTrip applies and rolls back twice. What that catches is residue: a down that leaves a table
// behind makes the second up fail, and the server is what says so. It is not a comparison of two
// schema snapshots -- that needs Conn.Introspect, which is M11-T2, and an SDK composing catalog SQL
// is what invariant #3 forbids. The repository's own end-to-end suites measure the same property the
// same way.
//
// Status: all three helpers (M10-T6).
package migrtest
