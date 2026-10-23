// Package postgres implements the Driver contract for PostgreSQL using pgx.
//
// PostgreSQL is the reference driver: it supports transactional DDL and most operations
// natively, which is why the engine is built against it first. It provides advisory locking, and
// its lock-impact verdicts are keyed to the detected server version — ADD COLUMN with a default
// rewrites the table on PostgreSQL 10 and is instant from 11 onward.
//
// Status: complete through M3-T7, plus M4-T4 — connections, compilation, the capability
// declaration, the migration lock and its configured bounds, transactional execution, the history
// store, the round-trip proof that every operation this driver compiles can be rolled back against
// PostgreSQL 16 and 17, and M11-T2's Introspect.
//
// # Reading a snapshot back
//
// Four probes over current_schema(), which is asked for rather than assumed: migr.yaml need not
// name a schema, and an unset one leaves the search path to the server. Every type this driver emits
// reads back exactly -- this is the only one of the three engines whose native map has no
// collisions, and only because it refuses enum outright. Two measurements decided the SQL. Joining
// pg_constraint on conindid alone double-counts, because a foreign key's conindid names the index on
// the *referenced* side, so the parent's own key comes back twice; and generate_subscripts over
// indkey is 0-based, int2vector's lower bound being zero.
//
// What it will not compare is what this server rewrites: a check condition asked for as "n" > 0
// reads back as (n > 0), and a view asked for as SELECT * comes back with an expanded column list.
// Both are carried as the engine text ADR 0065 says they are. See
// docs/adr/0069-per-driver-introspection.md.
//
// # The migration lock
//
// One session-level advisory lock, taken on the session that will release it — which is why a
// Conn is one pinned connection and never a pool (ADR 0011). The key is two int4s:
//
//	pg_advisory_lock(<a constant identifying migr>, <digest of schema + history table>)
//
// PostgreSQL keeps the two-key advisory space separate from the one-key space and never lets them
// collide, so an application using pg_advisory_lock(bigint) for its own purposes cannot collide
// with migr at all — by construction rather than by hash luck. The fixed first half makes migr's
// locks recognizable in pg_locks, which is what the holder report reads.
//
// The second half is a SHA-256 digest, not hash/maphash: maphash is seeded per process, so it
// would give every migr invocation a different key and the lock would block nobody. The two inputs
// are separated by a NUL, which no identifier may contain, so schema "a" with table "bc" and
// schema "ab" with table "c" are different installations rather than one. Advisory locks are
// already scoped to the database, so the database name adds nothing and is not an input; neither
// is the DSN, which carries a password and can be spelled several ways for one server.
//
// # The wait is bounded by the server, not by a context
//
// pg_advisory_lock waits indefinitely, and the obvious bound — wrapping the call in a context with
// a timeout — is wrong here. pgx closes the connection when a context is canceled mid-query, and
// its own documentation says so: "The default behavior when a context is canceled is for the
// method to immediately return. In most circumstances, this will also close the underlying
// connection." A Conn is pinned for the lock's lifetime, so that would kill the one session that
// could have held it, leaving nothing to retry on.
//
// So the acquire sets lock_timeout for its own duration. A wait that gives up fails with SQLSTATE
// 55P03 and the session stays alive and usable — verified against both test servers, along with
// the fact that lock_timeout covers an advisory wait at all, which is not obvious from the
// documentation's "a table, index, row, or other database object".
//
// It is set through set_config rather than SET, because SET takes no bound parameters — the same
// reason applySearchPath uses it, and the reason migr.yaml's lock.timeout is bound rather than
// concatenated.
//
// The surviving session is not merely a precondition for something later; it is what makes the
// holder answerable at all. Bounded by a context, the connection would be closed by the time the
// acquire failed, and there would be nothing left to run pg_locks against.
//
// # lock.timeout is one budget, and there is no retry
//
// pg_advisory_lock queues, and PostgreSQL's lock manager is a fair FIFO waiter — so a session that
// gives up and re-acquires goes to the back of a queue it was at the front of. Backoff and jitter
// exist to break contention convoys on a primitive that does not queue; this one does.
//
// Invariant #12's "a timeout retries with backoff rather than queueing" is about DDL statements,
// and its reason does not transfer: an ALTER TABLE blocked behind a long-running query blocks every
// later query on that table, so staying queued is itself the outage. An advisory lock blocks
// nothing but other migr runs. safety.lock_retries belongs to that per-statement bound, which is why
// it sits in the safety block beside safety.lock_timeout rather than beside lock.timeout. See ADR
// 0020. Since M13-T2 a statement whose lock_timeout expires is 55P03 wrapped in
// driver.ErrStatementLockTimeout, and internal/engine reruns the migration's transaction after a
// jittered backoff -- the rollback having released every lock its earlier statements took (ADR 0082).
//
// # A canceled statement is canceled on the server
//
// pgx's default reaction to a canceled context is a deadline on its own socket, after which the
// dead connection sends a cancel request from a goroutine and terminates: measured at M13-T3 on 16
// and 17, the statement did stop, but only after Exec had returned, and the session -- the one
// holding the advisory lock and writing the failed run's row -- was gone. pinCancellation sets the
// cancel-request handler instead, which sends the request first and waits for 57014, with a 5s
// socket deadline as the fallback for a server that never answers. 57014 says nothing about why, so
// stoppedBy joins the context's ending to it (ADR 0083).
//
// # A duration is spelled to the server, not printed
//
// lock_timeout takes an integer and at most one unit from us, ms, s, min, h, d. Go's canonical
// duration form is multi-unit, so 90s renders "1m30s" and an hour renders "1h0m0s" — both refused
// by both servers, and a refused set_config means the bound is lost rather than applied. Every
// value anyone tries by hand happens to round-trip, so passing Duration.String straight through
// survives testing and fails the first time someone writes lock.timeout: 2m.
//
// lockTimeoutSetting is therefore the only thing that reaches the server: integer milliseconds,
// rounded up, refused above what an int can hold. Rounding up rather than truncating is not a
// nicety — Duration.Milliseconds turns 500µs into 0, and lock_timeout of 0 is PostgreSQL's
// spelling for waiting forever. Duration.String stays correct in exactly one place, the message an
// operator reads.
//
// # A second acquire is refused, and a second release is not
//
// pg_advisory_lock is re-entrant and counted, so acquiring twice on one session would succeed
// instantly and raise the hold count to two. One release would then report success while the lock
// was still held, and the next run would block forever against a lock nobody knows about. Lock
// refuses instead, naming the bug at the call site.
//
// Releasing twice is fine and returns nil, which is what the contract's "safe to call more than
// once" is for: an early explicit release and a deferred one must not double-release. A release
// the server answers false to is an error rather than a shrug — it means this session did not hold
// what it thought it held.
//
// # A dead backend needs no recovery; a dead client is not a dead backend
//
// Invariant #11 requires a stale-lock story. PostgreSQL's is that the server releases a
// session-level advisory lock when the backend goes away, so there is no recovery code to write —
// proven by abandoning a locked session, and again by terminating one with pg_terminate_backend.
//
// That covers a client that closed, crashed cleanly, or was killed with SIGTERM. It does not cover
// one killed with SIGKILL, OOM-killed, or partitioned away: no FIN reaches the server, the backend
// stays blocked in ReadCommand, and it holds the lock until TCP keepalives expire — two hours by
// default on Linux. A crashed migr up would wedge every later run for that long, which is the one
// case invariant #11 exists for.
//
// So the startup packet pins tcp_keepalives_idle, _interval and _count, bringing that to about
// ninety seconds. They are USERSET, they read back as set over TCP, and they report 0 without
// error on a Unix socket or an unsupported platform — measured on both servers, both transports.
// The reaping itself needs a dropped packet and is not reproducible in the compose environment;
// a proxy that closes a socket sends FIN or RST, which is the opposite of the failure being
// modeled. What is tested is that the server was asked.
//
// migr never terminates a holder itself: killing another session on the strength of an advisory
// row is not a decision a migration tool makes silently, and the usual victim would be a
// legitimately slow migration. It reports the pid instead, and an integration test terminates the
// pid taken out of the error to prove the advice works.
//
// Recoverability is a property of the strategy rather than of the driver, and the file and table
// strategies will need machinery this one does not — a lock file, unlike a backend, does not
// vanish when its owner dies. lock.strategy reaches this driver through Options and anything but
// advisory is refused at construction, so a strategy migr.yaml names and this engine cannot do is
// an error rather than a silent substitution.
//
// # Who holds the lock, and since when
//
// A failed acquire reads pg_locks joined to pg_stat_activity on migr's fixed class id and reports
// a driver.LockHolder. Four clauses in that query are load-bearing and each has a test: granted
// separates the holder from the sessions queued behind it; objsubid = 2 keeps the two-integer key
// space apart from the bigint one, whose rows carry an identical classid and objid; and classid
// and objid are what make the lock migr's, and this installation's.
//
// PostgreSQL records no grant time for an advisory lock, so what is reported is the holder's
// session age — which bounds the hold from above and is the honest answer. xact_start is the
// tempting wrong one: it is NULL for an idle holder, which is exactly what a session holding a
// lock and doing nothing is, so it would render "held for 0s" for a hold of an hour.
//
// That is what this driver labels its duration, since ADR 0060: the three engines measure three
// different things and only the one that measured can say which. A session age is also why the label
// is set only where a number was read -- a backend opened this instant is genuinely zero milliseconds
// old, and an unlabeled zero is what renders as an age this connection could not determine.
//
// Across roles pg_stat_activity withholds backend_start while leaving application_name visible, so
// the degraded report is "who, but not how long" rather than a confident zero. The holder is read
// after the wait has already failed, so it is who holds the lock now rather than provably who
// blocked this run, and the message says so — otherwise it aims a pg_terminate_backend at a
// session that may have taken the lock a moment ago.
//
// # A transaction is bracketed, never handed out
//
// Transact begins, runs the caller's work, and commits only if that work returns nil. There is no
// handle to hold, so a transaction cannot outlive the call or be paired with another session, and
// the rollback covers a returned error and a panic through one mechanism. It runs on a context
// stripped of cancellation, for the reason the release path is: the context that caused the
// failure is the one already canceled. See ADR 0017.
//
// A second transaction on one session is refused, and so is one this driver did not open — the
// session is asked, not just the tx field. PostgreSQL answers a second BEGIN with a warning and
// does nothing, so a commit would end somebody else's transaction and make permanent whatever it
// had done. That refusal is the counterpart of the lock's second-acquire refusal, for the same
// reason: an engine primitive that silently does nothing is worse than one that fails.
//
// A commit the server answers by rolling back is a failure. pgx reports it as ErrTxCommitRollback,
// and taking it for success would report a migration applied whose every statement was discarded.
//
// While a transaction is open, every statement goes through it rather than around it, and so does
// every query the history store makes. That is what makes ADR 0013's "recorded before the
// transaction commits" true by construction rather than true because pgx happens to send both
// down one connection.
//
// A statement declaring it cannot run inside a transaction is refused while one is open. Nothing
// compiled here sets that flag, so the guard is unreachable today and is the only place that could
// notice an executor failing to split a migration around such a statement.
//
// # The history store
//
// The table's shape is internal/history's, so three drivers cannot invent three of them, and
// Ensure creates it by compiling history.Schema through this package's own compiler. Everything
// after that is ordinary DML — an INSERT and two SELECTs, raw SQL that invariant #3 permits here
// and nowhere above. Unlike DDL a statement's values are bound rather than written in, so only
// the table name is concatenated, and New refuses one PostgreSQL could not carry.
//
// The usable length of that name is 51 bytes rather than 63, because history.Schema names the
// index after the table. New checks the derived name too, so an operator reads a sentence about
// history.table instead of a complaint about a field called "name".
//
// Ensure compiles both operations before running either and runs them as one unit — inside the
// caller's transaction when there is one, in its own otherwise. The reason is retry idempotency:
// a failure between them would leave a table present without its index, after which every later
// Ensure reports it present and the index is never created at all. It is a setup step, taken
// before the migration transaction opens and under the migration lock, and it deliberately does
// not go through the plan or capability path — this driver declares add_index as long_exclusive,
// so --require-safe would otherwise refuse to create migr's own history table.
//
// to_regclass folds an unquoted name to lower case, so the existence check passes the name
// through quote_ident. Without it a mixed-case history table reports absent forever and every run
// tries to create it. When the table is present, every column history.Schema declares must be
// there; extras are fine, because a table a newer migr extended must stay usable.
//
// One SQLSTATE means two things and is classified per call site rather than from a table. Two
// concurrent CREATE TABLEs do not both raise 42P07: the loser blocks on a pg_catalog unique index
// and raises 23505 when the winner commits, measured on both test servers. So 23505 is a lost
// race in Ensure and a duplicate run on a write, and the two never share a statement. A lost race
// names the migration lock, whose advisory key is derived from the schema and table names and
// needs no table to exist — it is available on a database with no history at all.
//
// A missing table is reported as itself on every path, reads included. Read as "no runs yet", a
// history.table typo would make a fully migrated database look virgin and the next up would
// re-run every migration it had already applied.
//
// error is written as NULL rather than as an empty string, since it is the one nullable column of
// the fifteen and writing ” makes that declaration decorative. duration_ms is what the column is
// called: a run measured in microseconds does not round-trip, and truncating toward zero is the
// behavior rather than a defect.
//
// # Every statement runs under a lock timeout
//
// lock_timeout is pinned in the startup packet at Open, not set on the session afterwards, and the
// difference is invariant #12. A startup-packet value reaches the server as source "client", which
// sets the setting's reset_val; an interactive SET does not. acquire raises lock_timeout to the
// lock wait and ends with RESET — so had the per-statement bound been established with set_config,
// the first migration lock would have returned it to 0 and every DDL statement afterwards would
// have waited for a lock forever. Both halves were measured on PostgreSQL 16 and 17.
//
// So there are two bounds, and they are not the same setting: the advisory acquire waits under
// migr.yaml's lock.timeout, and each statement under safety.lock_timeout. Options carries both,
// and whichever migr.yaml left unset falls back to the contract's own constant rather than to
// zero — which the server would read as no bound at all.
//
// # What a lock verdict says
//
// The four verdicts describe how long a lock is held, not which lock is taken. Nearly every
// ALTER TABLE here takes ACCESS EXCLUSIVE, which blocks readers as well as writers, so "which
// lock" would sort a catalog update alongside an index build. See ADR 0016.
//
//	lock_free        create_table; create_view without or_replace -- nothing existing is touched
//	brief_exclusive  rename_table, drop_column, rename_column, drop_foreign_key, drop_check,
//	                 drop_view, create_view with or_replace
//	long_exclusive   add_index and add_unique and add_primary_key (an index build);
//	                 add_foreign_key and add_check (a full scan to validate);
//	                 drop_table, drop_index, drop_unique, drop_primary_key (files deleted)
//	table_rewrite    see add_column and alter_column below
//	unknown          raw, permanently
//
// The four drops that delete a table's or an index's files hold their lock while the files go:
// PostgreSQL unlinks at commit and releases locks after it. Measured on 16 and 17 at M13-T1, DROP
// TABLE took 2.5 ms at 224 kB and 22 ms at 471 MB, and DROP INDEX 1.6 ms at 136 kB and 30 ms at
// 760 MB, so the hold scales with the relation (ADR 0081). drop_column only marks the column dropped,
// and a constraint that is not an index deletes nothing, so those stay brief.
//
// raw is the case unknown exists for and the one that never resolves: the framework cannot know
// what the statement does. A driver with nothing left to classify still reports this one.
//
// add_column depends on the payload and on the server at once. PostgreSQL 11 stores a new column's
// default in pg_attribute and applies it on read, so a default no longer rewrites every row --
// unless it is volatile, because then every row needs a value of its own. gen_random_uuid() is
// volatile and rewrites the table on every release; a literal and the three SQL-standard time
// keywords do not, from 11 onward. Volatility is read from pg_proc rather than carried in a list.
//
// alter_column reports the worst of the changes it was asked for, since it runs them all: a retype
// rewrites, SET NOT NULL scans the table, and DROP NOT NULL or either default change is catalog
// work. A retype is always reported as a rewrite even where PostgreSQL would not perform one --
// varchar(255) to varchar(512) is binary coercible -- because the operation carries only what a
// migration author recorded the old type as. Over-reporting costs a flag; under-reporting costs an
// outage (invariant #13).
//
// What is measured rather than assumed: an integration test asserts that nothing rewrites the
// table without having been declared a rewrite, by watching pg_class.relfilenode. The
// brief/long distinction is a duration under contention and is M13-T12's to measure, not this
// driver's to claim.
//
// # What the server is asked at Open
//
// Beside the version, one query asks whether gen_random_uuid() exists and how volatile it is.
// Both answers matter: the volatility keys the add_column verdict, and the existence closes the
// support question M3-T2 could not answer, since that function moved into core only in
// PostgreSQL 13 and this driver connects to 10. A server that lacks it now gets a refusal from
// the declaration at validation time instead of an error from the server at apply time.
//
// Asking beats testing the version. pgcrypto has provided that function for a decade, so a
// PostgreSQL 12 carrying the extension has it, and a version comparison would refuse a server
// that works.
//
// The configured schema is checked for existence in the same pass, because PostgreSQL drops an
// entry naming no schema from search_path rather than reporting it -- so a typo opens cleanly and
// fails at the first migration with a message naming neither the schema nor the config key.
//
// # The declaration is derived from the compiler
//
// A driver states what it supports twice — Compile refuses with a driver.UnsupportedError, the
// capability set refuses with a Verdict — and the conformance suite asserts the biconditional:
// Resolve(op).Allowed() is false exactly when Compile refuses. Here the rule calls the compiler,
// so the two cannot drift and the suite proves the wiring rather than a coincidence.
//
// Callers are unaffected: migr validate and migr plan read the declaration before anything
// touches a database, so the declaration is still what is consulted. Which of the two is derived
// is an implementation detail of this driver, and it does not generalize — SQLite must declare
// Emulated with a named strategy, and a Compile that succeeded says nothing about how. See
// ADR 0015.
//
// rules() needs no connection, so the conformance suite runs untagged against a session double
// carrying the same declaration, and the agreement is a make test and CI gate rather than one
// only Docker reaches. The integration run stays: it holds the declaration a live server produced
// to what that server's driver compiles, which is a different claim.
//
// # Refusals are per payload, not per kind
//
// PostgreSQL supports create_table. It does not support the one with an enum column. Five refusals
// have that shape, and capability.Rule takes the whole operation so the declaration can say the
// same thing:
//
//   - an enum type, which needs a CREATE TYPE object no v1 operation owns — nothing would drop it,
//     so apply, roll back, and apply again fails on a type that already exists. Permanent: the
//     blocker is an IR change rather than a driver one, so the reason names what is missing rather
//     than a task (ADR 0016)
//   - a uuid column default on a server with no gen_random_uuid, which the declaration knows and
//     the version-blind compiler does not
//   - a nested array, because PostgreSQL declares array dimensions and does not enforce them:
//     integer[][] and integer[] are one type, so two logical types would become one native one
//   - a drop_primary_key naming no constraint, because PostgreSQL has no name-free form; the v1 IR
//     leaves the name optional because MySQL has no name for a primary key
//   - an identifier over 63 bytes, which PostgreSQL truncates with a notice rather than an error,
//     so two long names silently become one object
//   - an identifier, literal, or embedded fragment holding a NUL, which ends the statement where it
//     appears, because a query reaches the server as a C string
//
// Every one is a silent-wrong rather than a loud failure, which is why each is refused rather than
// left to the server. A raw operation addressed to another engine is the sixth, and the one the
// conformance suite already required of every driver.
//
// # What a rollback restores, and what it does not
//
// M3-T7 holds every operation this driver compiles to the round trip: applied against PostgreSQL
// 16 and 17, then reversed, the schema digest must return to exactly what it was. Nineteen of the
// twenty pass. raw is the twentieth and has no inverse by construction — its undo is authored, as
// ir.Raw.Reverse's own message instructs, and a fixture cannot derive one.
//
// Two things a reversal does not bring back, both measured rather than assumed, and neither of
// them a compiler bug:
//
//	drop_column      an index on the dropped column. PostgreSQL removes it silently, and
//	                 ir.DropColumn.From records a column — name, type, nullability, default —
//	                 and nothing that depended on it.
//	add_primary_key  the nullability its subject had before. ADD PRIMARY KEY sets NOT NULL on a
//	                 nullable column and DROP CONSTRAINT leaves it set, on both 16 and 17.
//
// Neither is fixable by widening a from payload: the first destroys more than the operation
// names, and the second inverts by name, where a name says nothing about what the constraint
// implied. Both are ADR 0008's open trust problem, and both closed the same way — M11-T2 reads the
// live schema before either operation runs and refuses it, because by rollback time the index is
// gone and the nullability was never recorded anywhere. What this driver owes that refusal is
// Introspect: the engine reasons about what the IR records against what the snapshot holds, and
// names no engine at all.
//
// The practical consequence for a migration author is an ordering rule: drop dependents before
// their subjects, the way the M3-T7 fixture does. PostgreSQL enforces the loud half of that
// itself — it refuses to drop a column a view depends on — and says nothing about the quiet half.
//
// # Identifiers are always quoted
//
// Double quotes, with an embedded quote doubled, unconditionally. One rule covers both halves of
// what M3-T2 asked for: "order" is not a keyword and "userID" does not fold to userid. A
// reserved-word list would need updating every release and would be silently wrong in between.
// Nothing is schema-qualified — Options.Schema sets search_path at Open, and qualifying as well
// would make one migration mean different things depending on which mechanism an operator used.
//
// # Logical types
//
//	string(n)   varchar(n)     int16/32/64   smallint/integer/bigint   uuid        uuid
//	text        text           float32/64    real/double precision     json        jsonb
//	bool        boolean        decimal(p,s)  numeric(p,s)              binary      bytea
//	date        date           timestamp     timestamp                 array(of)   <of>[]
//	time        time           timestamptz   timestamptz               enum(...)   refused
//
// json maps to jsonb rather than json: PostgreSQL's json type stores the source text, preserving
// whitespace, key order, and duplicate keys, so two equal documents are different values and
// nobody indexes it. A schema type should be the canonical one.
//
// Default functions map to CURRENT_TIMESTAMP, CURRENT_DATE, CURRENT_TIME, and gen_random_uuid().
// The first three are SQL-standard keywords rather than functions and are stable by definition, so
// they are not looked up; the fourth is, because both its existence and its volatility vary by
// server. Compile is pure by contract and cannot know which server a session reached, so the
// version-dependent refusal lives in the declaration. ADR 0015 recorded the asymmetry; ADR 0016
// narrowed it to a refusal an operator sees before anything runs.
//
// # What this driver does not emit
//
// No USING clause on ALTER COLUMN ... TYPE: a cast that does not exist should fail loudly rather
// than be coerced into one that silently loses data. Compile emits no CONCURRENTLY and no NOT VALID:
// it is pure and version-blind, and both are the session's to choose (M13-T5, ADR 0085).
//
// # What a session rewrites
//
// pgConn is a driver.Rewriter. Against the version Open detected, it answers six operations with
// a safer sequence than the plain compile:
//   - add_check and add_foreign_key become ADD ... NOT VALID and then VALIDATE CONSTRAINT, the
//     VALIDATE bare;
//   - alter_column narrowing to NOT NULL with no retype becomes, on 12 and later, a NOT VALID
//     IS NOT NULL check, its VALIDATE bare, SET NOT NULL, and the check dropped;
//   - add_index becomes CREATE INDEX CONCURRENTLY, bare;
//   - drop_index becomes DROP INDEX CONCURRENTLY, bare (M13-T11, ADR 0092);
//   - add_unique becomes CREATE UNIQUE INDEX CONCURRENTLY, bare, then ADD CONSTRAINT ... UNIQUE
//     USING INDEX, a catalog change;
//   - add_primary_key becomes, on 12 and later, a validated IS NOT NULL check per key column, the
//     concurrent unique build, ADD PRIMARY KEY USING INDEX, and the checks dropped -- measured over
//     3 million rows, the attach scanned for 87ms without the checks and took 1.8ms behind them.
//
// Every concurrent statement and every VALIDATE is Statement.Unblocking, so the engine bounds it by
// safety.concurrent_timeout rather than safety.statement_timeout: 30 seconds stopped every build of a
// large index, which then left an INVALID one.
//
// Measured on 16 and 17 over 15 million rows: the plain ADD CHECK blocked 34 of 35 writes for its
// 9.5s scan, and the pair blocked none. The pair inside one transaction blocked 34 again, because
// the ADD's lock is held to commit, which is why VALIDATE runs outside one. SET NOT NULL took 0.7ms
// behind a validated check against 367ms without one.
//
// A concurrent build that fails leaves its index INVALID, and a concurrent drop that fails leaves its
// index in place and INVALID. Exec reads pg_index afterwards on the session that is still alive (ADR
// 0083) and returns InvalidIndexError naming the index. repair.go is the driver.Repairer the engine
// asks next (M13-T11, ADR 0092): after a failure it drops a build's leftover so the build can run
// again, and lets a drop or a VALIDATE simply run again; before a resume, which may follow a killed run
// that repaired nothing, it rebuilds a leftover with REINDEX INDEX CONCURRENTLY where the leftover is
// exactly the index the operation builds -- its table, its columns in order, its uniqueness -- drops
// any other, and skips a build or drop it finds already done. A failed REINDEX leaves <name>_ccnew
// INVALID beside the original, and both are dropped. InvalidIndexes lists every INVALID index the
// namespace holds, and Introspect marks each one -- schema.Index.Invalid and a marker -- so a name it
// holds is not taken and migr diff declines to compare it. Only a raw operation declaring
// no_transaction (ADR 0084) and these rewrites set Statement.NoTransaction, and a raw one's concurrent
// statement is repaired too, never reindexed, since nothing says which index it builds.
//
// alter_column emits one statement per requested change, in a fixed order: type, then nullability,
// then default. That is not cosmetic — SET NOT NULL against a column being retyped has to run after
// the retype — and it is what makes the golden file reproducible.
//
// Every statement this driver composes ends in a semicolon. A raw operation does not: run verbatim
// is the whole contract, and the author may already have written one. A view query is trimmed of
// its own terminator before being embedded, because pgx sends through the extended protocol, which
// rejects more than one command.
//
// # One pinned session, and no pool
//
// Open returns a single pgx connection. The task that specified this asked for connection
// pooling, and it does not do that, because nothing would draw from the pool: the engine migrates
// serially under one lock, and a capture stream is a replication connection opened from the
// Driver (ADR 0014) which would not share it either. pgxpool defaults to four connections, and
// holding four backends to use one is a cost operators notice — max_connections is a budget.
//
// The contract's rule survives regardless: an advisory lock belongs to a backend, so the session
// M3-T4 locks on must be the session that releases it. Pooling arrives when a second consumer
// does, and pinning is what it will have to preserve.
//
// # A connection string is never rendered
//
// pgx redacts passwords from its own error text on a best-effort basis, and its source says so:
// above the redaction, ParseConfigError.Error carries a comment that returning a static string
// "would ensure that the error message cannot leak a password". It is right. The keyword/value
// fallback matches password=[^ ]*, so a password containing a space keeps its tail —
//
//	host=localhost port=notanumber password=my secret
//	  -> cannot parse `host=localhost port=notanumber password=xxxxx secret`
//
// — and the URL branch that would have handled it only runs when url.Parse succeeds, which is
// exactly what fails when the string is unparsable. So parseDSN wraps the failure in a DSNError
// that renders none of it, and unwraps to the pgx cause for errors.Is and errors.As. The
// diagnosis survives; the text does not.
//
// # The health check is the version detection
//
// One query for server_version_num proves the session works and returns the fact M3-T3 and
// M13-T1 need, so there is no separate ping. The display string is derived from that number
// rather than read from server_version, which reports "17.2 (Debian 17.2-1.pgdg120+1)" — text
// nobody can compare. Deriving both from one detection is what keeps ServerVersion and
// capability.Facts.Server from disagreeing, which the driver contract requires.
//
// Servers older than PostgreSQL 10 are refused, because the major*10000 + minor encoding the
// version type reads does not hold below it. That refusal prints the raw number rather than a
// derived release, since the formula is the thing that does not apply.
//
// # search_path needs more than a bound parameter
//
// The schema from migr.yaml is set through set_config with a bound parameter — and that alone is
// not enough. A bound parameter stops SQL injection, but search_path is a list-valued setting, so
// PostgreSQL parses whatever value it receives as comma-separated entries: a schema of
// "public, pg_catalog" silently becomes two of them, which is a privilege surface rather than a
// typo. quote_ident makes it one identifier, so an injected value names a schema nobody has.
//
// This matters because internal/config validates a schema for nothing at all — not identifier
// legality, not length, not commas — and it reaches the session before any migration runs. An
// integration test asserts both halves: an ordinary schema is set plainly, and an injected one
// stays whole.
//
// # A string literal is written in, not bound
//
// DDL takes no parameters, so a column default's literal value is concatenated into the statement
// and doubling ' is the whole defense. That defense is complete only while
// standard_conforming_strings is on: with it off, a backslash starts an escape sequence and the
// doubling can be escaped past. It has been the default since PostgreSQL 9.1 and is deprecated,
// but Compile is pure and cannot read a session setting — so Open pins it rather than trusting it,
// alongside search_path.
//
// # Replica lag is read on the primary
//
// ReplicaLag reads pg_stat_replication (M13-T8, ADR 0088), and the order its rules are read in was
// measured on 16 against a standby replaying three seconds late: a standby with no WAL outstanding is
// caught up whatever replay_lag says, since that column keeps its last measurement for about ten
// seconds after the writes stop; with WAL outstanding replay_lag is the answer, and outstanding with
// none measured is unknown; a NULL state is a user without pg_read_all_stats, which is unknown too.
// replay_lag trails the writes it measures, climbing a second a second while they go on, so a burst
// shorter than the threshold is never held. databases.<name>.replicas is refused: the primary
// already reports every standby, and a list an operator keeps would miss the one they forgot.
//
// # Whether this server permits log-based capture
//
// Open reads wal_level, whether the role is a superuser or has REPLICATION, and how many replication
// slots max_replication_slots leaves free, into capability.Facts.Capture (M13-T16, ADR 0090). A
// failed read is read as not permitted. The fact's Retention reads max_slot_wal_keep_size, since that
// is what bounds carrying a stopped migration on.
//
// # A shadow table, through a replication slot
//
// Capture opens a stream on a connection of its own: pgoutput through a publication of the one
// table and a logical slot of the same name, read by polling pg_logical_slot_peek_binary_changes up
// to where the WAL ended as the read began and parsed with pglogrepl (M13-T15, ADR 0094). The slot
// is advanced only on Acknowledge, after the engine's record commits; a fresh start drops a slot and
// publication of its derived name first, which is how a start killed before its record is cleaned.
// The session is a driver.Shadower -- LIKE ... EXCLUDING INDEXES with every index recreated under a
// derived name, a FOR SHARE copy, an idempotent replay, and a cutover in one transaction that swaps
// the index names and the tables -- and a driver.CaptureKeeper, listing the migr_ slots and the WAL
// each retains. shadow.go, shadow_catalog.go, stream.go, pgoutput.go and slots.go hold it.
//
// Three things keep a read's cost to one batch, each measured on a CI runner that never caught up
// without it. A peek, and an advance, decode from the slot's restart point rather than from where
// migr confirmed, and that point moves only past a RUNNING_XACTS record the background writer logs
// every 15 seconds: so before each advance the stream logs one itself with pg_log_standby_snapshot,
// which PostgreSQL 16 adds and which needs superuser or an EXECUTE grant -- refused, it is not asked
// again and the slot moves at the server's own pace. Measured under eight writers, the restart point
// trailed the WAL end by 24 MB after 7 seconds without it, and by at most 1 MB with it. A peek asks
// for a batch's worth of messages past those it already consumed, rather than the whole backlog. And
// a batch is replayed in one pipelined round trip.
package postgres
