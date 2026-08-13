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
// Status: the specification and its names (M6-T1). The codec is M6-T2, the host
// callback server M6-T3, and plan-mode recording M6-T4.
package protocol
