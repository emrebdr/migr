// Package engine orchestrates migration execution: locking, transactions, apply and rollback, and
// batch semantics.
//
// Deciding what order migrations run in is not here. Finding them, sorting them by version, and
// overlaying the dependencies they declare is internal/migration, which imports stdlib and nothing
// else — so the CLI can list and validate migrations without reaching through an executor that
// opens databases (M4-T1, M4-T2). Turning operations into statements is internal/plan, which needs
// no database either. What arrives here is a sequence and a way to compile it.
//
// Status: both queues are complete — apply in M4-T5 and rollback in M4-T6 — with drift reporting in
// M4-T7, narrowing in M4-T8, the dirty-state model in M4-T9, and previewing in M5-T2. The
// transaction opt-out is completed in M13-T4. The live-schema gate arrived with M11-T2: restorable.go
// reads a snapshot before each half runs and refuses a migration whose record of the schema this
// database contradicts, which is where ADR 0008's from trust problem closed. See
// docs/adr/0069-per-driver-introspection.md, docs/adr/0021-apply-executor.md,
// docs/adr/0022-rollback-executor.md, docs/adr/0023-batch-narrowing.md,
// docs/adr/0024-dirty-runs.md and docs/adr/0026-dry-run-and-sql-sources.md. A data-only migration
// runs in chunks and carries on after an interruption as of M12-T3 (docs/adr/0078), and a statement
// that gave up on a lock is tried again after a backoff as of M13-T2 (docs/adr/0082), under two
// bounds stopped on the server as of M13-T3 (docs/adr/0083).
//
// # A preview reads what an apply ensures
//
// Preview is the third entry point, and it runs the same pipeline the other two do — reconcile,
// classify, emit, compile — stopping before the first statement. Only the last step differs, so a
// dry run and the run it describes cannot drift apart into two answers.
//
// It does not Ensure. Creating the history table is a write, and a command whose entire promise is
// that it makes none may not make that one either. So it is the only caller for which a database
// with no history table is an answer rather than a bug: resolved refuses that report because an
// apply has just ensured one, while a preview folds against an empty-but-present log and reports
// Recorded: false beside the queue. That is not ADR 0019's mistake, which is reading absent as
// empty *silently* — a preview says so in the first line of its output and applies nothing either
// way.
//
// It does take the lock, because it reconciles, and reconciling outside the lock reports on a
// database another run is still changing. A dry run can therefore exit 3, which is the cost of the
// answer being true at the moment it is printed.
//
// # What Apply is given, and why the operations arrive late
//
// Options carries the migrations already discovered and hashed, a Planner to compile them, a Source
// to ask what each one does, a clock, and the runtime name every history row records.
//
// Source is asked only about migrations that will actually run, and only once the lock is held.
// That is not an optimization. ADR 0009 permits a migration to branch on live query results, so
// operations emitted before the lock are emitted against a database another run may still change;
// and M6 spawns a runtime process per migration, so emitting for a migration that will be skipped
// is a subprocess of side effects for an answer nothing reads.
//
// The cost is real and is stated rather than hidden: a migration that will not load is discovered
// only after the ones before it have applied. migr plan emits and compiles the whole queue with no
// database, which is where that is caught, and is why internal/plan opens none.
//
// # The order is the design
//
//	check Options      no database; a clock found missing after the lock is a lock held for nothing
//	Lock               deferred release, on a context that outlives whatever canceled the run
//	Ensure             after the lock, before any transaction
//	Read               after Ensure
//	Reconcile          pure
//	classify           switch on history.State, no default; refusals fire before any statement
//	narrow             a prefix of what survived, before the ordinals are assigned
//	Latest             only when something will run
//	per migration      Emit, hash, compile, group, refuse, run, record inside the last transaction
//	unlock             deferred, so it runs after the rollback rather than before it
//
// A rollback runs the same steps in the same order with one difference, and it is the difference
// this executor exists to make: the whole queue is emitted, inverted, compiled and refusal-checked
// before the first statement of any of it runs, and only then is a batch allocated.
//
//	classifyDown       applied runs, pending is skipped, missing refuses along with drifted and dirty
//	newestFirst        then narrow, since "the last two applied" needs the run order to mean anything
//	invert             every migration: Emit down, derive where none was authored, compile, refuse
//	Latest             after invert, so a refused rollback allocates no batch at all
//	per migration      run, record inside the last transaction
//
// Lock before Ensure, because two runs racing between the existence check and the CREATE TABLE
// collide on a catalog index, and the advisory key is derived from schema and table names and so
// needs no table to exist.
//
// Lock before Read, because the state a run acts on must be the state it acts under. Reading first
// leaves a window in which another run applies migrations and this one then executes a stale queue
// against a stale batch — which the (batch, ordinal) key would catch as a failure partway through,
// not as a correct answer.
//
// Ensure before Read, and this one is invisible in the signatures. history.Read maps ErrNoTable to
// an absent log and reconciling an absent log yields zero statuses by design (ADR 0019), so reading
// first reports "nothing to do" on every fresh database, forever.
//
// Ensure outside any transaction, which is invisible too: a store may open one only when the
// session has none, so a table ensured lazily inside a migration's transaction is created in it and
// the first rollback drops the table along with the row just written into it.
//
// The lock is released last. PostgreSQL refuses every command in an aborted transaction, the one
// giving an advisory lock back included, so a release that ran before the rollback would turn a
// failed migration into a failed release as well — and the second failure is the one an operator
// reads.
//
// # Why an apply is lazy and a rollback is not
//
// An apply emits one migration at a time, because migration 5's operations may depend on the column
// migration 4 just created — emitted up front, 5 would be emitted against a database where 4 has not
// run. The stated cost is that a migration which will not load is found only after the ones before
// it applied (ADR 0021).
//
// A rollback cannot pay that cost. It runs newest first, so an irreversible migration at position
// three would be met only after five and four had been undone, leaving a database carrying
// migrations whose successors are gone — a schema nothing describes. Irreversibility is a property
// of the migration rather than of the run, so it is knowable before anything happens, and the whole
// queue is therefore inverted before any of it runs. ir.ReverseAll refuses whole for the same reason
// one level down: a partial reversal is worse than none (ADR 0008).
//
// The mirror-image price is real and is not hidden: a down is emitted while the migrations after it
// are still applied, rather than against the state it will run against. No shape avoids both.
//
// # A migration that authors no down
//
// The source is asked for the down direction first, because a down its author wrote beats one this
// framework guessed at — that is what invariant #10 promises. A source that reports
// ErrNoDownAuthored gets its up operations asked for and inverted with ir.ReverseAll.
//
// Deriving here rather than in each SDK is the argument that put grouping here rather than in each
// driver: three SDKs must not reach three different inverses for one migration. The sentinel lives
// beside the interface that consumes it, which is history.ErrNoTable's precedent, and every source
// owes it.
//
// The hazard is stated rather than hidden. Deriving re-emits a migration's up against a database
// where it is already applied, and ADR 0009 permits a migration to branch on live query results, so
// one that emits differently the second time yields an inverse of something that never ran. The
// history records only a digest, so nothing can catch it after the fact. Verifying an inverse
// against the live schema is what closed it (M11-T2): restorable.go reads a snapshot immediately
// before each half runs, and an inverse of something that never ran lands there as an operation
// naming an object that is already present or already gone. Authoring a down avoids it entirely.
//
// That re-emission is asked for as a question, and it is the reason Mode exists.
//
// # A run and a question are asked for differently
//
// Emit carries a Mode. Apply and Rollback ask in ModeApply; Preview and internal/validate ask in
// ModePlan; and derive asks in ModePlan whatever the invocation is, so one rollback issues both —
// the down as the run it is, then the up as the question its inverse comes from. A migration
// reached through a language runtime can write directly, through host.db.exec, and plan mode is
// what holds that statement rather than running it (docs/protocol.md §6, ADR 0033).
//
// Nothing observes the difference yet: internal/source/sql reads a text file and cannot branch,
// which is why it ignores the parameter and a test asserts it emits the same operations either
// way. The day a Go migration is previewed, asking it as a run is how migr plan performs the side
// effects it was invoked to avoid.
//
// The engine owns the distinction rather than borrowing internal/runtime/protocol's spelling of it,
// because it exists with no runtime in sight: migr plan over .sql migrations is plan mode, and migr
// validate opens no database at all. The two spellings are held level by a test in that package.
//
// # The order a rollback undoes in is the order the runs happened
//
// Not version order: a declared dependency may point backwards in time, which ADR 0018 makes the use
// case rather than a mistake, so a later-timestamped migration can have run first. Not the reverse
// of the reconciled report either: that is the order the current files would run in, and a
// dependency edge added after both applied makes it disagree with what happened.
//
// The order is (Batch, Ordinal) descending, read from the run that applied each migration — the
// exact inverse of history.byRun. That pair is the history table's primary key, so no two runs can
// tie and the order is total.
//
// # Narrowing is a prefix of the order the run is already in
//
// Scope is exactly one of All, Steps, To, or LastBatch, and its zero value is All — so options
// nobody narrowed behave as they did before the field existed. All four select a leading run of the
// queue; they differ only in how that run ends.
//
// Nothing here compares two versions, and that is the point. Going up the sequence is
// migration.Order's, where ADR 0018 makes a backwards-pointing declared dependency the use case;
// going down it is (Batch, Ordinal) descending, which ADR 0022 chose over version-descending by
// name. A prefix of either is meaningful and a version comparison over either is wrong — and wrong
// in a way that every version-ordered fixture would agree with.
//
// --to names the state the database should end in rather than the last migration acted on, so
// up --to X applies through X and down --to X leaves X applied. Going up the cut is taken over the
// report rather than over the queue, because X may already be applied and a hotfix inserted behind
// it must still run. Going down the queue has been re-sorted, so the cut is taken there and a
// version the database does not carry is refused rather than guessed at.
//
// LastBatch reads the highest recorded.Batch among the applied queue, which is the anchor up run of
// each migration the database still carries. It deliberately does not read Latest: that is
// direction- and outcome-blind, so after one rollback the highest batch in the table is the
// rollback's own, and a second migr down would report nothing to do while every earlier batch stayed
// unreachable. LastBatch is a rollback's alone — a pending migration has never run and belongs to no
// batch — and an apply given it is refused before the lock.
//
// Narrowing happens after the state switch, so a drifted, dirty or missing migration refuses the
// whole run even when the scope would never have reached it. That is deliberate: those are
// properties of the reconciled database rather than of one invocation's appetite. It over-refuses
// for missing, which ADR 0023 records rather than hides.
//
// migr down with no arguments rolls back the last batch. The zero Scope is not that, because a zero
// value meaning something different per direction is one nobody can read at a call site — so M5-T2's
// down command passes LastBatch() when neither flag is given, and that line is the last piece of
// M4-T8's done-when.
//
// # A batch belongs to one database
//
// A batch is one migr invocation against one database, and it is one more than that database's own
// last recorded run. No lock spans two databases, so a shared counter has no allocator; migr down
// reverses one database's last batch, and a shared number can name a batch that database never
// used. Two databases in one migr.yaml therefore get unrelated numbers, and migr up loops.
//
// It is read from Latest rather than from the log already in hand, because the batch is a decision
// about writing while the log was read for reconciling — and M4-T8 now filters that log by --step
// and --to, at which point a maximum taken from it is the maximum of a filtered slice. Under the
// lock the two cannot disagree.
//
// Ordinals number the runs rather than the discovered migrations, from one, so a batch numbers what
// it actually did and a rollback steps through no gaps.
//
// # This package may not read a clock
//
// forbidigo bans time.Now everywhere outside cmd/ and internal/cli/, tests included, and a history
// row must carry a non-zero AppliedAt and a non-negative Duration. The clock is therefore an
// Options field with no default, because there is nothing here to default it to, and a run given
// none is refused before it takes the lock.
//
// It is read once before a migration's first statement and once inside the transaction just before
// the row is written, so the recorded duration excludes the commit. There is no alternative: the
// value has to be in the row the commit makes durable. A clock that steps backwards is clamped to
// zero rather than allowed to fail a migration that worked, since Validate refuses a negative
// duration and it validates inside the transaction.
//
// # A migration runs as parts, split around what cannot share a transaction
//
// A statement may declare that it cannot run inside a transaction; CREATE INDEX CONCURRENTLY is why
// the flag exists, and a raw operation's no_transaction is how a migration says so (M13-T4, ADR
// 0084). The steps are divided into parts before anything runs: consecutive statements that may
// share a transaction are one part and run inside one, and a statement that may not is a part of its
// own and runs bare. A migration with no such statement is one part, which is exactly the one
// transaction M11-T7 made bracket the whole step loop, so nothing about it changed.
//
// Every part but the last commits on its own, so each one writes a dirty row saying how far the
// migration got -- inside its transaction where it has one, so the row commits with the statements,
// and straight after the statement where it has none. The last part writes the applied row, bare
// where it is a statement of that kind. Each row takes the next ordinal, as a chunked run's do. A
// crash between a bare statement and the row after it undercounts by that one statement; nothing
// records a statement that has no transaction atomically.
//
// An operation compiled as it runs belongs to a transactional part and is expanded inside that
// part's transaction, so what it reads is the parts before it committed and its own part's
// statements sent and not yet committed -- the schema the steps before it left. An expansion is
// still held to statements a transaction takes, as it arrives, which is the one moment they exist;
// a refusal there discards the part's transaction. ErrNothingToRun reads a statement list, so a plan
// holding only deferred operations is refused only where nothing is deferred either; otherwise every
// SQLite migration of one rebuild would be unrunnable (ADR 0071).
//
// What survives a failure is what the parts before it committed, and on an engine whose DDL commits
// also what the failing part ran before the statement that failed. A lock wait spent in a
// transactional part retries from where what survived ends, so a committed part never runs twice;
// one spent by a bare statement is not retried at all, since nothing rolled back what it left.
//
// The statement counter continues across a migration's steps and restarts for the next migration,
// because a position is a place in one migration and M9-T6 resumes from it.
//
// Grouping lives here and not in a driver, because it reads Statement's flag and nothing
// engine-specific. Three drivers must not reach three answers about the same statement list.
//
// # An emulated operation is compiled as the run reaches it
//
// An operation a driver performs only against a live database arrives as a deferred plan.Step
// (ADR 0053, ADR 0071). The run walks a migration's steps in order and expands each deferred one
// immediately before its statements execute, so the catalog it is rendered from is the one the steps
// ahead of it left.
//
// That is why the transaction brackets the whole step loop rather than one statement list. Inside a
// transaction, "the schema the steps before it left" means the statements they have already sent and
// not yet committed, and both expanders read through the session's open transaction already — so
// this needed no driver change. Compiling the whole migration first instead was the defect: measured
// on SQLite, an add_primary_key rendered before the alter_column ahead of it had run re-created the
// table without the NOT NULL that alter had just written, and a rollback of two emulated migrations
// re-created what the newer inverse had removed.
//
// ADR 0022's whole-queue inversion is unchanged and is about something else: irreversibility is a
// property of the migration, so it must be met before anything is undone. What moved is when a
// statement is rendered, not when a refusal is decided.
//
// Two things follow and are stated where they are read. A dirty run can no longer say how many
// statements the migration had in all, because the operations it never reached are rendered from a
// catalog it never read — so DirtyError carries how many were still to be compiled and renders its
// total as a partial one. And a resume is refused outright where any operation is compiled as it
// runs, for ADR 0062's own reason one layer in: the recorded index names a statement in the list
// that run emitted, and re-rendering an expansion here numbers a different list.
//
// M13-T4 therefore writes the multi-run loop rather than deleting a precondition, and inherits the
// question this creates: a deferred step's split points are not known until it is expanded, so an
// expansion after a split can no longer read the statements before it from inside one transaction.
//
// # What a run refuses, and why refusing is the honest answer
//
// Three conditions are refused before the lock, since all three are knowable with no database: a
// session that hands back no history store, and options missing a planner, a source, a clock or a
// runtime name.
//
// Three more are refused once the history has been read, before any migration runs:
//
//   - a history table still absent after Ensure reported success, because reading that as "no runs
//     yet" makes a fully migrated production database look virgin (ADR 0019);
//   - two entries claiming one version, which Discover already refuses but which arrives here as a
//     caller's slice;
//   - any migration reconciling to drifted or dirty (M4-T9). Both refuse the whole run rather than
//     the migration, including the ones queued ahead of it: running until the queue reaches the
//     problem leaves a partially migrated database for a condition that was knowable before the
//     first statement. Drift refuses a rollback for a stronger reason than it refuses an apply —
//     the undo about to run was authored by bytes that are not the ones that ran;
//   - a version the database carries that no file describes, on a rollback only. An apply skips it
//     because there is nothing to run for it; a rollback runs newest first, so it sits in front of
//     every migration older than it and stepping around it would undo them while it stays. The case
//     that decides the rule is migr down against a misconfigured migrations.dir: every applied
//     version reconciles to missing, and a skip rule reports nothing to undo against a fully
//     migrated database.
//
// The switch on history.State carries no default, so exhaustive names this package the day a sixth
// state arrives — M13-T14 already says it needs one. The trailing return is the answer for a state
// this build cannot interpret, and it refuses rather than guessing.
//
// Three are refused per migration, before its first statement, each naming the task that lifts it:
//
//   - one that compiles to no statements at all, because reporting success for a schema change that
//     never happened is the driver conformance suite's own failure, one layer up -- except a down
//     that emitted no operations, which is its author saying there is nothing to undo and is
//     recorded rolled back with none run (ADR 0079); a derived down never reaches that, being
//     refused where the up it would invert now emits nothing;
//   - one that groups into more than one run, because nothing here splits a migration around a
//     statement that cannot share a transaction;
//   - one whose single run is not transactional, because then there is no transaction for its
//     history row to be written inside.
//
// The last is the hole M3-T5 left: its refusal counted groups, so a migration that was entirely
// standalone statements passed it and was rejected by the driver instead. Both belong to M13-T4,
// which deletes two preconditions rather than rewriting an executor — for both directions at once,
// since an inverse groups independently of what it inverts and a migration that applied cleanly may
// have a down these refuse. Neither still refuses because partial progress could not be recorded;
// M4-T9 removed that reason, and a fourth refusal with it.
//
// On a rollback all three fire during invert, so they are batch-wide preconditions rather than
// per-migration ones. That is strictly stronger than the apply path, where they fire as the queue
// reaches each migration.
//
// # A run is dirty when its statements outlived the failure
//
// A failed run recorded as failed says the schema is unchanged and the next up may proceed. That is
// true only when something took the statements back, so the executor asks what did rather than
// asking what the driver declares:
//
//	the failing run was transactional and the session has transactional DDL   nothing survived
//	the failing run was standalone                                            it ran bare
//	the session has no transactional DDL                                      its DDL auto-committed
//	any run that completed before the one that failed                         already durable
//
// Only the third is what M4-T9's done-when names, and branching on capability.Set.TransactionalDDL
// alone would see only that one. The other two happen on PostgreSQL, which reports true — so that
// rule would record a half-changed schema as failed the day M13-T4 lands multi-run migrations, and
// tell the next up to proceed against it.
//
// Zero survivors is MarkFailed and one or more is MarkDirty, but that split is not a convention this
// package remembers: history.Record.Validate refuses a dirty row that names no statement and a
// non-dirty row that names one. The index recorded is the last statement that *succeeded*, which is
// one before StatementError.Position, because M9-T6 resumes from it and an off-by-one there replays
// a statement that already ran. A failure with no statement position at all — a commit, or the
// history write itself — happened after every statement in its run took effect, so the counter is
// the answer.
//
// A transactional group still goes through Transact on a session that reports no transactional DDL.
// The driver is the only thing that knows what a transaction means on its engine, and MySQL's DML is
// transactional even where its DDL is not. The honest consequence is that ADR 0013's
// record-before-commit buys nothing there: the first DDL statement implicitly commits, so the
// history row lands in a different transaction from the schema change it describes. That window is
// what a dirty row exists to describe rather than a defect in it. See docs/adr/0024-dirty-runs.md.
//
// The row records what stopped the run and the caller gets the window it opened. Those were one value
// until M9-T5: perform computed leftDirty once and passed the resulting *DirtyError as both, so the
// error column held the whole window paragraph — measured at 638 bytes over 10 lines against MySQL,
// advice included, with a statement index inside the column that sits beside last_statement. The
// failed path had always recorded the statement failure, so the two write paths disagreed. They now
// record the same kind of thing, and the join that reports both puts the window above the bookkeeping
// failure, because an operator acts on the first one they read.
//
// # What a dirty database is told when it is refused
//
// A migration that *is* dirty and a run that just *became* one are both ErrDirty, and the two are
// separated by type: *DirtyStateError is built from the row, *DirtyError from the run. Neither
// errors.As finds the other, which is what lets a caller tell "this database is between states" from
// "this invocation left it there" — and errors.Is(err, ErrDirty) still finds both, which is the
// obvious thing a caller writes.
//
// The refusal renders four labeled rows: what survived, what stopped it, what is not recorded, and
// where the row sits. It cannot say how many statements the run had, because the row records how far
// it got rather than how far it had to go — so it says that rather than leaving a reader to infer it.
// Nothing recomputes the total: the statements were the ones *that run* emitted, and a migration may
// branch on live query results (ADR 0009), so re-asking could legitimately produce a different list
// than the one that half-ran. See docs/adr/0061-refusing-a-dirty-database.md.
//
// # Settling it, which is the only way out
//
// Resolve is the fourth entry point and the only one that exists because invariant #6 permits a
// repair path. It finds the one migration between states and either runs what the stopped run never
// reached or records what an operator did about it, and DryRun stops it after the plan rather than
// putting a second pipeline beside the first (ADR 0026's rule).
//
// Resolution is an append. The fold already clears a dirty run when a later Applied or Resolved row
// lands, so nothing is edited and no recorded checksum is rewritten -- which is the only shape
// invariant #6 allows.
//
// A resume carries on in the direction the stopped run was going, asking for the undo by the same
// path a rollback does, and it refuses on two digests. The source half moving is drift (exit 4, and
// the message says "stopped partway" rather than "applied" because only one of the two can be
// resumed). The emitted operation list hashing differently is ErrNotResumable: the recorded index
// names a statement in the list *that run* emitted, so against a list that moved it names a different
// statement. A migration that branches on live data is therefore not resumable, and the refusal says
// so and names the two repairs that are. A migration holding an operation this engine compiles as it
// runs is not resumable either, for the same reason one layer in (ADR 0071).
//
// The skip is executor.from, read by exec as the statements it counts through and does not send, so a
// resume that stops again records an absolute index rather than one relative to itself. Where from
// equals the whole count -- a dirty row naming every statement, which ADR 0024 says means the commit
// or the history write failed -- nothing is executed and the Applied row the bookkeeping never got
// to is recorded instead.
//
// The two asserted repairs reach neither a planner nor a runtime: the row is built from the recorded
// one plus this session, so a migration that no longer builds can still be settled. They copy the
// *recorded* source digest rather than the file's own now, so an operator who edited the file gets the
// dirty state settled and the edit reported as drifted -- which is true, rather than laundered.
// See docs/adr/0062-resolving-a-dirty-migration.md.
//
// # What a drifted migration is told, and what it is not told
//
// DriftError is a block rather than a sentence, which every other refusal here is. Comparing two
// 64-character hex digests is the whole job of this one message, and two values a terminal wraps
// into one paragraph cannot be compared at all.
//
// It shows the recorded checksum in full — both halves, exactly as the history row holds it — and
// the recomputed source digest in full beside it. There is no recomputed IR half and none is
// invented: producing one needs a language runtime and a live database, and ADR 0009 permits a
// migration to branch on live query results, so a mismatch there would have three possible causes
// and only one of them would be drift. Rendering a placeholder inside a migr1: value would be a
// checksum no parser accepts, which is worse than an absence the message explains.
//
// Nothing here recomputes or re-compares anything. history.stateOf already decided this, on
// Checksum.Source alone, and this package adds no second comparison — a second one is how the two
// answers start to disagree. What M4-T7 added is the message and the exit code, not the check.
//
// # A failure aborts; it never undoes
//
// The migrations that committed stay committed, the rest are never asked what they do, and the
// error names the migration and the operation the failing statement was compiled from (invariant
// #9). Result is populated alongside that error, which is unusual in Go and is the point: without
// it an operator cannot see which migrations did commit.
//
// A rollback that fails partway is the same shape: what it undid stays undone, what it had not
// reached stays applied, and the migration it failed on stays applied too — a failed down leaves the
// fold unchanged, which is what internal/history means by "a failed rollback leaves the migration
// applied".
//
// A run that failed is recorded after its transaction has rolled back, on a context that outlives
// whatever canceled it, and the write is joined into the failure rather than replacing it.
// Reporting only the bookkeeping failure swaps the operator's problem for a diagnostic's; reporting
// only the migration's hides that migr history will not show the event. A failure that happens
// before the operations are known writes nothing, because a run whose operations were never
// produced is not a run and has no checksum to be recorded under.
//
// # What a run is permitted to destroy
//
// Destructive is the policy half of invariant #7 and lives here rather than in internal/validate,
// because that package already imports this one to read its refusals -- and the reverse import
// would be a cycle. An Allowance is what migr.yaml permits for the project plus what
// --allow-destructive permits for the run, and Destructive reports every operation in a list it
// does not cover, each with the object it destroys. Subject names that object and is total over the
// twenty kinds; the exhaustive linter does not read a type switch, so a test over
// drivertest.Operations is what holds it total.
//
// The gate is one call in forward, after the migration says what it does and before anything is
// compiled, and it reads the mode: an apply refuses, a preview does not. That is not a shortcut --
// migr plan and --dry-run share forward with migr up, and the command that reports what a run would
// do is the only place an operator can find out what to permit. They mark those operations and exit
// 0, which is the rule migr status already follows over drift while migr up exits 4.
//
// Two paths reach neither the gate nor forward, and both are deliberate. A rollback undoes what it
// was asked to undo -- gating it would make the permission mandatory for migr down outright, since
// the inverse of CreateTable is DropTable. And a resume finishes what a permitted run started, on a
// schema that is already half-changed; refusing there would leave a database between states with
// nothing but the asserted repairs to settle it. See ADR 0063.
//
// # What this server has declared it cannot perform
//
// Unsupported is the other half of the same shape, and the same two callers read it: a verdict
// resolved against the connected server is asked of every operation a migration emits, and one it
// refuses stops that migration with the driver's own reason, the operation's index, and the object
// it names. Nothing here resolves a verdict for itself -- Capabilities() is the session's, built
// inside Open against a server it detected (ADR 0012) -- so the check costs no I/O and needs no
// configuration at all. There is no flag and no opt-out: a refusal an operator can switch off is
// not one.
//
// capable sits beside permit in forward and again in reverse, and reads the mode the same way, so a
// preview marks what a run refuses. Where the two gates differ is direction: permission is asked of
// the up half alone, because the inverse of CreateTable is DropTable, while an operation the server
// will not perform is just as unrunnable backwards. A rollback inverts its whole queue before
// running any of it, so migr down is preflighted whole; an apply cannot be, because migration N may
// depend on N-1 having been applied and may branch on live query results (ADR 0009).
//
// A resume is not gated, for the reason the destructive gate is not: the statements it runs are the
// ones the stopped run reached rather than ones a verdict was asked about.
//
// What this buys is sharpest on an engine whose DDL commits. Measured on MySQL 8.0 before the gate
// existed: a migration whose second operation that server refuses applied the first, sent the
// second, took error 1674, and was recorded dirty -- which then refused both directions until migr
// resolve. See ADR 0064.
//
// # A step waits for a later release, and a run stops at it without failing
//
// Options.Gates is every step of a change across deploys that waits on another, resolved by
// migration.Gates, and Options.Release names the deploy this run belongs to (M13-T7, ADR 0087). Before
// a queued migration is asked anything, Gated judges it against the history as it stood before the
// run, advanced by what this run has applied since: it waits while its prior step is not applied, or
// belongs to this release, and the run then stops there with Result.Held and no error -- a change
// partway through its deploys is the normal state of one, and failing would fail every deploy until
// it finished. Everything after the step waits too, since order is kept. A preview stops at the same
// step and says so.
//
// A queue holding any step needs a release, ErrNoRelease before anything runs, so every step is
// recorded under one; the release is written by recordRelease in the transaction of the batch's first
// applied row. A prior step whose batch recorded none counts as an earlier release, which only a run
// before releases were recorded can leave. A rollback and a resume are never held. Where DDL commits
// as it goes, a hold at a swap or after one says the swap is two commits, read from TransactionalDDL
// rather than from a driver's name.
//
// # What an operation costs against the ceiling
//
// Exceeding is the third gate of that shape and the only one no run reads: it reports every step
// whose plan.Step.Lock is above safety.max_lock_impact, and migr plan and migr validate fail with
// LockImpactError on it under --require-safe, which internal/cli resolves to exit 5 (ADR 0086). It
// skips an operation the server refuses, which Unsupported already reports, and a step carrying no
// verdict at all exceeds every ceiling, so a planner never handed the session's capabilities fails
// closed rather than passing everything.
//
// A preview lowers the first queued migration's table-dependent verdicts through driver.Assessor
// and no other's, judgedFirst's rule: a later migration's table is not yet what it will be. A
// rewritten step, a step on a table its migration creates, and a verdict with no condition are never
// assessed, and a read that fails keeps the worst and is carried as Planned.Unassessed rather than
// failing a report that only asked in order to annotate.
//
// # An obligation the contract cannot express
//
// ADR 0013's record-before-commit rests on a driver's history store writing through the session's
// open transaction. PostgreSQL does; the contract cannot say so, and a store on a second connection
// would break the guarantee silently. It is stated in internal/driver's package doc and asserted
// per driver, exactly as ErrNoTable is (ADR 0019).
//
// # A data-only migration runs a chunk at a time, and carries on where it stopped
//
// A migration whose every operation is a data operation is marked by the planner to run in chunks
// (M12-T3, ADR 0078), and chunk.go is its run: for each operation, the key from Introspect, then a
// loop of the pacer's say, one driver.Chunker chunk, and that chunk's checkpoint, all in one
// transaction. The first chunk's transaction also writes the dirty row, so a run killed between two
// chunks leaves what a failed one does, and the last one's writes the applied row and clears the
// checkpoint instead. The marker and the applied row are two rows in one batch, so a run that wrote
// the marker takes the next ordinal, and executor.shift moves every later migration one on.
//
// A stopped run is carried on by the next Apply only where it was going up, a checkpoint names it,
// and both digests and the chunking still match -- classify queues it with the checkpoint, and every
// other dirty state is refused as before. Resolve's resume takes the same path in either direction,
// and an asserted resolution clears the checkpoint in the transaction its row goes in.
//
// pace.go is the seam: a Pacer is asked before every chunk, the first included, with what the chunks
// so far did, and answers how many rows the next covers, how long to wait for it, and why it is
// waiting. The wait goes through a Sleeper, so no test here ever sleeps.
//
// Lagged is the pacer M13-T8 drives from replica lag (ADR 0088). It reads driver.LagReader on every
// Pace and answers Hold -- wait, then ask again -- while any replica is above the threshold or could
// not be read, naming the worst, an unreadable one first; the engine reports each held poll through
// Progress with Waiting set, counts them on Run, and asks again after the wait. It keeps no state,
// so the next reading under the threshold is the resume. A held wait ends with the run's context, so
// safety.migration_timeout and Ctrl-C bound it exactly as they bound a lock backoff.
//
// # A statement that gave up on a lock is tried again
//
// Invariant #12's second half: a lock timeout retries with backoff rather than queueing. retry.go is
// the loop, and driver.ErrStatementLockTimeout is the only failure it repeats, so a refusal is never
// sent twice. Backoff is the seam -- Exponential doubles a base up to a cap with half of each delay
// random, from a Jitter the caller injects since this package may hold no randomness -- and Retried
// is told of every retry before its wait, which internal/cli logs as a warning (ADR 0082).
//
// What is rerun depends on what survived. On transactional DDL the rollback undid every statement
// and released every lock they took, so the whole migration runs again from where this run began,
// with its steps restored so an emulated operation is expanded again from the catalog the rollback
// put back. Where DDL commits as it runs, the statements before the one that waited are applied, so
// the next attempt begins at that one and keeps the expansions it already ran. A chunk is its own
// transaction and is rerun alone. Nothing is written to the history between attempts; the last
// failure is recorded as any failure is, wrapped in a LockRetryError saying how often it was tried.
//
// # Two bounds, each a deadline with a cause of its own
//
// timeout.go holds safety.statement_timeout, around each statement and each chunk, and
// safety.migration_timeout, around a migration with its retries and their backoff (M13-T3, ADR
// 0083). Each is context.WithTimeoutCause, so context.Cause tells either from an operator's
// interrupt: a bound reached is a TimeoutError naming the key to raise, and Ctrl-C stays Canceled.
// The driver stops the statement on the server before returning, so a stopped statement is a failure
// like any other -- rolled back where the engine allows, recorded on the session that ran it, never
// retried, the lock released by the ordinary path. A migration bound that cuts a backoff short
// reports the lock wait it was backing off from, and not retries spent.
//
// # A shadow-table migration is four recorded stages, and the hold has no end of its own
//
// A migration declaring Online's ShadowTable runs through shadow.go, shadow_run.go and
// shadow_settle.go rather than as statements (M13-T9, ADR 0089). It is refused before anything is
// created where the server does not permit capture, the session is no driver.Shadower, or an
// operation is not a change to one existing keyed table. The stream opens before the copy reads a
// row, and each stage -- copying, syncing, cut_over -- is recorded in <history table>_shadows in the
// transaction of the work it reports, so a run stopped anywhere leaves exactly what committed. The
// copy is an insert-ignore and every captured change an upsert or delete by key, so any interleaving
// is correct and a replay from the recorded position is harmless. The hold reads the log, applies
// with its position, and reads a confirmation the engine never writes; migration_timeout bounds the
// copy and never the hold, and a cancel detaches with the capture resource kept. Resolve's
// RepairRolledBack is the abort, by the names the state row recorded; Retire drops the retained
// original. Only an up runs through the shadow: a rollback is the ordinary inverse.
package engine
