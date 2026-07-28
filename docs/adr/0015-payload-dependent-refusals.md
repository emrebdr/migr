# 0015. A driver refuses payloads, not operation kinds

## Status

Accepted

## Date

2026-07-28

## Context

The PostgreSQL compiler (M3-T2) is the first one written, and it turned up a shape the capability
model had made room for but nothing had yet needed: several refusals it has to make are not
properties of an *operation kind* at all.

PostgreSQL supports `create_table`. It does not support *this* `create_table`, the one with an
`enum` column. It supports `drop_primary_key`, but not the one that names no constraint. It
supports every operation, but not one whose table name is 70 bytes long. In each case the kind is
fine and the payload is not.

Four such cases arrived at once:

- **`enum(...)`.** A PostgreSQL enum is a `CREATE TYPE` object. Nothing in the v1 IR owns it:
  `drop_table` rebuilds the table on reversal but nothing drops the type, so apply → roll back →
  apply fails on a type that already exists. `add_column`'s inverse is a bare `drop_column`
  carrying no `from`, so the type leaks with no record of what created it.
- **`array(array(...))`.** PostgreSQL parses `integer[][]` and treats it as *identical* to
  `integer[]` — array dimensionality is declared and not enforced. Two distinct logical types
  would become one native type with no error anywhere.
- **An unnamed `drop_primary_key`.** PostgreSQL has no name-free form; a key is dropped by
  `ALTER TABLE … DROP CONSTRAINT <name>`. The v1 IR leaves the name optional because MySQL has no
  name for a primary key.
- **An identifier over 63 bytes, or one holding a NUL.** PostgreSQL truncates a long identifier to
  `NAMEDATALEN - 1` with a *notice* rather than an error, so two distinct long names silently
  become one object. A NUL ends the statement where it appears, because a query reaches the server
  as a C string — what runs is a prefix of what was compiled, and the golden file records the
  whole of it.

Every one of these is a silent-wrong rather than a loud failure, which is the class this project
treats as most severe.

There is also a fifth case this ADR does **not** resolve, recorded here because it constrains what
a compiler can decide at all — see *Consequences*.

## Decision

**A driver's unit of refusal is the operation, payload included, and the same predicate answers
for both the compiler and the declaration.**

`capability.Rule` is `func(op ir.Operation) capability.Verdict` — it already receives the whole
operation, decided in [ADR 0012](0012-capability-resolution.md) so MySQL could answer per column
type. This is the same mechanism, used for the support axis rather than the lock axis. The driver
conformance suite already depends on it: a `raw` operation addressed to another engine must be
refused by every driver but the one it names, and that too is a payload-dependent refusal of a
kind the driver otherwise supports.

`Compile` renders a refusal as a `driver.UnsupportedError`; the `Rule` renders the same refusal as
`capability.Unsupported`. In the PostgreSQL driver the `Rule` is *derived from* `Compile`:

```go
func verdict(op ir.Operation) capability.Verdict {
	if _, err := compile(op); err != nil {
		return capability.Unsupported(describeRefusal(err))
	}

	return capability.Native(lockPending)
}
```

The biconditional the conformance suite exists to prove — `Resolve(op).Allowed()` false exactly
when `Compile` refuses — then holds by construction rather than by agreement, and the suite proves
the wiring rather than the coincidence.

## Consequences

**The declaration stays authoritative to callers.** `migr validate` and `migr plan` read the
capability set before anything touches a database, and that does not change. Which of the two is
*derived* is an implementation detail of this driver; which one is *consulted* is the contract.

**This shape does not generalize to every driver, and does not have to.** A `Compile` that succeeds
says nothing about whether it succeeded natively or by emulation, so SQLite — which must declare
`Emulated` with a named strategy for its table rebuilds (M8-T3) — will need its rule to say more
than "it compiled". The rule is per driver; the contract is not.

**A refusal reason names the payload, not just the kind.** `postgres does not support create_table`
would be false. The reasons say which column, which type, which identifier and how long it was, so
an operator can act on them without reading the compiler.

**Redundant guards are kept and tested.** The nesting check exists in both the refusal predicate
and the type table. A mutation sweep found that dropping the table's half was invisible, because
the refusal fires first — so it is now asserted directly. Against a silent-wrong, defense in depth
is worth having and worth testing.

### The case this does not solve: a version-blind compiler

`Driver.Compile` is pure by contract — no context, no connection ([ADR 0011](0011-driver-contract.md))
— and lives on the `Driver`, which is constructed once per process and may open sessions against
several servers. The capability set is built inside `Open` and *does* know the version.

`gen_random_uuid()` is the spelling of the `uuid` default function, and it is core only from
PostgreSQL 13. This driver's minimum is 10. So the compiler cannot make the decision the answer
depends on, and the declaration is the only place a version-dependent refusal can live.

This is [ADR 0012](0012-capability-resolution.md)'s "the support axis is version-dependent too"
meeting PostgreSQL. Its example was SQLite 3.35's native `DROP COLUMN`, which is answerable in the
declaration alone because the compiler emits the same statement either way. PostgreSQL's is not:
on a PG 12 server the declaration would refuse an operation `Compile` would happily emit SQL for.

That is not an outage — the declaration is read first, so the operator gets a clean refusal at
validation time — but it is a genuine break in the biconditional, and the conformance suite would
report it on a pre-13 server against a fixture carrying a `uuid` default. `drivertest.Operations`
carries none, so nothing fails today.

Three ways out were considered and none belongs to M3-T2:

- **Raise the driver minimum to 13.** Deletes the problem and deletes M3-T3's canonical
  lock-impact case with it: the PostgreSQL 10 → 11 `ADD COLUMN … DEFAULT` boundary becomes
  unreachable through `Open`, and the tested matrix (16 and 17) contains no boundary above 13.
- **Refuse the `uuid` default on every version.** Symmetric and version-free, and absurd to an
  operator on PostgreSQL 17 where it works perfectly.
- **Tell `Compile` the version.** Correct, and a change to the driver contract — an ADR of its own,
  not a line in a compiler.

M3-T3 owns the declaration and inherits the choice.

## Rejected

**Refusing the whole kind.** Declaring `create_table` unsupported because *some* create_table
operations carry enums would refuse every table PostgreSQL can create. It is honest about nothing.

**Compiling an enum to a `CREATE TYPE` object.** Truly native, and it commits the project to a
type-lifecycle design the IR does not model: deterministic type naming, dropping the type on
reversal, and altering labels (which `ALTER TYPE … ADD VALUE` could not do inside a transaction
before PostgreSQL 12). M3-T7's done-when is "the full op set applies and rolls back cleanly", and
this would not.

**Emulating an enum as `text` with a check constraint.** Loses the declared label ordering that
`docs/ir.md` §5.3 says is part of the type — PostgreSQL compares enum labels by declaration order,
so `enum(a,b)` and `enum(b,a)` genuinely differ — and creates a constraint no IR operation names,
which is the same lifecycle problem with a different object.

**Deriving `<table>_pkey` for an unnamed `drop_primary_key`.** It is PostgreSQL's own default
naming, so it would usually work and would fail loudly when it did not. But it names a constraint
the migration never declared, and `migr diff` (M11) will always introspect the real name — so the
guess only ever applies to a hand-written migration, where telling the author what to look up is
strictly better.

**Refusing an over-long identifier with a plain error rather than a refusal.** It would sit outside
the conformance suite's "either compilable or refused with a reason, and there is no third
possibility". A refusal is what that suite is built to read, and a `Rule` can express the same
thing, so there is no reason to invent a third answer.
