// Package host serves the six callbacks a migration makes back to the engine:
// host.schema.apply, host.db.query, host.db.exec, host.capability.get,
// host.info, and host.log.
//
// docs/protocol.md §5 is the specification and it is normative. M6-T1 stated
// every payload and left one thing to this package on purpose -- how
// host.db.query is held to being read-only -- because a syntactic check, a
// read-only transaction, and a session-level setting all conform and they
// differ per engine.
//
// # Read-only is the server's answer, not the host's guess
//
// The driver runs the statement in a read-only transaction and PostgreSQL
// itself refuses the write, which this package reports as not_read_only through
// driver.ErrNotReadOnly. A statement inspected here instead would be a lie
// detector a data-modifying CTE defeats: WITH d AS (DELETE FROM users RETURNING
// *) SELECT count(*) FROM d begins with WITH and deletes rows, and refusing it
// by its first keyword is exactly the "merely by convention" the task ruled out
// (ADR 0032, B).
//
// The sentinel lives on the driver contract for the reason ErrLockTimeout does:
// this package cannot name a driver, and -32020 is otherwise unreachable.
//
// # The window
//
// A callback is only valid while an engine request is in flight, so a call is
// opened with Execute or Validate and closed by Operations or Close. Outside
// one, every callback is session_state. A Validate window is the narrower of
// the two: host.schema.apply and host.db.exec are refused inside it, because a
// self-check that emitted operations would change what a later apply emits, and
// one that wrote would not be a check.
//
// Serve never touches a Reader. It takes one message and returns one, or
// nothing for a notification. The same stream carries responses to the engine's
// own requests and §2.5's pending table is per direction, so the loop that
// decides which messages reach here is M6-T5's.
//
// # What plan mode changes, and what it does not
//
// One callback: host.db.exec records rather than runs, and says so with
// "executed": false rather than a fabricated row count, so a migration
// branching on rowsAffected can tell it is being previewed rather than lied to.
// host.schema.apply records in both modes and always did -- the engine collects
// a migration's whole operation list before it hashes, compiles, or runs any of
// it, so there is nothing there for plan mode to switch off (ADR 0030).
//
// # The mode is the call's, not the session's
//
// Execute states it and Validate opens a plan window, because a self-check is a
// question by construction. §4 puts the mode on migration.execute rather than
// on initialize, and one rollback is why: a migration that authors no down is
// asked for its down as a run and then for its up as the question the inverse
// is derived from, on one session. A server carrying one mode could not serve
// that conversation at all (ADR 0033).
//
// mode_test.go is the proof the task is named for. One fixture migration issues
// its callbacks through the codec, runs twice against one fake session, and the
// two ir.Marshal outputs must be byte-identical; a second branches on a live
// read and emits the same IR while the data is unchanged, and different IR
// across a change -- which is §6's caveat made mechanical. What it does not
// prove is that a real SDK behaves this way: a Go closure is not one, and
// test/conformance/runtime (M6-T9) is where that is settled.
//
// # host.db.exec runs outside the migration's transaction
//
// Every callback fires during engine.Source.Emit, which completes before the
// engine compiles anything or opens a transaction. So an escape-hatch write is
// committed on its own, under the migration lock, and a migration that fails
// afterwards does not undo it. That is the second thing using it costs, beside
// the ones the IR's raw operation costs, and §5 now says so.
//
// Status: the six callbacks and the window (M6-T3), the per-call mode and its
// identical-IR proof (M6-T4). The process that drives this is M6-T5.
package host
