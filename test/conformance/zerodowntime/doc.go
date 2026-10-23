// Package zerodowntime is the suite that holds every driver's lock-impact verdicts to what a client
// actually waits (M13-T12, ADR 0096).
//
// Every verdict before this was held by an oracle of one engine's: a relfilenode or a TABLE_ID that
// did not move, an ALGORITHM=INSTANT the server accepted, a reader held to one lock wait per retry.
// None of them measured a client query. This suite runs each operation against a seeded table while
// eight readers and eight writers keep working it, and asks one question of each direction: did any
// client query take longer than the budget while it ran?
//
// # What a verdict promises
//
// The answer is judged against plan.Step.Lock() -- what the gate reads, assessed as a run assesses
// it -- and the worst step of the direction is what the direction promised:
//
//   - lock_free and brief_exclusive promise that no client waits: a query past the budget fails,
//     naming the operation, the direction, the declared cost and the query that waited;
//   - long_exclusive, table_rewrite and unknown say a client will wait, and pass whatever was
//     measured. A cheap operation declared at its worst is logged as an over-report and passes,
//     because over-reporting costs a flag and under-reporting costs an outage.
//
// The budget is 1.5s, or MIGR_ZERODOWNTIME_BUDGET where it is set to a Go duration; a value that does
// not parse fails the run rather than falling back. It is 1.5s rather than tighter because MySQL's
// lock_wait_timeout takes whole seconds: a brief_exclusive statement there may hold a client for one,
// by design, and a budget under it would fail an honest verdict -- measured, a rename held a write for
// 543ms against an earlier 500ms budget. The worst kept promise anywhere was 206ms. Before any operation the load runs alone,
// and a server whose own noise floor reaches the budget fails, since it cannot be judged at it.
//
// # Every operation, both ways, through the engine
//
// The operations run through engine.Apply and engine.Rollback, with the options migr up builds from
// a default migr.yaml -- every rewrite on, chunking, retries, assessment -- so what is measured is
// what an operator's run would do. Each case applies its setup, runs its change up, and rolls it
// back, both under load; a drop is measured as the down of the create it undoes, so twenty-three
// kinds need twenty cases. A kind no case reached fails the run, which is how a new operation is
// met here the day it is added. A kind the server declares unsupported is a pass, being honest.
//
// The data operations run as data-only migrations, so in chunks; their down is authored and empty,
// none of them having an inverse.
//
// The shadow-table case runs on a server that permits capture: an alter_column through a shadow,
// unattended, so the copy, the hold, the catch-up and the cutover's rename all fall inside the window
// and the rename is held to the brief_exclusive its step declares.
//
// # The load is the driver's
//
// Invariant #3 forbids SQL above a driver package, so this suite composes none: the schema is
// ir.Operations, and every statement a client sends -- the seed, a read, a write, a view's body, a
// raw statement, a drop -- is the Load a driver's own test supplies. The table is wide on purpose:
// a 192-byte pad and an index make a rewrite of SeededRows take several budgets on every engine
// compose runs, so a rewrite declared brief cannot hide inside one -- with the pad indexed as well,
// measured, PostgreSQL 17's retype took 3.6s with nothing else running. add_index indexes (pad, label)
// for the same reason: measured, a plain build over the all-NULL extra column took 112ms and a lying
// lock_free passed, where one over the pad takes 2.2s. Not over n, which every write changes: MySQL's
// online build logs each change to an indexed column, and over (pad, n) failed with error 1799. The pad is a string(255) rather than a
// text, since MySQL refuses to index a text without a prefix length the IR has no field for.
//
// # A brief lock is brief only under a short lock timeout
//
// A brief_exclusive statement still waits for the lock it asks for, and every client behind it waits
// with it, for as long as safety.lock_timeout lets it. Measured, PostgreSQL 16's rename_column queued
// behind autovacuum on the freshly seeded table until autovacuum's one-second deadlock_timeout made it
// yield, and its readers waited 1.0s under the default three-second lock_timeout. So a driver's
// sessions run the suite with LockWait, four fifths of the budget, and a statement stuck behind
// someone else's lock gives up and backs off before a client reaches the budget. It is not less,
// because autovacuum yields to a waiter only after deadlock_timeout, one second: measured, at 750ms
// every attempt of a concurrent build and a VALIDATE on PostgreSQL 16 gave up before autovacuum was
// asked to, and the run stopped after six. MySQL's lock_wait_timeout takes
// whole seconds, so it runs at one. SQLite keeps the default: it has no lock queue, its busy handler
// retrying rather than lining clients up behind a waiting statement, and measured, a 250ms busy_timeout
// let the load's own writes starve migr's BEGIN IMMEDIATE on the history table.
//
// # Two cases are sized by what an engine can finish
//
// Tightening a column to NOT NULL runs on a side table everywhere. Measured, MySQL's online rebuild of
// SeededRows under the load ran 988s and failed with error 1799, its online log full, and an
// unthrottled load starved it outright -- so each client pauses between queries, as a busy application
// rather than a benchmark does. The same change on the loaded table runs only where a session performs
// it as a safer sequence, PostgreSQL's validated check, which is the promise a full table is needed for.
//
// Adding a foreign key is sized the same way. MySQL and MariaDB copy the whole table for it, an honest
// table_rewrite whatever it costs, and on a CI runner MariaDB's copy of SeededRows under the load ran
// past the ten-minute statement bound; PostgreSQL's NOT VALID and VALIDATE is the promise a full table
// proves, so that case alone runs on it.
//
// # Where it runs
//
// It measures latency, so nothing else may share its disk: run beside another driver's suite on one CI
// runner, MySQL's million-row rewrites starved a PostgreSQL read for 4s during a concurrent index build
// that took no lock a read waits for. make test-zerodowntime runs one package at a time, and CI runs
// each driver -- MySQL and MariaDB apart -- on a runner of its own. A server there only for its change
// stream is Suite.ShadowOnly: it runs the shadow-table cases alone, over a side-sized table, since a GTID
// set or a binary log changes how a shadow is captured and no other operation's lock.
//
// The latency clock is the one place this package reads the wall clock, and it says so in the
// directive beside it: nothing here plans, and a latency is a duration of real time.
package zerodowntime
