// Package driver defines the database driver contract and the name-to-constructor registry.
//
// Adding a database must not require changing anything outside its own package. Two rules
// govern this subtree: drivers never import one another — shared logic moves up into this
// package — and raw SQL appears nowhere else in the codebase (CLAUDE.md invariants #2 and #3).
//
// Status: contract and registry complete (M2-T1); capabilities added (M2-T2). See
// docs/adr/0011-driver-contract.md and docs/adr/0012-capability-resolution.md.
//
// # The split: a Driver answers what needs no database
//
// Name and Compile need nothing. Everything that needs a session belongs to the Conn that Open
// returns, and that is the whole of the design.
//
// An earlier sketch put Introspect, Lock, and History on Driver with the connection as a
// parameter. That shape made pgDriver.Introspect(ctx, mysqlConn) a type-correct program, left
// History() with no connection to bind to, and gave the connected server's version — which
// invariant #13 requires in order to derive lock impact from the server rather than from
// documentation — nowhere to live. Making the connection the receiver removes all three.
//
// Compile is deliberately pure: no context, no connection, no clock. That is what lets migr plan
// print statements without touching a database, what lets golden tests run with no engine
// installed, and what the Compiler interface exists to name.
//
// # Capabilities belong to the session
//
// Capabilities returns what the connected server can do, on both axes, already resolved against
// its detected version. It sits here rather than on Driver because invariant #13 requires a lock
// verdict derived from the server rather than from documentation, and a driver holding no
// connection cannot know which server it will be asked about.
//
// A driver builds its set inside Open, after detecting the version, so the method takes no context
// and returns no error. A set that will not build fails the connection instead. What a driver
// declares is authoritative: Compile must refuse exactly the operations the set refuses, and M2-T4
// is what proves the two agree. See internal/capability and
// docs/adr/0012-capability-resolution.md.
//
// # History belongs to the session too
//
// History returns the store already bound to this connection, which is why it takes no context
// and returns no error: there is nothing left to fail. A driver builds it inside Open, alongside
// the capability set, and Store's own methods each carry the context they need.
//
// A store's writes must go through the session's open transaction when there is one. ADR 0013's
// "a migration is recorded before its transaction commits" rests entirely on that, and nothing here
// can express it: History takes no transaction and Record takes no handle, so a store that used a
// second connection would compile, pass every test it has, and leave a row claiming a migration was
// applied that the rollback discarded. It is the same unenforced obligation ADR 0019 recorded for
// ErrNoTable, and it is checked the same way — per driver, by a test that discards a transaction
// after the row was written and asserts the row is gone.
//
// internal/history never imports this package. It is named here as a return type, so the
// dependency runs one way and the other direction is an import cycle. ADR 0011 recorded that as a
// review rule because depguard cannot express it — the deny prefix deliberately leaves this
// package importable. Naming history.Store here is what made it mechanical: the cycle the rule
// warned about is now the thing that enforces it. See docs/adr/0013-migration-history.md.
//
// # Deferred, but decided
//
// One method is not written yet. It is recorded here so the task that adds it inherits a decision
// rather than a blank page:
//
//	Driver.Capture(ctx, capture.Request) (capture.Stream, error)   M13-T9
//
// Introspect's return type arrived with M11-T1 and the method with M11-T2, which is this rule
// applied rather than an exception to it: three drivers producing a snapshot was M11-T2's own
// done-when, so adding the method first would have meant three implementations returning a
// plausible empty value.
//
// Capture hangs off Driver rather than Conn deliberately. Reading a replication log — MySQL's
// row-format binlog, PostgreSQL's logical decoding — is a second connection of a different kind
// with its own lifetime: it outlives individual statements and must be closed even while the
// migration session is healthy. A PostgreSQL slot left open retains WAL until the disk fills, so
// that lifetime is an outage if it is tangled with another one. See
// docs/adr/0014-online-change-capture.md. internal/capture is subject to the same one-way import
// rule as internal/history, for the same reason.
//
// ServerVersion and capability.Set.Server report the same fact, so a driver derives both from one
// detection inside Open or they can disagree. M3-T1 added it and does exactly that: it reads
// server_version_num once and renders the display string from the number.
//
// They are not stubbed. A method whose only possible implementation returns a plausible empty
// value is worse than a missing one, and the conformance suite's "no third possibility" rule
// exists to forbid exactly that.
//
// # A Conn is one pinned session, not a pool
//
// PostgreSQL session advisory locks belong to a backend. Acquiring on one pooled connection and
// releasing on another silently fails to release — satisfying invariant #11's deferred-release
// requirement while breaking the property that requirement exists to guarantee. A driver that
// pools must pin a connection for the lock's lifetime.
//
// Unlock is safe to call more than once, so an early explicit release and a deferred one do not
// double-release. It returns an error because a failed release leaves the lock held until the
// session ends.
//
// The release path must not inherit a canceled context, or it fails exactly when it matters
// most — the context that caused the failure is already canceled:
//
//	defer func() { err = errors.Join(err, unlock(context.WithoutCancel(ctx))) }()
//
// # Statements
//
// A Statement is the text and one flag: whether it can run inside a transaction. Not every
// operation can — CREATE INDEX CONCURRENTLY is the motivating case — and an executor written on
// the assumption that one migration is one transaction cannot be retrofitted cheaply. The flag
// exists before the first executor does, so M3-T5 groups statements rather than wrapping them.
// It is spelled so that the zero value is the safe answer.
//
// It did: internal/engine's Groups splits a statement list into consecutive runs that agree on the
// flag, and Transact brackets one run. Nothing any driver compiles sets the flag yet, so a
// migration is one run today; the executor's loop over runs is written, and what it still refuses
// is a migration of more than one run, or one whose single run is not transactional and so has no
// transaction for its history row to be written inside. Both name M13-T4, which is where the flag
// stops being hypothetical.
//
// # Transactions
//
// Transact takes a closure rather than returning a handle, and the reasoning is ADR 0017's. In
// short: the driver brackets, so rollback on error and rollback on panic are structural rather
// than remembered, the release path applies context.WithoutCancel once instead of at every call
// site, and ADR 0013's "recorded before the transaction commits" is the last line of the closure
// body rather than a hook parameter that would be nil at every call site.
//
// Deciding which statements share a transaction is not a driver's. That is grouping, it reads
// Statement.NoTransaction and nothing else, and it lives in internal/engine so three drivers
// cannot reach three answers about one statement list.
//
// The migration lock is taken before the transaction begins, so deferring both in that order makes
// the rollback run before the release. PostgreSQL refuses every command in an aborted transaction,
// including the one that gives an advisory lock back, so the reverse order turns a failed
// migration into a failed release as well.
//
// There is no Args field. No target engine parameterizes schema changes, so it would be
// permanently empty for the statements this type carries, and its presence would invite drivers
// to route ordinary queries through it. A driver's own internal queries are its business.
//
// # The migration lock: what a driver owes, and what it may not decide
//
// Options carries two bounds and a strategy, and Waits defaults whichever migr.yaml left unset.
// Non-positive means unset rather than "no bound", because on every engine so far a zero timeout is
// the spelling for waiting forever — the opposite of what an operator who left a field blank meant.
// The defaults are DefaultLockWait and DefaultStatementLockWait, and internal/config applies those
// same constants, so a driver built from migr.yaml and one built by hand behave alike.
//
// The two are different settings and are easy to swap. LockWait bounds the wait for the migration
// lock (migr.yaml's lock.timeout); StatementLockWait bounds one statement's wait for a lock
// (safety.lock_timeout), which is what invariant #12 requires of every DDL statement.
//
// LockStrategy is what migr.yaml asked for, and a driver must refuse one it does not implement
// rather than silently substituting its own. internal/config asks whether a value is a word migr
// knows; only the driver can say whether this engine can do it.
//
// Giving up on the lock is ErrLockTimeout, and every driver reports it, because internal/cli cannot
// name a driver (invariant #1) and exit code 3 is otherwise unreachable. A driver may wrap it —
// PostgreSQL adds the advisory key an operator matches against pg_locks — but the sentinel has to
// survive errors.Is.
//
// LockBusyError carries who holds the lock, and LockHolder is deliberately shaped for three
// engines rather than one: Process is a string because MySQL's holder is a connection id, SQLite's
// an OS pid in a lock file, and PostgreSQL's a backend pid. Elapsed is a duration the server
// measured, not an instant, since forbidigo leaves a driver no clock and comparing a server
// timestamp against a client one is wrong across skew. A holder is read after the wait has already
// failed, so it names who holds the lock now rather than provably who blocked this run — the
// message says so, because otherwise it aims a termination at a session that may be innocent.
//
// # The holder's duration is labeled, because the three engines measure three different things
//
// Measured names what Elapsed is a duration of, and ADR 0060 added it because no one sentence is
// true of all three. PostgreSQL reads a session age out of backend_start, which bounds the hold
// from above. SQLite reads the hold itself, out of the stamp in its own lock file. MySQL reads the
// holder's time in its current state, because the server records a session's start time nowhere at
// all — and that number under-reports: a session an hour old that has just begun a statement reads
// near zero.
//
// The label rather than the duration is what says whether anything was measured. Zero is a real
// answer on MySQL, where a holder that has just taken the lock and gone idle has been idle for no
// whole seconds, so a driver sets Measured only where it read a number. An Elapsed with no label
// renders a line naming that omission as a driver bug, which is the shape capability.Emulated with
// no strategy already has: a value that cannot be built honestly degrades to one that says so.
//
// What "recoverable" means is a property of the strategy rather than of the driver. PostgreSQL's
// advisory lock dies with its backend and needs no recovery code; a lock file does not vanish when
// its owner does, so the file strategy owes a staleness rule this one has no use for. See ADR 0020,
// and ADR 0055 for how SQLite pays it: the lock is an flock the kernel reclaims on death, the file
// beside it is only where that lock and its holder's record live, and the record is what a failed
// wait reports rather than what decides. MySQL's user-level lock is the advisory shape again — it
// dies with its session, so it owes no staleness rule either (ADR 0060).
//
// # Refusing an operation
//
// UnsupportedError is the one way a driver says it cannot perform an operation. M2-T4 asserts
// every operation is either compilable or refused with a reason — no third possibility — and
// that is checkable only because a refusal is distinguishable from a compiler bug through
// errors.Is(err, ErrUnsupported).
//
// # Registration is explicit
//
// cmd/migr builds a Registry and calls Register, which returns an error on a duplicate name.
// There is no init() and no blank import, because neither can report that error, and panicking
// instead terminates with status 2 — which this project has already assigned to validation
// failure, making a wiring bug indistinguishable from a bad migration to anything reading exit
// codes.
//
// A Registry is a value, so two of them share no state and tests need no reset hook. It was also
// expected to be what the conformance suite took, and it is not: Registry.New already enforces
// that a driver answers to the name it was registered under, so a registry parameter would prove
// nothing that check does not, and depguard leaves a driver package able to register only itself.
// test/conformance/driver takes a Driver and an open Conn instead (M2-T4).
//
// A driver name has one spelling. migr.yaml deliberately does not validate driver names against
// a list — that would require core to know which drivers exist — so this registry is the only
// place a name is ever checked, and it rejects a variant rather than quietly accepting a second
// way to say the same thing.
//
// # A note for M2-T3
//
// depguard denies the internal/driver/ prefix everywhere except cmd/migr, so a shared
// internal/driver/drivertest helper package would be denied to internal/driver/postgres and to
// test/conformance/driver. The fix is one allow entry, using the same longer-prefix-beats-shorter
// mechanism that already keeps internal/runtime/protocol importable.
package driver
