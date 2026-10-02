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
// transaction opt-out is completed in M13-T4. See docs/adr/0021-apply-executor.md,
// docs/adr/0022-rollback-executor.md, docs/adr/0023-batch-narrowing.md,
// docs/adr/0024-dirty-runs.md and docs/adr/0026-dry-run-and-sql-sources.md.
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
// history records only a digest, so nothing can catch it after the fact; verifying an inverse
// against the live schema needs introspection and is M11-T2's. Authoring a down avoids it entirely.
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
// # Grouping decides where transactions begin
//
// A statement may declare that it cannot run inside a transaction; CREATE INDEX CONCURRENTLY is why
// the flag exists. Groups splits a statement list into consecutive runs that agree on that flag, so
// a migration is a sequence of runs rather than one wrapped block (ADR 0017).
//
// The loop that executes those runs is written: a transactional run goes through Transact, a
// standalone run executes bare, and the history row is written inside the last transactional run,
// which is the position ADR 0017 chose the closure shape to make expressible. The statement counter
// continues across a migration's runs and restarts for the next migration, because a position is a
// place in one migration and M9-T6 resumes from it.
//
// Grouping lives here and not in a driver, because it reads Statement's flag and nothing
// engine-specific. Three drivers must not reach three answers about the same statement list.
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
//     never happened is the driver conformance suite's own failure, one layer up;
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
// # An obligation the contract cannot express
//
// ADR 0013's record-before-commit rests on a driver's history store writing through the session's
// open transaction. PostgreSQL does; the contract cannot say so, and a store on a second connection
// would break the guarantee silently. It is stated in internal/driver's package doc and asserted
// per driver, exactly as ErrNoTable is (ADR 0019).
package engine
