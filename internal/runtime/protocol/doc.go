// Package protocol implements the wire protocol between the engine and a
// language runtime: line-delimited JSON-RPC 2.0 over the child process's stdin
// and stdout, with stderr passed through as log output.
//
// The engine calls initialize, migration.discover, migration.execute,
// migration.validate, and shutdown. The runtime calls back with
// host.schema.apply, host.db.query, host.db.exec, host.capability.get,
// host.info, and host.log. host.db.query is enforced read-only at the host, not
// merely by convention.
//
// Plan mode is the design lever: mutating callbacks are recorded rather than
// executed while read queries still run live, so dry-run, diff, and golden
// tests exercise exactly the same code path as a real apply.
//
// Changes here are versioned and require updating docs/protocol.md and the
// conformance suite for every SDK (CLAUDE.md invariant #5).
//
// Status: scaffolding. Implemented in M6-T2 through M6-T4 (see TASKS.md).
package protocol
