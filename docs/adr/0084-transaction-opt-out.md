# 0084. A statement may declare it runs outside a transaction, and the migration runs in parts around it

## Status

Accepted

## Date

2026-10-17

## Context

M13-T4 asks for three things:
- an operation can declare it must run outside a transaction;
- the executor splits the migration around it and records partial progress through M4-T9's
  dirty-state model;
- `CREATE INDEX CONCURRENTLY` runs end to end on PostgreSQL.

What stood in the way:

- **Nothing could declare it.** `driver.Statement.NoTransaction` has existed since M3-T5, and no
  driver ever set it. `ir.Raw` was `{driver, sql}`, so a `.sql` migration could not express a
  concurrent index build at all: the statement ran inside the transaction the executor opened, and
  PostgreSQL refused it with `25001`.
- **The executor refused the shape.** `checkAtomic` refused a migration of more than one run, and one
  whose only run was not transactional, with `ErrNotAtomic`. It did so from `prepare`, so `migr up`,
  `migr down` and `migr plan` all refused.
- **The loop was gone.** M11-T7 (ADR 0071) made one transaction bracket the whole step loop, so that
  an operation compiled against the database reads what the steps before it left. It deleted the
  multi-run loop ADR 0017 had left written, so this task wrote one.
- **A deferred step's split points are unknown until it is expanded**, which M11-T7 left as this
  task's question.

## Decision Drivers

- A `.sql` migration can build an index concurrently, since `.sql` is the format most projects start
  in.
- After any failure, and after a crash between two commits, the history says how far the migration
  got. Before this, a migration that committed twice could not say so.
- A migration with no such statement behaves exactly as before: the same transaction, the same
  rows, the same bytes, the same checksum.
- What an expansion reads is still exactly what the steps before it left.
- No statement waits unbounded for a lock (invariant #12), and nothing reruns what may have left
  something behind.

## Considered Options

### Option 1: A field on `ir.Raw`, declared per statement; the engine runs the migration in parts

**Pros**

- The declaration is a fact about one statement, which is the unit the server refuses.
- One loop decides it for every driver and both directions; no driver groups.
- The dirty row and the ordinal shift are the ones the chunked runner already writes (ADR 0078).

**Cons**

- It is an IR change, so it needs a version.

### Option 2: A `concurrently` field on `add_index`

**Rejected because:** choosing `CONCURRENTLY` is M13-T11's, which uses it automatically where it is
safe and owns the invalid-index cleanup. A field an author sets would be a second answer to the same
question, decided before that task can decide it.

### Option 3: A file-level marker, as goose does

**Rejected because:** a file that creates a table and indexes it concurrently would have to become
two migrations. With a per-statement marker the split is reachable from `.sql` as well as from the
SDKs.

### Option 4: Refuse a deferred step in a migration that splits

**Rejected because:** it is narrower for no gain. A deferred step belongs to a transactional part and
is expanded inside that part's transaction. What it reads is the parts before it, which committed,
plus its own part's statements, which are sent and not yet committed. That is exactly the schema the
steps before it left. The only shape still refused is an expansion that itself returns a statement
outside a transaction. Such a statement did not exist when the migration was divided into parts, and
it stays `ErrNotAtomic`.

### Option 5: Record progress in the checkpoint table rather than in dirty rows

**Rejected because:** the done-when names M4-T9's dirty-state model. `migr resolve --resume` already
reads a dirty row's `last_statement`, and positions continue across parts, so a resume needs no
second mechanism. A checkpoint is the record for a chunk, which a statement is not.

### Option 6: Emulate with savepoints

**Rejected because:** a statement that cannot run inside a transaction cannot run inside a savepoint
either.

## Decision Outcome

Option 1.

**The IR.**
- `ir.Raw` gains `no_transaction` (bool, omitted when false), and **`irVersion` 3** carries it.
- A version is now a property of an operation's fields as well as its kind. `ir.Since(op)` returns 3
  for a raw carrying the flag and `Kind.Since()` otherwise; `VersionOf` and `CheckCarried` ask it.
- A document stating 2 that carries the flag is refused, naming `raw carrying no_transaction` and
  both versions.
- Both SDKs refuse it before sending, as `unsupported_ir`, against an engine stating 2. An engine
  reading 2 decodes raw strictly, so it would otherwise refuse the field as an unknown member,
  pointing at the migration rather than at the CLI to upgrade.
- Every document without the flag keeps its bytes. The runtime conformance suite holds both SDKs to
  one new fixture.

**Declaring it.**
- `.sql`: the line `-- migr:no-transaction`, alone and directly above the statement it declares.
  - The scanner reads it, so a marker inside a quoted body or a block comment is not one.
  - It is taken out of the statement it declares, since it is migr's and not the server's.
  - A marker that declares nothing is refused, naming its line: inside a statement, twice in a row,
    with no statement after it, above the up marker, or below a declaration.
- Go: `s.Raw(driver, sql).NoTransaction()`. TypeScript: `.noTransaction()`. Each marks the statement
  just added.
- Drivers carry the flag as given and set it from nothing else.

**The parts.** Before anything runs, the steps are divided into parts:
- Consecutive statements that may share a transaction are one part, and a deferred step counts as
  one of those.
- A statement that may not is a part of its own.
- A migration with none of these is one part, which is the transaction M11-T7 describes, unchanged.

**What each part writes.**
- Every part but the last writes a **dirty row** naming the last statement it ran. The reason it
  records is "running in parts".
  - A transactional part writes it inside its own transaction, so it commits with the statements.
  - A bare statement writes it immediately afterwards.
- The last part writes the **applied** row, inside its transaction, or bare after the last statement
  where that statement had no transaction. So M4-T5's "a migration with no transactional run has
  nowhere to write its row" is answered by the dirty model, as the task said it would be.
- Each row takes the next ordinal, as a chunked run's rows do (ADR 0078).

**Failures.**
- What survives a failure is what the parts before it committed. Where DDL commits as it goes, it is
  also what the failing part ran before the statement that failed (ADR 0024, unchanged).
- `recordFailure` writes `failed` where nothing survived and `dirty` where something did.
- `DirtyError` gains `Split`, and its message says the migration is not one unit even on
  transactional DDL, rather than blaming the engine's declaration.

**Retries (ADR 0082).**
- A lock wait spent in a transactional part retries **from where what survived ends**, re-expanding
  only the steps the rollback undid. A committed part never runs twice.
- A lock wait spent by a bare statement is **not retried**. Nothing rolled back what it left: measured,
  a failed concurrent build leaves its index behind INVALID, and a second attempt would fail on the
  name. The run stops dirty, or failed if the statement was first, and the error says why.

**Lock timeouts.** These are unchanged and still bound a bare statement:
- PostgreSQL's `lock_timeout` is pinned at connect.
- MySQL's `lock_wait_timeout` is a session variable.
- SQLite's busy timeout is a pragma.

Measured on 16 and 17: a concurrent build blocked by a `SHARE` lock gave up after its 300 ms bound,
once.

**Both directions.**
- Deleting the refusal lifts it for `migr up` and `migr down` at once.
- An inverse is divided independently of what it inverts. A `CREATE INDEX` runs in a transaction and
  its `DROP INDEX CONCURRENTLY` runs bare.
- A resume skips a committed part whole, since positions continue across parts.

**`migr plan`.**
- Marks the statement with `[no transaction]` directly above it, and the migration with how many
  parts it runs in.
- `--json` carries `no_transaction` positions beside an operation's statements, and `parts` beside a
  migration.
- `--dry-run` says "in N parts".

## Consequences

### Positive

- `CREATE INDEX CONCURRENTLY` runs from a `.sql` migration on PostgreSQL 16 and 17. The index is
  valid, the history reads dirty, dirty, applied, and its `DROP INDEX` undoes it.
- SQLite's `VACUUM` runs inside a migration, which is the untagged proof of the split: undeclared,
  SQLite refuses it inside the transaction and the whole migration rolls back.

### Negative

- **One window no transaction closes.** A crash between a bare statement and the row written after it
  leaves the history saying one statement fewer than ran. A crash during a migration's first
  statement, when it is bare, leaves no row at all. Both are the statement's nature rather than this
  executor's, and both are stated.
- **A split migration writes one row per part.** `migr history` shows each dirty row, with the reason
  it was written, before the applied one.
- **A failed concurrent build leaves an INVALID index**, which degrades writes and serves no query.
  Detecting and dropping it is M13-T11's, as is lifting the not-retried rule for the statement that
  task emits itself.

### Neutral

- On MySQL and MariaDB the flag changes where the rows are written and nothing about what survives,
  because every DDL statement there commits on its own already.

## Notes

The baseline was measured before anything was built. On PostgreSQL 16 and 17, a `.sql` migration
holding a `CREATE INDEX CONCURRENTLY` and no marker is refused with `25001`, and nothing it created
survives. That is now the control in each driver's suite, beside the test it makes meaningful.
