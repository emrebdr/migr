// Package history defines the migration history store contract, the record it holds, and the
// neutral operations that create the table it holds them in.
//
// There is no SQL here and there never will be. The history table is created through
// driver-compiled IR operations, because raw SQL lives only in driver packages (CLAUDE.md
// invariant #3), and this package is the one place the table's shape is written down — otherwise
// PostgreSQL, SQLite, and MySQL would each invent a different one.
//
// Status: contract complete (M2-T5), the first Store satisfying it in M3-T6 (PostgreSQL),
// reconciliation complete (M4-T3), the executor that writes through it in M4-T5, and the drift
// this fold decides reported as exit code 4 in M4-T7.
//
// # Two vocabularies, deliberately kept apart
//
// An Outcome is how one run ended: Applied, Failed, Dirty, or Resolved. It is a fact about a run,
// written once by the store method that recorded it.
//
// A State is what Reconcile resolves a migration to by comparing the records against the files on
// disk: applied, pending, missing, drifted, or dirty. It is derived, it is not stored, and it can
// change without anything being written.
//
// Both belong here and both would naturally be called State, so the run axis is Outcome and the
// derived one is State. Note that "applied" and "dirty" each appear on both — a migration whose
// runs leave it applied and whose file still hashes the same reconciles to applied, and the two
// senses agreeing is the ordinary case rather than a confusion.
//
// Outcome is several values rather than a bool because Failed and Dirty differ in what an operator
// must do next. A failed run left the schema unchanged and the next up may proceed; a dirty one
// did not, and M4-T9 requires the next up to refuse until it is explicitly resolved. Blocking is
// where that difference is named, so the engine asks rather than testing for Dirty itself, and
// Resolved is false there because it is the run that settles a dirty one.
//
// There is deliberately no State.Blocking. Blocking works on the run axis because a dirty run
// blocks every command identically; a state does not — drifted blocks up and down but not status,
// and missing blocks neither — so one bool would be three different answers. M4-T5, M4-T6 and
// M4-T9 each switch on State with no default instead, which is what makes exhaustive name every
// one of them the day a sixth state arrives -- and M13-T14's did: waiting, a dirty forward run whose
// shadow table is held in sync, which Read derives by reading the shadow rows beside the log.
//
// # The table is a log, not a state
//
// Every run appends a row, including rollbacks and failures. Nothing is updated and nothing is
// deleted, so migr history has an execution log to print (M5-T3) and a rollback does not erase
// the evidence that it happened. Current state is folded from the log by Reconcile, which is also
// what invariant #6's "never silently rewrite" asks for.
//
// # The fold replays the log; it does not read the last row
//
// A run's meaning depends on the runs before it. MarkFailed records a run that failed without
// changing the schema, which is what makes "a failed rollback leaves the migration applied" true —
// but only if it was applied. So up/applied · down/applied · down/failed reads as applied under
// any rule that consults the last row alone, and the migration is pending. The primitive is
// per-run and stateful:
//
//	up   applied   the database now carries it, anchored on this run
//	up   failed    nothing changed
//	up   dirty     the schema is between states, anchored on this run
//	down applied   the changes were given back, anchored on this run
//	down failed    nothing changed
//	down dirty     the schema is between states, anchored on this run
//	up   resolved  an operator says the database carries it, anchored on this run
//	down resolved  an operator says it does not, anchored on this run
//
// The anchor is the run that put the migration in the state it is in, which is what a status
// carries and what drift is measured against. A failed run put it in no state, so it is not one.
//
// Both switches are total and carry no default, so exhaustive breaks the build the day another
// outcome or a third direction arrives -- which is exactly what happened when Resolved did (M9-T6).
// The trailing return each one needs is also the answer for a pair this build cannot interpret: it
// changes nothing. Such a record neither claims applied-ness nor revokes it, and no store can
// produce one — but NewLog is a hole a caller reaches directly, and a total function is cheaper
// than the argument.
//
// # Precedence, stated once
//
// dirty, then not-carried, then no-file, then a source digest that disagrees, then applied. Two
// consequences are decisions rather than fallthrough. A rolled-back migration that was then
// edited is pending rather than drifted, because invariant #6 protects applied migrations and one
// the database no longer carries is free to be edited. And a dirty migration whose file also
// changed reports dirty, because that is the condition blocking everything and its repair needs the
// recorded row; a Status carries both digests, so a renderer can note the drift alongside. M9-T6 is
// where that mattered: migr resolve reads the recorded row, and an assertion over an edited file
// settles the dirty state and leaves the drift to be reported on its own.
//
// Drift compares Checksum.Source and nothing else, with a plain comparison. A recorded
// digest of all zeroes decodes cleanly — checksum.Parse checks length, case and hex, and 128
// zeroes pass all three — so it reaches this fold, and skipping a digest that reports Zero would
// let a row describing nothing pass as agreement.
//
// # missing means the database still carries it
//
// A version with no file is reported only when the fold leaves the database holding something
// only a file could explain. A migration applied, rolled back, and then deleted emits no status:
// it is fully undone, its file's absence is expected, and migr history still prints its rows. The
// alarm missing exists to raise is that the database carries schema this repository can neither
// describe nor roll back, and one permanent unactionable row per migration ever deleted is how an
// operator learns to ignore it. A dirty run with no file reports dirty, since the schema being
// between states dictates the next action rather than the file being gone.
//
// # A missing history table is a value, not a caller's discipline
//
// ErrNoTable is part of this contract rather than a driver's, because a driver-neutral fold
// cannot reach into internal/driver — invariant #1 forbids it and it is an import cycle besides.
// Every Store owes it on every read path. Nothing checks that mechanically: the interface shape
// is unchanged, so a store that forgets still compiles, and M8 and M9 carry the obligation.
//
// Read maps ErrNoTable, and only ErrNoTable, to an absent Log. Log's fields are unexported, so
// Log{} is the only literal a caller can write and it means the safe thing; present-and-empty
// takes a deliberate NewLog(nil). Report.Resolved returns two values, which will not compile
// inside a range, so the check cannot be skipped by omission — and an absent log yields zero
// statuses rather than every migration pending. Under-reporting is the direction to fail in:
// M3-T6 found that reading a missing table as no runs makes a fully migrated production database
// look virgin, and the next up re-runs everything it had already applied.
//
// Read takes a Reader — List alone — rather than a Store. That states invariant #6 as a signature:
// reconciliation cannot write, cannot Ensure, and cannot repair. It also makes a test fake one
// method rather than six.
//
// # Order in, order out
//
// Discovered migrations keep the order the caller gave them, because that is migration.Order's
// sequence and therefore what migr up will run; re-sorting here would show an order migr will not
// follow and would discard the dependency overlay (M4-T2). Versions no file describes are
// appended, sorted among themselves: the file sequence is not version-sorted, so there is no
// position to merge them into, and nothing will run them anyway.
//
// Determinism (invariant #8) is covered twice. The file loop walks the given slice and looks up in
// a map; the recorded-only versions are collected by walking the runs, which NewLog already
// ordered, so the result is fixed before the sort runs. There is exactly one place a map could be
// iterated and it is not iterated.
//
// Reconcile emits two statuses when two files claim one version. Discover already refuses that,
// and an error here would be a second definition of a guarantee that has one; dropping one
// silently would be worse than showing both.
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
// M4-T5 settled what those numbers mean. A batch is one more than that database's own last recorded
// run, read through Latest, so a batch belongs to one database: no lock spans two, migr down
// reverses one database's last batch, and a shared number could name a batch that database never
// used. An ordinal numbers the runs an invocation actually performed rather than the migrations it
// considered, so a queue that skipped two already-applied migrations still starts at one.
//
// A rollback is its own invocation and takes its own batch, so its rows cannot collide with the ones
// they undo. The order it undoes in is byRun reflected — (batch, ordinal) descending, read from the
// run that applied each migration — which is why this key rather than the clock is what a rollback
// can be built on at all (M4-T6, engine.newestFirst).
//
// Which batch migr down acts on is a third question, and Latest cannot answer it. Latest has no
// WHERE clause: a down row, a failed row and a dirty row each count as the most recent run, which is
// correct for allocating the next batch and wrong for selecting one to undo. The batch a bare
// rollback acts on is the highest Batch among the anchor runs of the migrations the database still
// carries — Status.Recorded, which is also the batch migr status prints (M4-T8).
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
// index that outcome needs, and MarkResolved writes Resolved and takes the operator's reason. A caller
// therefore cannot record a failure marked applied, cannot mark a run dirty without saying where it
// stopped, and cannot assert a migration settled without saying why.
//
// Resolved is the fourth outcome and arrived in M9-T6. It is a row an operator asked for about a
// migration a dirty run left between states: they finished it or undid it themselves, and this records
// which. The fold reads it exactly as Applied — carried going up, given back going down — so what it
// changes is not the state but what the history says produced it: nothing here claims migr ran
// statements a person ran. A resume records Applied instead, because migr did run them. The reason
// lives in the error column, which is why Validate's "an error is required" covers this outcome too,
// and the column being string(16) rather than an enum is why a fourth value changed no table.
// An older migr meeting one reports ErrCorrupt, which is this package's rule for a recorded value a
// build cannot read working as designed.
//
// Record is called inside the migration's transaction and MarkFailed after its rollback, which is
// the only order either can have: the transaction being discarded cannot carry a row describing
// itself, and PostgreSQL refuses every statement in an aborted one until it ends (M4-T5). MarkDirty
// is written on that same path since M4-T9, chosen over MarkFailed when the failing run's statements
// outlived the failure rather than when the driver declares no transactional DDL — a standalone run
// and an earlier committed one both survive on a driver that declares it has.
//
// M4-T6 is what first writes a down row, and what first produces the case this package's own fold
// was written for: a failed down leaves the fold unchanged, so "a failed rollback leaves the
// migration applied" stopped being a claim and became a path. A down row's Checksum.IR covers the
// inverse operations that run emitted, so one version's rows can carry two different IR digests and
// nothing may compare them to each other. Outcome, Error, and LastStatement are
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
// The cause a write method is given is what stopped the run, not a description of the state it left.
// Nothing here can check that, and until M9-T5 the engine handed MarkDirty the paragraph describing
// the window: the error column held ten lines of it, including advice about resolving the state and a
// statement index already sitting in the column beside it. A row is read by migr history, by
// migr status --json and by the refusal the next run gets, so a presentation stored inside it is a
// second copy to keep in agreement.
//
// # Ensure cannot be idempotent through IR alone
//
// Schema returns the operations that create the table, and ir.CreateTable has no IfNotExists.
// A driver therefore checks existence with its own internal query — which the driver contract
// already allows — and compiles Schema only when the table is absent. Adding IfNotExists to the
// IR would be a wire-format change of its own, and the history table is a poor reason for
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
// parse being the case the checksum format anticipated. It unwraps to both ErrCorrupt and the underlying
// cause, so a caller can act on "this row is unreadable" and still say which part of it was.
//
// It is deliberately not a checksum mismatch. Drift is exit code 4 and means someone edited an
// applied migration; an unreadable row means this history table needs manual repair. Reporting
// the second as the first is how operators learn to ignore exit code 4.
//
// There is consequently no corrupt State. A store returns no records alongside the error, so the
// fold never sees such a row, and a state nothing can produce is a state that rots. Read passes
// the store's error through unwrapped, so errors.Is finds ErrCorrupt and the cause under it — and
// so a corrupt row is not read as an absent table either, which would be a different wrong answer
// and would invite the next up to re-run everything.
//
// # A store decodes on read; it does not validate
//
// Validate is a write-side precondition, and a store must not apply it to a row it read back.
// Every driver has to agree on that, or migr status answers differently per engine — which is
// the framework's central claim rather than a driver's detail.
//
// CorruptError covers a value this build cannot decode: a checksum that will not parse, a
// direction or outcome naming something this build does not know. It does not cover a row that
// decodes cleanly and would fail Validate. A newer migr writing a fourth outcome, or an
// engine_version this build never emitted, or a batch someone repaired by hand, must still read
// back — migr history is the command an operator runs to inspect exactly that table, and a store
// that refuses to read it has removed the tool they were reaching for. ir_version and
// engine_version are provenance about the build that wrote the row and are never checked on read.
//
// A store selects its columns by name rather than all of them, for the same reason: a table a
// newer migr extended must stay readable, and a positional read would shift its scan targets
// silently rather than ignore what it does not know.
//
// # Import direction, now enforced by the thing it warned about
//
// This package must never import internal/driver. That is a review rule,
// noting that depguard cannot catch it — its deny prefix deliberately leaves the contract package
// importable so that core can depend on it.
//
// Adding History to Conn closed that gap without adding anything. internal/driver now imports this
// package, so importing it back is an import cycle the compiler refuses, and every package under
// internal/driver/ reaches the contract too, so every path from here into that subtree is the same
// cycle — with depguard's prefix deny covering the subtree besides. The rule stopped being
// review-enforced the moment the contract named Store.
//
// # The one table that is not a log
//
// A chunked data operation's cursor moves with every chunk, so it can be neither a row per chunk,
// which would bury the log, nor an edited row, which would make it not one (M12-T3).
// Checkpointer is a store's second table, <history table>_progress, created from ProgressSchema by
// Ensure: one row per migration and direction saying how far a run in progress got, written in the
// transaction of the chunk it describes and deleted in the one that appends the applied row. It is
// where a run is, never what happened, and Reconcile never reads it -- the dirty row is what the
// fold sees, and a checkpoint is what carries that run on.
//
// # The deploy a batch ran under
//
// A batch is one invocation and says nothing about which deploy it belonged to, which is what a step
// of a change across deploys is judged by (M13-T7). Releaser is a store's third table,
// <history table>_releases, created from ReleaseSchema by Ensure: one row per batch naming the release
// the operator gave it, written in the transaction of that batch's first applied row, so a batch that
// applied nothing names none. Like the checkpoint table it is beside the log rather than in it -- the
// history table has no schema evolution, and adding a column to every history ever created is the
// cost a table of its own avoids. A database without it reads as having recorded no releases.
//
// # Where a shadow-table migration stands
//
// Shadows is a store's fourth table, <history table>_shadows, created from ShadowSchema by Ensure
// (M13-T9): one row per shadow-table migration naming its table, its copy, the name the
// original retires under, its capture resource, its stage, how far the copy got and the log position
// every change before it was applied from. Like the checkpoint table it says where a run is and the
// fold never reads it. Its confirmed column is the one the engine reads and never writes -- SaveShadow
// leaves it as it is -- so a confirmation written from another process survives every save. Owned
// and OwnedSchema list all four tables, which every Introspect excludes.
package history
