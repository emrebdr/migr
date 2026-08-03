// Package ir defines the versioned Operation IR: the database- and language-neutral
// representation that language SDKs emit and database drivers consume. Nothing else
// crosses that boundary.
//
// Adding an operation is a five-part change — type plus round-trip test, a Compile
// implementation in every registered driver (or an explicit Unsupported capability), a
// golden file per driver, a defined reverse operation, and a docs/ir.md entry. See
// CLAUDE.md invariant #4.
//
// Status: complete for irVersion 1 — the operation set (M1-T2), the recorder (M1-T3), the
// versioned envelope (M1-T4), reverse operations (M1-T5), and the specification (M1-T7).
//
// # The wire format is specified in docs/ir.md
//
// That document, not this one, is what a third-party SDK implements against. Its JSON examples
// are read by spec_test.go and re-encoded through this package, so every one of them is a
// conformance vector and none of them can drift. The same test asserts that every operation has
// a section, which is what makes the fifth part of CLAUDE.md invariant #4 mechanical.
//
// Note what that makes true of this package: after M1-T6, the bytes it emits are a compatibility
// surface. Reordering a struct field, adding one without omitempty, or changing how a logical
// type renders changes the checksum of every applied migration in every installation.
//
// # The versioned document
//
// A Document is the unit an SDK emits and the engine reads:
//
//	{"irVersion":1,"operations":[{"op":"create_table","table":"users",...}]}
//
// A Recording marshals to one, so nothing can emit operations without a version attached, and
// an unstamped Document refuses to marshal rather than writing "irVersion":0. Nothing else goes
// in the envelope: the migration ID and direction are already named by migration.execute and
// held by the Recording, and duplicating them here would create fields that can disagree with
// their own container.
//
// UnmarshalDocument checks the version *before* decoding operations, and the ordering is the
// point. A document from a future version very likely contains an operation this build has
// never heard of; decoding first would report that unknown operation, which is true, useless,
// and points at the wrong problem. Version-first reports an unreadable irVersion, which tells
// the operator to upgrade the CLI. A missing irVersion is its own error and is never defaulted
// to 1 — assuming a version is the lenient behavior ADR 0003 rejected, because it is how a
// mismatch becomes a migration that reports success having done less than it said.
//
// irVersion is the one camelCase key in an otherwise snake_case format. CLAUDE.md §4.1,
// invariant #5, and ADR 0003 all name it that way, and ADRs are immutable records, so renaming
// it would leave a committed document describing a key that does not exist. The inconsistency
// is deliberate and documented rather than quietly corrected.
//
// # The checksum boundary
//
// Operations() is the hashable unit; the envelope sits outside it. ADR 0003 recorded, without
// being able to settle, that an irVersion bump would change every applied migration's checksum
// and fire drift detection across every installation. Keeping the version out of the hashed
// bytes narrows that to bumps which genuinely reshape operations — where the checksum *should*
// change, because the emitted IR really did. See docs/adr/0007-ir-document-envelope.md.
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
// # Reversal
//
// Reverse sits on the interface beside Destructive, so a new operation cannot compile without
// answering what undoes it. It returns either an operation or an *IrreversibleError carrying a
// reason — never a bare "irreversible", which tells an operator nothing about whether the
// situation was avoidable.
//
// Every add or create has a matching drop. M1-T5 found the original set had none for unique,
// check, or primary key, which would have made adding a unique constraint permanently
// un-rollbackable and blocked every migration batched with it.
//
// Operations that destroy information they do not otherwise record carry an optional From
// payload holding what they replaced. Supplied, they invert exactly; absent, they are
// irreversible with a reason naming the field that would fix it. A schema differ knows both
// sides of a change and can always populate it; a hand-written migration often cannot, and then
// says so rather than guessing. Note that From is trust rather than proof — nothing yet verifies
// a recorded prior definition against the live schema.
//
// Two cases are easy to get subtly wrong. Reversing "set a default" must restore "no default" as
// an explicit DropDefault, because an omitted field means "leave the default alone" and would
// leave the new one in place after rollback. And create_view with OrReplace does not assume the
// view was new: it may have overwritten an existing definition, so it inverts to a restoration
// when the prior query was recorded and refuses otherwise.
//
// ColumnState carries NoDefault so that "the column had no default" is sayable and distinct from
// "nothing was recorded about defaults". Before it existed, the first was expressible only by
// recording an unrelated unchanged field as padding, and the inverse inferred the absence from a
// missing one — which drops a default the migration never recorded whenever the payload is merely
// silent. It also makes alter_column an involution: reversing a fully recorded one twice returns
// the original, which no other operation can claim, because it is the only one whose inverse
// records its own prior state. See docs/adr/0010-recorded-absence-of-a-default.md.
//
// ReverseAll inverts a list and reverses its order, but only if every operation in it can be
// inverted. A partial reversal that stops halfway is worse than refusing to start.
//
// internal/engine is what calls it, and only for a migration whose source authored no down of its
// own — an authored one beats a derived one, which is what invariant #10 promises. Failing whole is
// what lets a rollback refuse before undoing anything (M4-T6, ADR 0022).
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
