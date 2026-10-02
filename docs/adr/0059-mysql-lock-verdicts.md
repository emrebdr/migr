# 0059. MySQL's lock verdicts, and the two things a server will say about its own ALTER

## Status

Accepted

## Date

2026-10-02

## Context

M9-T2 left every operation this driver compiles declared `Native(LockUnknown)` or
`Emulated("column redefinition", LockUnknown)`. The support axis was finished across three earlier
tasks — `TransactionalDDL: false` and the native enums in M9-T1 ([ADR 0057](0057-the-mysql-session.md)),
three version gates in M9-T2 ([ADR 0058](0058-compiling-mysql.md) §G) — and "no partial indexes",
the fourth half of M9-T3's done-when, needs no declaration at all: `ir.AddIndex` carries no
predicate, so nothing can request one, and [ADR 0016](0016-lock-impact-is-a-duration.md) already
recorded that *"a field recording a server capability that no operation can reach is a constant
nothing reads."*

So this task is the lock axis, which is also the axis M9-T3's own done-when gets wrong.

**The axis MySQL is the canonical example of.** CLAUDE.md §4.2, `capability/doc.go` and ADR 0012 all
cite MySQL's choice between `INSTANT`, `INPLACE` and `COPY` as *the* reason a verdict cannot come
from a table keyed by operation name. That is true, and it is true in a way the citation does not
say: the algorithm depends on the column's **current** type, and `Set.Resolve` performs no I/O.

**What MySQL offers that PostgreSQL does not.** Told which algorithm to use, the server answers
`1845`/`1846` when it cannot. That makes "is this metadata-only" a question the engine itself
answers, where ADR 0016 had to concede that *"the `BriefExclusive` / `LongExclusive` boundary is not
observable"* on PostgreSQL. And InnoDB gives a table a new `TABLE_ID` when it rebuilds it, which is
the `relfilenode` oracle one engine over.

## Decision Drivers

- Invariant #13: lock impact is **measured, not assumed**, and a driver reporting the wrong verdict
  is worse than one reporting `Unsupported` — it turns a "safe" migration into an outage.
- ADR 0016 fixed what the four verdicts mean — the **duration** of the lock and what it scales with,
  not the lock's level — and every driver's declaration follows from that.
- `Set.Resolve` takes no context, performs no I/O and returns no error (M2-T2). That is what lets
  `migr plan` annotate a plan and the version-dependent cases be tested with no database.
- CLAUDE.md §11: if a task turns out to be wrong, fix the task rather than quietly doing something
  else.

## Considered Options

### A. `INSTANT` is `BriefExclusive`, and M9-T3's own mapping was wrong

The done-when reads *"`INSTANT` is `LockFree`, `INPLACE` is roughly `BriefExclusive`, `COPY` is
`TableRewrite`"*. ADR 0016 had **already rejected** that reading one driver earlier, for PostgreSQL's
`ADD COLUMN ... DEFAULT` on release 11 — universally described as "instant", and declared
`BriefExclusive` because:

> It would be a claim that the operation cannot block anything, which is false — it requests
> `ACCESS EXCLUSIVE` like every other `ALTER TABLE`, and `--require-safe` with a `lock_free` ceiling
> would then pass an operation that can still take a busy table offline while it waits for the lock.

Every word of that holds for `ALGORITHM=INSTANT`. The statement is metadata-only *once it holds its
lock*; it still requests an exclusive metadata lock and still queues behind a long-running `SELECT`,
with every later query on that table queued behind it — invariant #12's whole subject.

So **`LockFree` stays the two operations that touch nothing that already exists**: `create_table`,
and `create_view` without `or_replace`. The same two as PostgreSQL, for the same reason. The task's
sentence is corrected in `TASKS.md` rather than honoured.

### B. Two instruments, each one-directional, and what neither settles

| Instrument | Proves | Cannot say |
|---|---|---|
| `INNODB_TABLES.TABLE_ID` moved (MariaDB: `INNODB_SYS_TABLES`) | the table was **rebuilt** | anything about an operation that did not rebuild |
| `ALGORITHM=INSTANT` accepted | the work is **metadata-only** | that a refusal means a rewrite — `INPLACE` covers both an index build and a rebuild |

The rewrite oracle carries ADR 0016's assertion over verbatim: **nothing rewrote that was not
declared a rewrite**, with over-reports logged by name rather than failed. The id is read back under
whatever name the table then *has*, because a rename keeps the id and changes the name — the false
positive PostgreSQL's probe avoids by looking a table up by oid.

The algorithm instrument fills the gap ADR 0016 left open on the other engine: every operation
declared `brief_exclusive` is held to being metadata-only *by the server*, and where `INSTANT` is
refused the fallback — accepted `INPLACE` with no rebuild — is **logged, not counted as proof**,
because `add_index` is exactly that and reads every row. Measured, two operations land there:
`drop_unique` on both servers, and `drop_foreign_key` on MySQL alone.

`ALGORITHM` is **asked for in a test and never compiled into a statement migr sends.** Emitting it
would make migr refuse DDL the server would have performed — a table that has exhausted its 64
instant column versions, say — and turn the declaration into a demand with no operator opt-out. That
is M13-T2's call to make deliberately, not a side effect of classifying.

### C. `alter_column` splits by payload, and the catalog row `Resolve` cannot reach

M9-T3's note says *"the expansion already reads the column — so the verdict this task resolves and
the statement M9-T2 builds come from the same catalog row."* **They cannot.** `Expand` has that row;
`Resolve` performs no I/O and is not reachable from it.

What *is* decidable from the payload is better than it looks. `restate` carries the catalog's type
and nullability through verbatim when the operation names neither, so an `alter_column` naming only a
default produces a `MODIFY COLUMN` that changes **only the default** — and that is `INSTANT` on both
servers, measured, including on a `longtext` where `SET DEFAULT` is error 1101 and the redefinition
is the only statement there is. So:

| `ir.AlterColumn` payload | Verdict |
|---|---|
| `DropDefault` alone (the `Native` half) | `BriefExclusive` |
| `Default` alone (`Emulated`) | `BriefExclusive` |
| `Type` or `Nullable` present | `TableRewrite` |

One `ir.Kind`, two lock verdicts — the shape M9-T2 gave the same operation's support axis.

A retype is declared a rewrite **even where the server would not rewrite**: MySQL widens a `varchar`
with `INPLACE` and no rebuild and MariaDB does it instantly. Telling a widening from a narrowing
needs the column's current type, which the operation carries only as `from` — what an author
recorded, not what the column is. ADR 0016 rejected keying the verdict to it *on merit*, and named
the condition for revisiting: introspection supplying the real type at plan time. MySQL's `Expander`
is the first place in the project where that input exists, and **M13-T6** — which already plans to
make a `capability.Set` a `Planner` field — is where it could be used. Moving a verdict onto the
expansion path is a contract change across three drivers and is not this task's.

### D. Two version gates, and only where the verdict moves

| Gate | Below it | Measured |
|---|---|---|
| `add_column` — MySQL 8.0.12 | an `INPLACE` rebuild, so `TableRewrite` | instant on 8.0.46 |
| `drop_column` — MySQL 8.0.29 | an `INPLACE` rebuild, so `TableRewrite` | instant on 8.0.46 |

MariaDB has added instantly since 10.3.2 and dropped instantly since 10.4.3, both **below this
driver's 10.5 floor**, so it never reaches either rewrite branch — the shape ADR 0057's
expression-default gate already has. Both MySQL spans are *inside* the supported range, so between
8.0 and 8.0.28 the two variants answer differently for one operation, and neither container can
reach either boundary: they are tested against constructed versions, which is ADR 0052's reading of
invariant #13.

`rename_column` gained `INSTANT` at MySQL 8.0.28 and is `INPLACE` with no rebuild below it. The
**verdict does not move**, so no gate is written. A gate per release would be a table of MySQL's
release notes rather than a declaration.

**Which default a server can store decides `add_column`, and the two disagree.** Measured:

| `add_column` default | MySQL 8.0.46 | MariaDB 11.4.13 |
|---|---|---|
| none, a bare literal, a bare `CURRENT_TIMESTAMP` | `INSTANT` | `INSTANT` |
| any **parenthesised** default — `(5)`, `('x')` on a `longtext`, `(CURDATE())` | `COPY`, rebuilds | `INSTANT` |
| `(UUID())` | refused outright — §G | `COPY`, rebuilds |

`isExpressionDefault` is already exactly the predicate for "this default renders parenthesised"
(ADR 0057), so MySQL's half needed no new concept. MariaDB rewrites only where the value cannot be
one stored constant, which is **the same distinction PostgreSQL draws by reading
`pg_proc.provolatile`**, reached from the other side: there the question is which defaults are
volatile, here which the engine can store.

### E. The three constraints that rebuild, and `foreign_key_checks`

The plan had `add_check` and `add_foreign_key` as `LongExclusive`, by analogy with PostgreSQL, where
both validate under a lock without rewriting. Measured on **both** servers, both are `ALGORITHM=COPY`
and both move the `TABLE_ID`; `add_primary_key` is `INPLACE` and moves it too — an `INPLACE` that
rebuilds. All three are `TableRewrite`.

`add_foreign_key` is `COPY` because **`foreign_key_checks` is on**, which this driver deliberately
leaves alone: MySQL offers `INPLACE` for an added key only when the checks are off, and turning them
off to buy a cheaper verdict would add a key the server never validated.

`add_index` and `add_unique` are `INPLACE` with no rebuild and still read every row, so they are
`LongExclusive` — ADR 0016's settled shape, where the vocabulary has no value for "blocks writers
only" and a fifth would be one more value in `safety.max_lock_impact` than an operator can order.
Their drops are catalog changes and are `BriefExclusive`.

### F. `drop_table` is the one verdict reasoned rather than measured

InnoDB evicts the table's buffer-pool pages and unlinks the tablespace under the metadata lock, and
a large `.ibd` unlink is a known production stall. The attempt to measure it failed: at 13 MB the
net cost was about 15 ms against a ~80 ms harness baseline, which is consistent with scaling and far
too small to be proof, and a larger table exhausted the container's disk.

So the **pessimistic verdict stands** — invariant #13's own instruction — and `drop_table` is
`LongExclusive` here where PostgreSQL calls it `BriefExclusive`. The asymmetry is named rather than
quietly introduced: whether PostgreSQL's answer deserves revisiting is **M13-T1's** question, and a
duration under load is **M13-T12's**.

### G. A uuid default is refused while the server writes a binary log

The probes found a support-axis hole this task closes, because this task's subject is making the
declaration true. On MySQL with `log_bin` on — the 8.0 default, and the image's — this is refused:

```
ALTER TABLE `t` ADD COLUMN `c` char(36) NOT NULL DEFAULT (UUID())
ERROR 1674: Statement is unsafe because it uses a system function that may return
            a different value on the replica.
```

The column is materialised into every existing row (§D), so a replica replaying the DDL would
compute different values. Measured narrowly, and every boundary matters:

- refused on an **empty** table too, and under `binlog_format=ROW`;
- `SET sql_log_bin=0` makes it pass, which is what identifies the binary log as the condition;
- `CREATE TABLE` with the same default is **accepted**, and so are `MODIFY COLUMN` and
  `ALTER COLUMN ... SET DEFAULT` — only `ADD COLUMN` materialises;
- `(CURDATE())` is accepted, so the condition is the value differing per row and not the syntax;
- MariaDB 11.4.13 started with `--log-bin` **accepts** it, checked against a throwaway container.

So the driver reads `@@global.log_bin` once in `Open` and refuses `add_column` with a uuid default
only on a MySQL that writes one. That is ADR 0016's *"asking beats testing"* — it rejected
hard-coding `gen_random_uuid()` to PostgreSQL 13+ for refusing a configuration that works, and a
blanket refusal here would make the same mistake. The refusal names the fix, which is also measured:
add the column without a default and set it in a second operation.

`facts`/`rules`/`verdict` therefore close over a `serverFacts` holding the version **and** that
fact, which is the shape `internal/driver/postgres` already has. `checkVersion` stays on
`serverVersion`, because it is genuinely about the release.

### H. `PROCESS` in compose, and a privilege the driver never needs

`INNODB_TABLES` and `INNODB_SYS_TABLES` both require `PROCESS`, and the images grant the test user
rights on one database. `test/initdb/grant-process.sql` grants it, mounted by both services.

**No migr deployment needs it.** The driver reads neither table; a verdict is resolved with no I/O at
all. Stated in the file, because a privilege appearing in a test fixture reads as a requirement
otherwise.

Rejected: opening the oracle as `root` from inside the test. It needs no compose change and smuggles
a second credential into a file whose `servers` table is built around one DSN per server with one
environment override — and it hides a requirement of the test environment inside a test.

## Decision Outcome

| Verdict | Operations |
|---|---|
| `LockFree` | `create_table`, `create_view` without `or_replace` |
| `BriefExclusive` | `drop_view`, `create_view` with `or_replace`, `rename_table`, `rename_column`, `drop_index`, `drop_unique`, `drop_foreign_key`, `drop_check`, `add_column` whose default this server stores, `drop_column` from the release that made it instant, `alter_column` naming neither a type nor a nullability |
| `LongExclusive` | `drop_table`, `add_index`, `add_unique` |
| `TableRewrite` | `add_check`, `add_foreign_key`, `add_primary_key`, `drop_primary_key`, `alter_column` naming a type or a nullability, `add_column` and `drop_column` on a release or with a default that rewrites |
| `LockUnknown` | `raw`, permanently |

## Consequences

### Positive

- **All three drivers now classify both axes**, which is what M13-T1 needs in order to hold them to
  one standard rather than finish two of them.
- **Invariant #13 is mechanical on an engine with a daemon**, which SQLite managed in-process and
  PostgreSQL could only do for rewrites. Both instruments run against 8.0.46 and 11.4.13 under the
  `integration` tag, and a verdict moved one rung fails them — checked.
- A support-axis hole closed: `migr up` adding a uuid-defaulted column to a MySQL with binary logging
  used to fail at apply time with the server's own error. It is now refused by the declaration, with
  a reason naming the setting and a fix that was measured.
- Twelve measurements, five of which overturned the plan: `add_check` and `add_foreign_key` rewrite
  rather than scanning, MariaDB drops a column instantly at every supported release, MariaDB stores
  a parenthesised default where MySQL materialises it, and the `INSTANT`/`LockFree` mapping was
  wrong.

### Negative

- **A retype is over-reported on both servers**, deliberately (§C) — it is the one verdict here that
  claims more than the server does. Over-reporting costs a flag and under-reporting costs an outage,
  and the live test logs each over-report by name so the imprecision is visible rather than assumed.
- **`drop_table` is reasoned, not measured** (§F), and it is the one verdict here that diverges from
  PostgreSQL's for the same operation.
- **`alter_column` setting a default on an `int` column is `brief_exclusive` and still goes through a
  full redefinition.** The verdict is right and the statement is dearer than PostgreSQL's
  `SET DEFAULT`; that cost is ADR 0058's, inherited.
- **`Open` performs one more round trip.** `log_bin` is a global variable read once per session.
- An existing compose volume predates `initdb/`, so `docker compose down -v` once is needed to pick
  the grant up. `make clean` does it.

### Neutral

- Nothing outside `internal/driver/mysql` and `test/` changed. `internal/cli` renders the support
  axis and not the lock axis (M13-T6).
- **These verdicts have no CLI surface yet.** `migr plan` and `migr up` on a MySQL project stop at
  the migration lock, which is **M9-T4**, and `migr validate` runs against `driver.Offline`, whose
  set refuses everything by design. The one consumer that would carry a verdict to a migration,
  `host.capability.get`, is reachable only down those paths. `migr validate`, `config`, `status` and
  `history` are unaffected and were re-driven against both servers.
- `Statement.NoTransaction` stays false on every statement, which is ADR 0058 §H and still M9-T5's.

## Rejected

**`INSTANT` as `LockFree`, which is what the task says.** §A. It is a promise `--require-safe` acts
on, and it is false: the statement waits for an exclusive metadata lock like every other `ALTER`.
Taking the task's word would also have given MySQL a different answer from PostgreSQL for one
operation on one piece of reasoning, which is how a cross-driver axis stops meaning one thing.

**Compiling `ALGORITHM=` into the statements migr sends.** §B. Attractive — a server that would
silently do something dearer fails loudly instead — and it moves every golden file, makes migr refuse
DDL that would have worked, and leaves an operator no way to accept the dearer path. M13-T2 is
positioned to decide it with the lock timeout in hand.

**Keying the `alter_column` verdict to `from.type`.** §C, and ADR 0016 rejected it first. It would
make a widened `varchar` read `brief_exclusive` on the strength of what an author wrote down.

**Refusing a uuid default on every MySQL.** §G. One gate, no new session fact, and it rejects a
server with `log_bin` off that really does accept the statement — the mistake ADR 0016 named when it
refused to hard-code `gen_random_uuid()` to a release.

**Recording §G's hole for a later task.** It is a declaration that disagrees with the server, found
by this task's own measurements, in a task whose name is "capability declaration" and whose §7
contract calls the declaration authoritative.

**A `LongExclusive` verdict for `add_check` and `add_foreign_key`**, by analogy with PostgreSQL. The
analogy is wrong: InnoDB rebuilds the clustered index for both, measured on both servers.

**Counting an accepted `ALGORITHM=INPLACE` as proof of a cheap operation.** It proves nothing —
`add_primary_key` is `INPLACE` and rebuilds the whole table. Only `INSTANT` acceptance proves
metadata-only, and the in-place cases are logged.

## Notes

The 64-instant-column ceiling is a known imprecision rather than an oversight: MySQL rebuilds instead
of adding instantly once a table has accumulated 64 instant versions, which is a property of the
table's history and not of the operation, so no pure `Resolve` can see it. Declaring every
`add_column` a rewrite to cover it would make the verdict useless for the operation it is most often
asked about. M13-T12 measures what a server actually does under load; this is the kind of thing it
would find.
