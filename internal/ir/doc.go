// Package ir defines the versioned Operation IR: the database- and language-neutral
// representation that language SDKs emit and database drivers consume. Nothing else
// crosses that boundary.
//
// Adding an operation is a five-part change — type plus round-trip test, a Compile
// implementation in every registered driver (or an explicit Unsupported capability), a
// golden file per driver, a defined reverse operation, and a docs/ir.md entry. See
// CLAUDE.md invariant #4.
//
// Status: the v1 operation set (M1-T2) and the recorder (M1-T3) are complete. The irVersion
// envelope is M1-T4, reverse operations M1-T5.
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
// # Recording
//
// A Recorder collects the operations of one migration execution, in arrival order. It is what
// the host callback server writes into when a migration running in another process calls
// host.schema.apply, and it is the single place operations accumulate in both plan and apply
// mode — which is what lets those two modes be provably identical rather than merely similar.
//
// Seal closes it and returns a Recording. Downstream signatures take *Recording, so the
// compiler enforces that a list handed to the planner is closed, rather than a review comment.
// An empty recording is legal: a migration whose Down records nothing still ran.
//
// Immutability is enforced rather than asserted. Operations hold slices, so a shallow copy
// would let a caller reach into a sealed recording through CreateTable.Columns. Append and the
// read accessors clone each operation through its canonical form instead. That reuses the
// round-trip this package already proves exact, and buys a second property: an operation that
// could not be serialized is rejected at Append, with its call site attached, rather than
// failing later in a compile step that has lost the context.
//
// # Attribution stays out of the document
//
// Each entry carries where it came from — file, line, and optionally function — supplied by the
// SDK over the protocol, since the migration runs in a different process. The migration ID and
// direction are held once by the recording, because migration.execute already names them once.
//
// None of it reaches the wire. A call site is a machine-specific path: in the IR it would make
// a migration hash differently on CI than on a laptop, breaking checksum stability, and the Go
// and TypeScript SDKs report call sites differently for the same migration, breaking the
// byte-identical-IR guarantee of ADR 0003. A Recording therefore marshals to exactly the
// operations array, and attribution serves error messages and plan output only.
//
// OpError is how CLAUDE.md invariant #9 — every user-facing error names the migration and the
// offending operation — becomes mechanical. Recording.Wrap attaches the full context to a
// failure discovered later by a driver or the executor, so no caller has to reassemble it.
//
// # Concurrency
//
// The recorder is mutex-guarded and race-free. The contract is narrower than the
// implementation: JSON-RPC over stdio is line-delimited and sequential, so operations arrive in
// one order, and the lock exists so a misbehaving runtime cannot corrupt state — not so callers
// may append in parallel. Invariant #8 requires deterministic planning paths, and parallel
// appends would reorder IR between runs and move the checksum with it.
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
