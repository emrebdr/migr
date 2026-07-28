// Package history defines the migration history store contract, the record it holds, and the
// neutral operations that create the table it holds them in.
//
// There is no SQL here and there never will be. The history table is created through
// driver-compiled IR operations, because raw SQL lives only in driver packages (CLAUDE.md
// invariant #3), and this package is the one place the table's shape is written down — otherwise
// PostgreSQL, SQLite, and MySQL would each invent a different one.
//
// Status: contract complete (M2-T5). Reconciliation is M4-T3; the first Store that satisfies this
// is M3-T6. See docs/adr/0013-migration-history.md.
//
// # Two vocabularies, deliberately kept apart
//
// An Outcome is how one run ended: Applied, Failed, or Dirty. It is a fact about a run, written
// once by the store method that recorded it.
//
// A state is what M4-T3 resolves a migration to by comparing the records against the files on
// disk: applied, pending, missing, or drifted. It is derived, it is not stored, and it can change
// without anything being written.
//
// Both belong here and both would naturally be called State, so the run axis is Outcome and the
// name State is left for M4-T3. Note that "applied" appears on both — a migration whose latest
// run applied it and whose file still hashes the same reconciles to applied, and the two senses
// agreeing is the ordinary case rather than a confusion.
//
// Outcome is three values rather than a bool because Failed and Dirty differ in what an operator
// must do next. A failed run left the schema unchanged and the next up may proceed; a dirty one
// did not, and M4-T9 requires the next up to refuse until it is explicitly resolved. Blocking is
// where that difference is named, so the engine asks rather than testing for Dirty itself.
//
// # The table is a log, not a state
//
// Every run appends a row, including rollbacks and failures. Nothing is updated and nothing is
// deleted, so migr history has an execution log to print (M5-T3) and a rollback does not erase
// the evidence that it happened. Current state is folded from the log by M4-T3, which is also
// what invariant #6's "never silently rewrite" asks for.
//
// # The key is (batch, ordinal), because v1 IR has no identity column
//
// A log wants a monotonic key and ir.Column offers no serial, identity, or auto-increment — a
// default is a literal or one of four neutral functions. applied_at cannot stand in: two rows can
// share a millisecond, and migr down rolls back the last batch in reverse order (M4-T8), so a tie
// there reverses two migrations in the wrong order.
//
// A batch is one migr invocation and an ordinal is the position within it, both supplied by the
// executor. That needs no clock, no randomness, and no engine feature, and it is exactly the
// order a rollback reverses. Both number from one, so the zero value is not a valid position.
//
// # This package reads no clock
//
// AppliedAt and Duration are fields a caller fills in. forbidigo bans time.Now everywhere outside
// cmd and internal/cli, including in tests, and inventing a Clock abstraction for a single
// consumer buys nothing: M4-T5 already owns the run, so it owns the instant the run started.
// Tests here use fixed instants, which is what invariant #8 would have required anyway.
//
// Stamp fills the two fields that describe the build rather than the run — the migr version and
// the IR version — because a caller has no business supplying either. Validate requires both, so
// a driver that forgot to call Stamp fails before it writes rather than recording a blank column.
//
// # Each write method owns its outcome
//
// Record writes Applied, MarkFailed writes Failed, MarkDirty writes Dirty and takes the statement
// index that outcome needs. A caller therefore cannot record a failure marked applied, and cannot
// mark a run dirty without saying where it stopped. Outcome, Error, and LastStatement are
// consequently read-side fields: populated by List and Latest, ignored on write. The alternative
// was a second struct differing in three fields, which is a worse trade than three documented
// fields.
//
// LastStatement is 1-based, matching the numbering the golden-file harness already uses, so zero
// means unambiguously that no statement succeeded. A run where none did left the schema unchanged
// and is Failed rather than Dirty — which is why Validate rejects a dirty run that names no
// statement. M9-T6 resumes from this index, and an off-by-one there replays a statement that
// already ran.
//
// # Ensure cannot be idempotent through IR alone
//
// Schema returns the operations that create the table, and ir.CreateTable has no IfNotExists.
// A driver therefore checks existence with its own internal query — which the driver contract
// already allows — and compiles Schema only when the table is absent. Adding IfNotExists to the
// IR would be a wire-format change with its own ADR, and the history table is a poor reason for
// one.
//
// Schema returns no error even though every schema.Parse call inside it can fail. The spellings
// are compile-time constants, panic is banned here, and an error no call site could ever hit is
// worse than the test that proves it cannot: a misspelling leaves a zero schema.Type, which
// Column.Validate rejects, and a package test walks every emitted operation.
//
// direction and outcome are string columns rather than enums. schema.Enum exists, but a fourth
// outcome would then require an ALTER on migr's own history table — the same reasoning that sizes
// checksum loosely at string(255) rather than to the 135 characters migr1 happens to need.
//
// # A corrupt row is not drift
//
// CorruptError reports a recorded value this build cannot read back — a checksum that will not
// parse being the case ADR 0009 anticipated. It unwraps to both ErrCorrupt and the underlying
// cause, so a caller can act on "this row is unreadable" and still say which part of it was.
//
// It is deliberately not a checksum mismatch. Drift is exit code 4 and means someone edited an
// applied migration; an unreadable row means this history table needs manual repair. Reporting
// the second as the first is how operators learn to ignore exit code 4.
//
// # Import direction, now enforced by the thing it warned about
//
// This package must never import internal/driver. ADR 0011 recorded that as a review rule,
// noting that depguard cannot catch it — its deny prefix deliberately leaves the contract package
// importable so that core can depend on it.
//
// Adding History to Conn closed that gap without adding anything. internal/driver now imports this
// package, so importing it back is an import cycle the compiler refuses, and every package under
// internal/driver/ reaches the contract too, so every path from here into that subtree is the same
// cycle — with depguard's prefix deny covering the subtree besides. The rule stopped being
// review-enforced the moment the contract named Store.
package history
