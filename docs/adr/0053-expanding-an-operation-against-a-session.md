# 0053. An operation a driver compiles against the database, rather than against nothing

## Status

Accepted

## Date

2026-09-29

## Context

`Driver.Compile` is pure by contract — no context, no connection ([ADR 0011](0011-driver-contract.md))
— and every consumer is built on that: `internal/plan` holds a `driver.Compiler` and no session,
`migr validate` opens no database at all ([ADR 0028](0028-validate-without-a-database.md)), and the
driver conformance suite asserts `Resolve(op).Allowed()` is true exactly when `Compile` succeeds.

SQLite's table rebuild breaks the premise. Changing a column, or adding or dropping any constraint,
means creating a replacement table carrying the original's **whole definition** plus the change —
and the only place that definition exists is `sqlite_schema.sql`, which is a read. M8-T3 could
therefore not declare those operations `Emulated`: `Emulated` is an *allowed* verdict, and declaring
one while the compiler refuses would break the biconditional ([ADR 0052](0052-sqlite-lock-verdicts.md)).

So something has to carry a live schema to the thing that produces statements. This is the ADR
M8-T3 deferred, written now that the rebuild exists to shape it.

## Decision Drivers

- CLAUDE.md §4.2: a capability is never silently degraded — an emulation is surfaced in `migr plan`,
  so whatever the mechanism is, the statements have to be visible before they run.
- `migr validate` is the CI gate and opens nothing. Whatever it cannot judge it must **say** it
  could not judge, rather than passing it or failing it ([ADR 0040](0040-running-a-go-migration.md)).
- The conformance suite is the only mechanical check that a driver's two answers agree, and a driver
  does not get to fork it — so if the rule changes, it changes for every driver at once.
- PostgreSQL and MySQL emulate nothing. They should pay nothing for this.

## Considered Options

### A. What an expansion returns

**A1. Statements.** The session produces `[]driver.Statement`, which the planner puts into the step
in place of what the compiler would have produced.

**A2. Neutral operations.** The session decomposes one operation into `ir.Operations`, and the engine
compiles each of those through the ordinary path — which would keep everything inside the IR and
reuse the compiler for the parts.

**A2 is closed by a fact rather than a preference.** `ir.CreateTable` carries columns and a primary
key and nothing else: the v1 IR has no way to express a `CHECK` or a `FOREIGN KEY` *inline in a new
table*. A shadow table built from IR would therefore drop every check and every key the original
carried — which is precisely the thing M8-T4's done-when says must survive. The IR is the neutral
middle for what a migration **means**; a rebuild is how one engine performs it, and its shadow table
is an implementation detail that was never neutral.

### B. How the planner learns a session is needed

**B1. The compiler says so.** `Compile` refuses with `driver.ErrNeedsSession` instead of
`driver.ErrUnsupported`, and `plan.Planner` gains an optional `Expander` and a `context.Context`.
The two error kinds are the two things a driver can mean by not returning statements: *I will not do
this* and *I cannot do this from here*.

**B2. The engine reads the capability verdict.** `internal/plan` stays untouched, and the engine
asks the session to expand wherever `Resolve(op)` says `Emulated`.

**B2 costs `migr validate` the ability to tell the two apart.** Validate has no session and therefore
no capability set; the only thing it can see is an error, and under B2 an emulated operation's error
is indistinguishable from a permanent refusal. It would keep failing every migration that uses one —
exit 2 on a migration the driver performs perfectly.

### C. What `migr validate` reports

**C1. Unchecked, with the driver's reason**, reusing `validate.Unchecked` — which ADR 0040 built for
Go migrations that read the database, and which is the same claim about a different half of the same
command. `Unchecked` gains `Operation` and `Kind`, since this is one operation rather than a whole
direction.

**C2. A problem**, as before. **C3. Silence**, letting it pass.

### D. What the conformance suite asserts

**D1.** `Allowed()` is false exactly when `Compile` refuses **and does not ask for a session**; an
operation that asks must be declared `Emulated`, and its session must satisfy `driver.Expander`.

## Decision Outcome

**A1, B1, C1, D1.**

```go
// internal/driver
var ErrNeedsSession = errors.New("this operation needs a database to compile")

type Expander interface {
    Expand(ctx context.Context, op ir.Operation) ([]Statement, error)
}
```

`Expander` is an **optional** interface a `Conn` may satisfy. `internal/cli` passes the session to
the planner on the run paths and passes nothing on `migr validate`'s; `expanderOf` returns nil for a
driver that is not one, so PostgreSQL is untouched and its conformance run is unchanged.

The three answers a compiler can give are now:

| `Compile` returns | means | the declaration says | `migr validate` reports |
|---|---|---|---|
| statements | it does this itself | `Native` | judged |
| `ErrUnsupported` | it will not do this | `Unsupported` | a problem |
| `ErrNeedsSession` | it cannot do this from here | `Emulated` | **unchecked**, with the reason |

**`internal/plan` is no longer database-free**, which [ADR 0021](0021-apply-executor.md) recorded as
a consequence and this supersedes. The claim that survives is the one that mattered: `migr validate`
still opens nothing, and now says per operation what that cost it. The claim that does not is
"`migr plan` compiles the whole queue with no database" — a preview of a SQLite emulation genuinely
needs a session, and `migr plan` has always had one.

## Consequences

### Positive

- A driver can emulate an operation and still be held to the biconditional, which is what M8-T3 had
  to defer and what every later emulation inherits.
- **`migr validate` stopped failing migrations the driver performs.** Against a `driver: sqlite`
  project, a migration using `alter_column` was exit 2 before this change and is exit 0 with an
  unchecked entry after it — and the entry names the operation, the direction, and the strategy.
- The whole rebuild is in the plan, statement by statement, because an expansion returns the same
  thing a compilation does. Nothing has to be taught to render it.
- M13-T5's `auto_rewrite` has a seam that already exists, and the same question answered: a rewrite
  the operator must be able to see is one that goes through `plan.Step`.

### Negative

- **`Planner.Compile` and `Planner.Step` grew a context**, which touched the engine, validate, the
  CLI and their tests. It is the smallest change that lets an expansion be canceled.
- **An expansion is not reproducible the way a compilation is.** The same operation against two
  databases produces different statements, so nothing an expander produces can be a golden file —
  the golden for such an operation records that it needs a session, and the statements are proved by
  live tests instead.
- **`migr validate` now reports something on a healthy SQLite project**, where it used to print
  nothing but the count. That is the honest answer and it is noisier than silence.

### Neutral

- PostgreSQL implements no `Expander` and nothing asks it to. Its conformance run, its goldens, and
  its plan output are byte-identical to before.
- The `-- migr:needs-session` golden marker sits beside `-- migr:unsupported`, so an operation that
  starts or stops being emulated arrives as a diff.

## Rejected

**A2, returning neutral operations.** Attractive — it would keep the decomposition inside the IR,
show every step in `migr plan` for free, and reuse the compiler. It cannot work: the shadow table
needs `CHECK` and `FOREIGN KEY` clauses that `ir.CreateTable` cannot express, so the rebuild would
silently drop exactly what it exists to preserve. Reconsider it if the IR ever grows a `create_table`
that carries its constraints — at which point this is worth revisiting for `migr plan`'s sake alone.

**B2, reading the verdict in the engine.** It keeps `internal/plan` pure and breaks `migr validate`,
which is the command this project points CI at. A CI gate that fails a correct migration is worse
than one that admits it did not check it.

**C2, keeping it a problem.** It is what the code did before this change, and it was wrong the
moment the driver could perform the operation.

**C3, passing it silently.** ADR 0040 settled this: a half nothing judged must not be
indistinguishable from one that passed.

**Putting `Expand` on `Driver` rather than on `Conn`.** The connection is the whole point; a driver
is constructed once per process and may open sessions against several databases.

## Notes

The natural next question is whether an expansion should be able to *read through the engine* — a
`driver.Conn` already has `Query`, and an expander reaching around it means a driver's own reads are
not subject to anything the engine knows about. SQLite's reads here are against `sqlite_schema` on
the session's own pinned connection, which is the same connection the migration runs on, so nothing
is hidden. A driver that needed to read data rather than schema would be worth stopping to think
about.

M11-T1's `Conn.Introspect` and `schema.Snapshot` may make some of this unnecessary: an expansion that
could work from a neutral snapshot would not need the driver to parse its own catalog. That is M11's
to decide, and the seam here does not prevent it.
