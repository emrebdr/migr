// Package ir defines the versioned Operation IR: the database- and language-neutral
// representation that language SDKs emit and database drivers consume. Nothing else
// crosses that boundary.
//
// Adding an operation is a five-part change — type plus round-trip test, a Compile
// implementation in every registered driver (or an explicit Unsupported capability), a
// golden file per driver, a defined reverse operation, and a docs/ir.md entry. See
// CLAUDE.md invariant #4.
//
// Status: the v1 operation set is complete (M1-T2). The recorder is M1-T3, the irVersion
// envelope M1-T4, reverse operations M1-T5.
//
// # Document shape
//
// An operation is a flat JSON object whose "op" field names it and comes first:
//
//	{"op":"add_column","table":"users","column":{"name":"email","type":"string(255)","nullable":false}}
//
// The discriminator is not a field on any operation type. It is written by the type's
// MarshalJSON and stripped before decoding, so the tag and the Go type cannot disagree.
// Operation is a sealed interface — only this package implements it — which is what makes
// a switch over the set genuinely total rather than merely exhaustive-looking.
//
// Each schema fact has exactly one spelling. AddIndex is a plain index and AddUnique is a
// unique constraint; there is no unique flag on AddIndex that would let one fact be
// written two ways. CreateTable carries its primary key because a key is intrinsic to the
// table and inlining it avoids a second lock event, but every other constraint is its own
// operation. See docs/adr/0006-ir-operation-shape.md.
//
// # Encode with Marshal, not encoding/json
//
// encoding/json escapes <, > and & as <, > and & by default, and does so
// even to the output of a MarshalJSON method. JavaScript's JSON.stringify does not. A
// check expression such as "total >= 0" would therefore serialize to different bytes in
// the Go and TypeScript SDKs for the same logical migration — breaking the
// byte-identical-IR guarantee of ADR 0003, and changing the checksum with it. Marshal
// turns escaping off; use it for anything that reaches the wire, a golden file, or a hash.
//
// Unknown fields are rejected rather than ignored. ADR 0003 settled why: decoding what you
// understand and skipping the rest turns a version mismatch into a migration that reports
// success having done less than it said.
//
// # Validation here is structural only
//
// Validate answers whether a document is well formed — a drop_table with no table is not
// an operation. Policy lives elsewhere: destructive-operation gating, capability
// preflight, and reference integrity are all M10, against a driver and a live schema this
// package has no access to.
//
// # Destructiveness
//
// Every operation classifies itself, so adding one forces a verdict rather than leaving a
// gap in a table somewhere else. DropTable, DropColumn, DropView, and Raw are destructive;
// Raw because its effect is unknowable to the framework.
//
// AlterColumn is the honest hard case. Whether a type change narrows depends on the
// current type, which the operation does not carry, so it reports destructive whenever a
// type change is requested. That is conservative and sometimes wrong in the safe
// direction: over-reporting costs an --allow-destructive flag, under-reporting costs data.
// M11 introspection can compare against the live schema and refine it.
//
// # Where dialect leaks in
//
// AddCheck.Expression and CreateView.Query are engine SQL sitting in a layer whose
// invariant #3 says raw SQL lives only in driver packages. There is no v1 alternative — a
// neutral expression language is a milestone of its own, and both operations are in the
// CLAUDE.md v1 set. They do not translate between engines, and neither does Raw, which at
// least names the driver it targets so that running it elsewhere fails at validation
// rather than at execution.
package ir
