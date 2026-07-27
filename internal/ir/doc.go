// Package ir defines the versioned Operation IR: the database- and
// language-neutral representation that language SDKs emit and database drivers
// consume. Nothing else crosses that boundary.
//
// Every IR document carries an irVersion. Adding an operation is a five-part
// change — type plus round-trip test, a Compile implementation in every
// registered driver (or an explicit Unsupported capability), a golden file per
// driver, a defined reverse operation, and a docs/ir.md entry. See CLAUDE.md
// invariant #4.
//
// Status: scaffolding. Implemented in M1-T2 (see TASKS.md).
package ir
