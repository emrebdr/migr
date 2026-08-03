// Package engine orchestrates migration execution: locking, transactions, apply and rollback, and
// batch semantics.
//
// Deciding what order migrations run in is not here. Finding them, sorting them by version, and
// overlaying the dependencies they declare is internal/migration, which imports stdlib and nothing
// else — so the CLI can list and validate migrations without reaching through an executor that
// opens databases (M4-T1, M4-T2). Turning operations into statements is internal/plan, which needs
// no database either. What arrives here is a sequence and a way to compile it.
//
// Status: the queue is complete (M4-T5). The dirty-state model is M4-T9, rollback is M4-T6, and the
// transaction opt-out is completed in M13-T4. See docs/adr/0021-apply-executor.md.
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
//	Latest             only when something will run
//	per migration      Emit, hash, compile, group, refuse, run, record inside the last transaction
//	unlock             deferred, so it runs after the rollback rather than before it
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
// # A batch belongs to one database
//
// A batch is one migr invocation against one database, and it is one more than that database's own
// last recorded run. No lock spans two databases, so a shared counter has no allocator; migr down
// reverses one database's last batch, and a shared number can name a batch that database never
// used. Two databases in one migr.yaml therefore get unrelated numbers, and migr up loops.
//
// It is read from Latest rather than from the log already in hand, because the batch is a decision
// about writing while the log was read for reconciling — and M4-T8 will filter that log by --step
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
//   - any migration reconciling to drifted (M4-T7) or dirty (M4-T9). Both refuse the whole run
//     rather than the migration, including the ones queued ahead of it: running until the queue
//     reaches the problem leaves a partially migrated database for a condition that was knowable
//     before the first statement.
//
// The switch on history.State carries no default, so exhaustive names this package the day a sixth
// state arrives — M13-T14 already says it needs one. The trailing return is the answer for a state
// this build cannot interpret, and it refuses rather than guessing.
//
// Four are refused per migration, before its first statement, each naming the task that lifts it:
//
//   - one that compiles to no statements at all, because reporting success for a schema change that
//     never happened is the driver conformance suite's own failure, one layer up;
//   - one whose session declares no transactional DDL, because a failure partway would leave the
//     schema half changed with nothing recording how far it got. That is M4-T9's, and the branch
//     reads capability.Set.TransactionalDDL and never a driver name (CLAUDE.md §11);
//   - one that groups into more than one run, because two runs means two commits and nothing here
//     can record partial progress across them;
//   - one whose single run is not transactional, because then there is no transaction for its
//     history row to be written inside.
//
// The last is the hole M3-T5 left: its refusal counted groups, so a migration that was entirely
// standalone statements passed it and was rejected by the driver instead. Both belong to M13-T4,
// which deletes two preconditions rather than rewriting an executor.
//
// # A failure aborts; it never undoes
//
// The migrations that committed stay committed, the rest are never asked what they do, and the
// error names the migration and the operation the failing statement was compiled from (invariant
// #9). Result is populated alongside that error, which is unusual in Go and is the point: without
// it an operator cannot see which migrations did commit.
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
