# 0054. The SQLite table rebuild, as the measurements made it

## Status

Accepted

## Date

2026-09-29

## Context

SQLite's `ALTER TABLE` renames a table, renames a column, adds a column and drops one, and does
nothing else. Seven operations — `alter_column`, both foreign-key operations, both checks and both
primary-key operations — have refused since M8-T2 because of it. Performing them means replacing the
table with one carrying a changed definition and moving the rows across, which SQLite documents as a
12-step procedure.

[ADR 0053](0053-expanding-an-operation-against-a-session.md) settles how a driver is allowed to
compile such an operation against a live database. This ADR is what the rebuild turned out to be,
and **almost none of it is the documented procedure**, because three measurements against
`modernc.org/sqlite` v1.59.0 (SQLite 3.53.4) contradicted the plan this task started from.

## Decision Drivers

- The done-when: *an integration test proves no referencing object is lost* — indexes, triggers,
  views and foreign keys.
- The engine runs a migration inside one transaction ([ADR 0017](0017-transaction-boundaries.md)),
  and M8-T1 measured that `PRAGMA foreign_keys` is a **silent no-op** inside one — so SQLite's own
  step 1, "turn foreign keys off", is unavailable.
- A rebuild that quietly loses a constraint leaves a schema that looks right and enforces nothing,
  which is the failure class this project treats as most severe.

## What the measurements said

**1. `DROP TABLE` fires `ON DELETE` actions on children, and `defer_foreign_keys` does not stop it.**

```
users has one row; orders references users ON DELETE CASCADE and has one row
inside one transaction, with PRAGMA defer_foreign_keys = 1:
  ... DROP TABLE users ... recreate ... commit
after: users 1 row, orders 0 rows, transaction committed successfully
```

`ON DELETE SET NULL` is the same shape and quieter: the child row survives with its key column
silently set to NULL. Deferral holds the **check** until commit; it does not hold the **action**.
There is no pragma that does, inside a transaction.

**2. A deferred violation does fail the `COMMIT`.** An orphan inserted under `defer_foreign_keys = 1`
made the commit fail with `FOREIGN KEY constraint failed`. So deferral is load-bearing and works for
exactly the case it is there for: while the table is gone, every key pointing at it is violated, and
the check is postponed to a commit by which time it is back with the same rows.

**3. Every order that uses `ALTER TABLE … RENAME` corrupts something.** At 3.25+ a rename rewrites
references to the table throughout the schema, and it re-parses the whole schema while doing it:

| order | what happened |
|---|---|
| shadow → copy → drop original → **rename shadow into place** | fails outright: `error in view listing: no such table: main.users`, because the view is dangling at the moment of the rename |
| the same, having dropped the views first | works, but the child's `REFERENCES` survived only because nothing renamed the *original* |
| **rename original out of the way** → create → copy → drop | the child's foreign key was rewritten to point at `migr_rebuild_users`, which the rebuild then drops |

`PRAGMA legacy_alter_table` **does** take inside a transaction — unlike `foreign_keys` — and does
**not** prevent the foreign-key rewrite. It is not a way out.

## Decision

**Rebuild without renaming anything.** The shadow table holds the rows across the one moment the
original cannot exist, and the replacement is created under the real name:

```sql
PRAGMA defer_foreign_keys = 1;
CREATE TABLE "migr_rebuild_users" ( …the changed definition… );
INSERT INTO "migr_rebuild_users" ("id","email") SELECT "id","email" FROM "users";
DROP TABLE "users";
CREATE TABLE "users" ( …the changed definition… );
CREATE INDEX …;                                   -- every index, before the rows come back
INSERT INTO "users" ("id","email") SELECT "id","email" FROM "migr_rebuild_users";
DROP TABLE "migr_rebuild_users";
CREATE TRIGGER …;                                 -- every trigger, after
```

Nothing is renamed, so **nothing re-parses the schema** — which is why views survive untouched and
need no dropping and recreating, and why the children's `REFERENCES` clauses still name the table
they always named. The price is a second copy of the rows, and it is worth it.

**The order of the restorations was a measurement too.** Indexes go back **before** the rows,
because a foreign key pointing at this table needs its unique index to exist or the copy fails with
`foreign key mismatch`. Triggers go back **after**, or an `AFTER INSERT` trigger fires once per row
for rows that were already there.

**A rebuild is refused where a child's key would take its rows with it.** Before anything is emitted,
every table referencing this one is read out of `pragma_foreign_key_list`, and a rebuild is refused
when any of them declares `ON DELETE CASCADE`, `SET NULL` or `SET DEFAULT`. The refusal names the
child table and the action, and says what to do: drop the foreign key, rebuild, add it back.
`NO ACTION` and `RESTRICT` are fine, because deferral covers exactly the case they are.

**The definition is re-rendered from `sqlite_schema.sql`, and everything the operation does not
target is copied byte for byte.** The body between the outermost parentheses is split on the commas
that separate its items — respecting `'…'`, `"…"`, `[…]`, `` `…` ``, `--` and `/* */`, and nesting —
and each item is classified only far enough to answer *is this the one the operation names*.
`GENERATED ALWAYS AS`, `COLLATE`, a column-level `REFERENCES … DEFERRABLE`, `AUTOINCREMENT` and the
`WITHOUT ROWID` / `STRICT` suffix all survive a rebuild without this driver understanding any of
them. `alter_column` is the only operation that edits *inside* an item, and it edits the clauses it
was asked about rather than re-rendering the column.

**A rebuild refuses rather than guessing** when: the table is not there or SQLite records no
`CREATE` for it; the definition cannot be split; the operation names a constraint or column the
definition does not carry; `add_primary_key` meets a table that already declares one; or the shadow
name is taken.

## Consequences

### Positive

- The seven are `Emulated("table rebuild", table_rewrite)`, which is what M8-T3 measured the cost of
  and could not yet declare.
- **The done-when is proved by violation, not by inspection.** A table with an index, a unique index,
  a trigger, a view, a check, a key out and a key in is rebuilt seven ways; afterwards every object
  is compared against a before digest, and each constraint is tested by being broken.
- Views need nothing done to them, which is one whole documented step this rebuild does not have.
- The splitter is small enough to be worth trusting, because it understands almost nothing.

### Negative

- **A rebuild copies the rows twice.** That is what avoiding `ALTER TABLE … RENAME` costs, and it is
  already `table_rewrite` on the lock axis, so the verdict does not change.
- **A table whose children cascade cannot be rebuilt at all.** The refusal is the honest answer and
  it is a real limitation: the operator has to drop the key, rebuild, and add it back — three
  migrations where one would do.
- **A column's implicit rowid is not preserved** for a table with no `INTEGER PRIMARY KEY`, since the
  copy names columns. Anything referencing such a row by rowid would be left pointing elsewhere.
- **`migr plan` against a real SQLite database cannot show any of this yet**, because a preview takes
  the migration lock and that is M8-T5. The rendering is proved through `internal/cli`'s fake.

### Neutral

- Nothing here is reachable except through the seven operations the driver declares emulated, so a
  driver that emulates nothing is unaffected by every line of it.
- `internal/driver/sqlite` grew a small SQL reader, which M11-T1's `Introspect` may one day replace.

## Rejected

**SQLite's documented order** — shadow, copy, drop, **rename** — with views dropped and recreated
around it. It works, and it is one rename away from corrupting a schema: it survives only because
nothing renames the original, and a reader would have to know that to keep it true.

**`PRAGMA legacy_alter_table` around the rename.** It takes inside a transaction and does not stop
the foreign-key rewrite, which is the half that matters.

**Rebuilding from the pragmas** rather than from the recorded SQL. `table_info`,
`foreign_key_list` and `index_list` give columns, keys and indexes — and SQLite exposes **no pragma
for CHECK constraints**, so every rebuild would silently drop every check, including the ones an
`enum` column compiles to ([ADR 0051](0051-sqlite-type-map-and-the-rebuild-boundary.md)).

**Refusing any definition this driver cannot fully parse.** Copying an untouched item through
verbatim preserves more, needs less, and cannot lose something by failing to understand it.

**Rebuilding the children too**, so a cascading key could be handled. One operation would become an
unbounded number of table rebuilds across tables the migration never named, each of them able to
fail. That is not an emulation of the operation any more.

## Notes

Revisit the cascade refusal if SQLite ever gains a transaction-scoped way to suspend referential
*actions* — `defer_foreign_keys` suspends only the checks today, and that is the whole of the
limitation.

Revisit the double copy if a rename ever becomes safe: `ALTER TABLE … RENAME` with reference
rewriting disabled for foreign keys, not just for triggers and views, would allow the one-copy order.
