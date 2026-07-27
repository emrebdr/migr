// Package driver defines the database driver contract and the name-to-
// constructor registry.
//
// A driver opens connections, declares its capabilities, compiles IR operations
// into engine-specific statements, introspects live schemas, acquires the
// migration lock, and provides a history store. Adding a database must not
// require changing anything outside its own package.
//
// Two rules govern this subtree. Drivers never import one another — shared
// logic moves up into this package. And raw SQL appears nowhere else in the
// codebase; it lives only in driver implementations (CLAUDE.md invariants #2
// and #3).
//
// Status: scaffolding. The Driver, Conn, Compiler, and Introspector interfaces
// and the registry land in M2-T1 (see TASKS.md).
package driver
