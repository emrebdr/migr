# 0064. Capability preflight: who reads the declaration, and when

## Status

Accepted

## Date

2026-10-03

## Context

[ADR 0012](0012-capability-resolution.md) built a `capability.Set` inside `Open`, already resolved
against the connected server, and CLAUDE.md §7 calls that declaration **authoritative**: it is *"what
`migr validate` and `migr plan` read before anything touches a database."*

Nothing on the run path has ever read a verdict from it. `internal/engine` calls `Capabilities()`
three times — once for `Driver()`, twice for `TransactionalDDL()` — and `Resolve` is read in exactly
two places: `internal/runtime/host` answering a migration's own `host.capability.get`, and
`internal/cli/plan.go` printing a column. So the declaration was authoritative over a report and
nothing else.

Four measurements shaped this task:

**The hole is reachable on a server in `test/docker-compose.yml`.** MySQL refuses an `add_column`
carrying a `uuid` default outright — error 1674 — while it writes a binary log, which `mysql:8.0` has
on by default (measured: `@@global.log_bin` is 1 on 8.0.46, 0 on MariaDB 11.4.13). `Compile` is pure
and version-blind, so it produces the statement; the declaration refuses it. Driven end to end
*before* this task: `migr validate` reported **"no problems found"**, `migr plan` printed
`add_column unsupported` with **no reason at all** beside the statement it would send, and `migr up`
applied the migration before it, sent the statement, and was refused by the server.

**On an engine whose DDL commits, that costs a dirty database rather than a failed run.** Measured at
the engine level on 8.0.46: a migration whose *second* operation the declaration refuses left the
first operation's table in the catalog and the run recorded `dirty` at statement 1 — which then
refused both directions until `migr resolve`. Every part of that was knowable with no I/O at all.

**The driver conformance suite called every version-dependent refusal a disagreement.**
`checkCompiled` reported *"it compiles, but the driver declares it unsupported; the declaration is
authoritative, so one of the two is wrong"*. Run against four constructed MySQL servers below this
driver's version gates, it produced five such failures: `add_check` and `drop_check` on 8.0.0,
`drop_check` on 8.0.15 and 8.0.18, `rename_column` on MariaDB 10.5.0. M9-T3 had already worked
around it by listing only versions where every gate passes, and
`internal/driver/mysql/conformance_test.go` said so out loud: *"a refusal reaching one fixture too
many fails here."*

**A zero `capability.Set` refuses everything.** `Resolve` on one answers *"an unbuilt capability set
declares no capability for X"*, and `driver.Offline` builds exactly such a set on purpose. So
"absent" and "refuses everything" are one value unless a caller distinguishes them.

## Decision Drivers

- M10-T2's done-when: every operation in the pending set is checked against the driver's capabilities
  **before any execution**, and unsupported ops fail validation **with the driver's stated reason**.
- [ADR 0028](0028-validation-report.md) B1: `migr validate` opens no database, which is *"the one
  command CI can run on a pull request with no secret"* — and B3, a connecting mode *"rejected on
  timing, not merit … worth revisiting once M10 has filled in the offline checks."*
- ADR 0028 B2's rule: *"a gate that silently skips a check because the DSN was wrong reports a pass
  it did not earn."*
- [ADR 0040](0040-running-a-go-migration.md), C: a migration that reaches the database is reported
  **unchecked**, because *"rejecting a migration for reading would have a CI gate refuse what
  migrations are allowed to do."*
- [ADR 0063](0063-destructive-operations.md): one policy, two callers; a report marks and a run
  refuses; a resume is not gated.
- [ADR 0029](0029-exit-codes-and-failure-output.md): *the argument is wrong → 2*, *the world is not
  ready → 1*.

## Considered Options

### A. Who refuses

- **A1: `migr validate` reports and the engine refuses, off one policy** ✔. `engine.Unsupported(ops,
  caps) []Refusal` is read by `internal/validate`'s fifth check and by the executor's gate, exactly
  as `engine.Destructive` is. A report and a run cannot disagree about one operation.
- A2: the run alone, since `migr validate` had no connection. **Rejected**: the command built for CI
  is the one place this can be found before a deploy, and the done-when says *fail validation*.
- A3: inside `plan.Planner`, where M13-T6 is already headed with a `capability.Set` field.
  **Rejected**: `migr plan` compiles through the same planner, and the one command that shows an
  operator *what* the server refuses must not refuse to.

### B. How `migr validate` gets a database

- **B1: a `--connect` flag, off by default** ✔. An existing invocation is unchanged, so ADR 0028 B1's
  no-secret run survives; asked for, an unreachable database **fails** the command (exit 1) rather
  than skipping the check, which is B2's rule applied to B3. The offline report says in a sentence of
  its own that nothing was checked against a server, because *"no problems found"* alone would be the
  pass B2 rejected. M13-T6's `--require-safe` needs the same connection and will imply it.
- B2: connect by default, with `--offline` to opt out. **Rejected**: it inverts ADR 0028 B1, and every
  existing caller — this repository's own suite included — would then need a reachable database or a
  new flag to do what it does today.
- B3: a `migr.yaml` key. **Rejected**: the decision stops being visible in the command line that
  failed, and a laptop run and a CI run can no longer differ.

### C. What the connection is used for

- **C1: the capability set alone** ✔. `Resolve` performs no I/O by contract, so the set outlives the
  session: `migr validate --connect` opens, reads `Capabilities()`, closes, and then walks the
  migrations exactly as before. `internal/validate` is handed a **value** and still cannot touch a
  database — the command opens one, the package never can.
- C2: the whole session — migrations' reads answered live, emulations compiled against the catalog, so
  nothing is left `unchecked`. **Rejected**: a reading migration's answer would then depend on how far
  that database happens to be migrated. Against an empty CI database, a migration reading a table an
  earlier migration creates *fails*, and the report would call it broken — which is precisely the
  verdict ADR 0040 refused when it chose `unchecked` over an answer it could not trust.

### D. Which half, and which unit of "before any execution"

- **D1: both halves, each migration before any of its own statements** ✔. Unlike the destructive gate
  this is direction-neutral: `ir.CreateTable` inverting to `DropTable` is what made *that* one
  up-only, while an operation the server will not perform is just as unrunnable backwards. A rollback
  inverts its whole queue before running any of it ([ADR 0022](0022-rollback-executor.md)), so
  `migr down` is preflighted whole.
- D2: emit and check the entire pending queue before applying any of it. **Rejected**: it asks
  migration 2 what it does against a database where migration 1 has not run, which is why
  `applyQueue` interleaves at all — migrations may branch on live query results
  ([ADR 0009](0009-migration-checksum.md)).

### E. The conformance biconditional

- **E1: narrow it to the one implication that is a contract** ✔ — *a declaration never promises what
  the compiler refuses*, and a refusal must carry a reason whichever side reached it. A declaration
  **may** refuse more than the compiler does, because it is resolved against a server the compiler
  cannot see.
- E2: a second constructor, `capability.UnsupportedByServer`, so the suite keeps both guards.
  **Rejected**: it is a `capability` API change plus a correct choice in every driver's `verdict`, to
  preserve a guard this task retires on purpose.
- E3: leave the suite and document the hole. **Rejected**: CI only meets servers where every gate
  passes, so it stays green while being wrong — and the next driver to add a version gate pays for it.

**What the dropped half was standing in for.** *"It compiles but is declared unsupported"* mattered
because the declaration was read by the report while the run read the compiler: an operator was told
the operation was impossible and the driver would have run it anyway. A1's gate makes the run read
the declaration too, so the hole the assertion guarded is closed by the task that narrows it.

## Decision Outcome

**A1, B1, C1, D1, E1.**

```console
$ migr validate --connect
validate — database "default" (mysql), 2 migrations

  20260101T000100_add_token.go
    capability  up operation 1 (add_column): column "token" is added with a uuid default, which
    MySQL writes into every existing row and therefore refuses outright with error 1674 while it
    writes a binary log: … This server has log_bin on. …

1 problem in 1 migration. every operation was checked against server MySQL 8.0.46.
```

```console
$ migr up
migr: 20260101T000100_add_token.go: driver "mysql" against server MySQL 8.0.46 will not perform
1 operation in this migration's up run, so nothing in it ran and nothing was recorded for it:

  operation 1 (add_column) on "probe_sessions.token": column "token" is added with a uuid default, …

The declaration is resolved against the connected server rather than read from documentation
(invariant #13), so another server may answer differently.
```

Five details earned their place:

**The reason is the driver's own, carried verbatim.** Every driver's refusal already names the
release, the variable, or the extension to install; rephrasing it would lose the only part an
operator can act on. The two checks therefore **partition** the refusal: `compile` reports what a
version-blind compiler refuses, `capability` reports exactly what only the server knows. The order is
what keeps one defect from being reported twice — every driver's `verdict` builds its reason from
`Compile`'s own refusal first, so asking the declaration about an operation the compiler already
refused would print the same sentence under a second name, and under a name that **changed depending
on whether `--connect` was passed**.

**An emulated operation is judged too.** It is `unchecked` for compiling — ADR 0053's answer, unchanged
— and still asked of the declaration, because MySQL's column redefinition is emulated *and*
version-gated. The two statements are about different things.

**Exit 1 on the run, 2 on the report, and nothing new in `ExitCode`.** A compile refusal already ends
that way, and the rule picks it: the operator cannot retype anything — they upgrade the server, change
a variable, or rewrite the migration — so *the world is not ready*.

**`validate.Options.Capabilities` is a pointer.** Nil means this run opened nothing and the check does
not run; a **zero** `capability.Set` refuses every operation, so a value field would turn a caller's
omission into twenty problems per migration. An unbuilt set is refused by `Options.check` as the
caller bug it is.

**A refusal names the half it is about.** Found by hand: the first version printed an identical
message for `migr up` and `migr down`. It now reads *"in this migration's down run"*, the idiom
`DirtyError` already uses — and that is as much as it may claim, since a migration refused partway
through a batch leaves the ones before it applied, and the run's own summary is what reports those.

## Consequences

### Positive

- The declaration is authoritative in fact rather than in principle: nothing is executed that the
  connected server has declared it cannot perform, and the refusal arrives with no I/O at all.
- On an engine whose DDL commits, this is the difference between a refusal and a dirty database that
  needs `migr resolve`. Measured on MySQL 8.0.46 both ways.
- `migr plan` prints the reason it has always had and never showed, and marks it `×` with a note
  saying what `migr up` will refuse.
- Four MySQL servers that **could not be in** `TestTheConformanceSuitePassesWithoutAServer` are now in
  it, and five version gates are held against the suite for the first time.
- `migr validate` has a second mode without losing its first: run with no flag it still opens nothing,
  and now says so about the check it did not make.

### Negative

- **A `.sql` project gains nothing from this check**, because every statement reaches the database as
  `ir.Raw` and every driver declares that native. It is the exact mirror of ADR 0063, where a `.sql`
  project is the only one that must permit something — and the same underlying fact: migr cannot read
  SQL.
- **`migr validate --connect` can fail for a reason that is not about the migrations.** That is the
  point of B1's *fails rather than skips*, and it is why the flag exists instead of a default.
- **The offline gate is still weaker than it looks, and now says so.** A project that never passes
  `--connect` is in exactly the position ADR 0028 described; the report names the gap rather than
  closing it.
- **The suite lost a real guard.** A driver that declares an operation unsupported while its compiler
  happily produces statements is now conformant. What makes that safe is the engine gate, so the two
  changes may not be separated.

### Neutral

- `--connect` is registered on `migr validate` alone: every other command either opens a database
  already or is a report that must not, so anywhere else it would be a flag accepted and ignored
  (ADR 0063's rule, applied again).
- A resume is not gated, for ADR 0063's reason: `prepareResume` reaches neither `forward` nor
  `reverse`, the schema is already half-changed, and the statements it runs are the ones the stopped
  run reached rather than ones a verdict was asked about.
- `migr validate --connect` takes **no migration lock** and reads no history (ADR 0027): it answers
  what these migrations are, not what will run.

## Notes

M10-T3 (reference integrity) needs introspection, which needs the same connection — `--connect` is
where it landed, and the report shape was already built
([ADR 0075](0075-reference-integrity.md)). It also inverted this ADR's own test: `migr validate
--connect` does introspect now. C2 still holds, by the mechanism rather than by not looking — a
reference is reported only where the live schema **and** the project's own beginning both refuse it,
so a migration whose `create_table` the live schema contradicts is judged by neither.

M13-T6 reads the **lock** axis of the same verdict, which is why TASKS said `--require-safe` on
`migr validate` *"arrives with M10-T2's online half"*. The flag that makes it reachable now exists;
what it means is still that task's.

M13-T6 also makes a `capability.Set` a `plan.Planner` field. When it does, the verdict for an
**expanded** operation becomes reachable — ADR 0016 notes that a MySQL retype is declared
`TableRewrite` because `Set.Resolve` performs no I/O — and this check is where a narrower answer would
show up first.
