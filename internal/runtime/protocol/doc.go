// Package protocol implements the wire protocol between the engine and a
// language runtime: line-delimited JSON-RPC 2.0 over the child process's stdin
// and stdout, with stderr passed through as log output.
//
// docs/protocol.md is the specification, and it is normative. This package
// holds the names and numbers that document assigns -- the methods, the
// application error codes and their slugs -- and spec_test.go holds the two to
// each other: a method here with no section fails, a section naming no method
// here fails, and a code tabulated under the wrong slug fails.
//
// # What M6-T1 settled
//
// The engine calls initialize, migration.discover, migration.execute,
// migration.validate, and shutdown. The runtime calls back with
// host.schema.apply, host.db.query, host.db.exec, host.capability.get,
// host.info, and host.log. Either side may send $/cancel.
//
// The engine offers a set of protocol versions at initialize and the runtime
// picks one, because an SDK installed from a package registry and a CLI
// installed from a release move on different clocks. irVersion is stated rather
// than negotiated: the two are independent version lines and coupling them
// would make an IR addition a protocol break (ADR 0030, A).
//
// A migration with no Down answers no_down_authored and one declaring itself
// one-way answers declared_irreversible, both as JSON-RPC errors carrying a
// slug. They are refusals rather than results because a client that forgets a
// result case reports zero operations -- a rollback that undoes nothing and
// says it worked -- where a forgotten error case raises (ADR 0030, C).
//
// Every application code sits in -32000..-32099, the band JSON-RPC 2.0
// delegates to the implementation. LSP's -32800 for a canceled request is not
// reused: it sits in the range JSON-RPC reserves for its own predefined errors.
//
// # host.schema.apply records in both modes
//
// This is the one place the sketch in CLAUDE.md §4.3 was wrong, and the
// specification supersedes it. The engine never executes an operation as it
// arrives: it collects a migration's whole list, hashes it -- that hash is half
// the migration's checksum -- compiles it through the driver, groups the
// statements, and runs them in one transaction. None of that can begin until
// the migration has finished emitting.
//
// So plan mode changes exactly one callback, host.db.exec, and nothing else.
// That is a smaller difference than ADR 0002 implied and a better one: plan and
// apply diverge in one stated place rather than several.
//
// # The codec, and what ends a session
//
// M6-T2 added the wire itself: Message with its three-state ID, Encode and
// Decode, and a Reader and Writer over pipes. Two rules in it are load-bearing
// and neither is obvious from the types.
//
// A line that cannot be framed ends the session. Not that line -- the session.
// The reader is spent afterwards and every later Read returns the same failure,
// because the id was exactly the part that could not be read, so skipping the
// line and carrying on would leave whoever was waiting on that request waiting
// forever. Refuse builds the one message §2.6 permits before that happens: an
// error carrying "id": null, best-effort, and then nothing (ADR 0031, A).
//
// The envelope is closed and a payload is open. Unknown members on the envelope
// are refused; params, result, and data are json.RawMessage and are never
// decoded here. That is §10.2's compatibility rule expressed as a type rather
// than as a rule to remember, and it is also what keeps the operations array
// inside host.schema.apply byte-identical to the array that is hashed.
//
// Validate is the shape check both sides run. Error.Validate is §7.1's slug
// requirement, which only the writer runs: a peer's error with no slug still
// reaches the operator carrying its message, where refusing it would replace
// "your migration failed: boom" with "the runtime sent a malformed message"
// (ADR 0031, E).
//
// # What this package does not do
//
// It frames and it validates shapes. It correlates nothing. §2.5's rules -- each
// side numbers its own requests, and a pending table is per direction rather
// than shared -- belong to whoever owns a read loop: M6-T3 for the host side,
// M6-T5 for the engine's. An implementation that keeps one table for both
// directions will mismatch responses, and nothing here can stop it.
//
// It also answers no method: an unknown method on a request decodes cleanly, so
// that a dispatcher can return -32601 and the session can continue. That is what
// makes adding a method a compatible change.
//
// Status: the specification and its names (M6-T1); the codec and framing
// (M6-T2). The host callback server is M6-T3 and plan-mode recording M6-T4.
package protocol
