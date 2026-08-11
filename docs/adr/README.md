# Architecture Decision Records

Decisions that shaped `migr`, with the alternatives that were rejected and why.

These records are deliberately self-contained. Each states enough context to be read on its own,
by someone who has cloned the repository and read nothing else, possibly years later. If an ADR
only makes sense alongside another document, that ADR needs rewriting.

## Index

| # | Title | Status | Decides |
|---|---|---|---|
| [0001](0001-architecture.md) | Go core with an operation IR in the middle | Accepted | Implementation language; the IR as the sole contract between SDKs and drivers |
| [0002](0002-runtime-protocol.md) | Bidirectional JSON-RPC over stdio for language runtimes | Accepted | How migrations written in other languages execute |
| [0003](0003-ir-versioning.md) | Version the operation IR explicitly | Accepted | What happens when an SDK and the engine disagree |
| [0004](0004-safety-model.md) | Lock impact as a capability axis; zero-downtime as a v0.1 correctness property | Accepted | How the cost of an operation against a live database is modelled |
| [0005](0005-logical-type-encoding.md) | Encode logical types as canonical strings in the IR | Accepted | How a logical type is written down inside an IR document |
| [0006](0006-ir-operation-shape.md) | Flat discriminated operations, one representation per schema fact | Accepted | The shape of an operation document, and where destructiveness is decided |
| [0007](0007-ir-document-envelope.md) | A versioned document envelope, outside the checksum boundary | Accepted | What an IR document is, and which bytes a migration checksum covers |
| [0008](0008-reverse-operations.md) | Every operation declares an inverse or a reason it has none | Accepted | How rollback is derived, and the three drop operations the set was missing |
| [0009](0009-migration-checksum.md) | A migration checksum is a scheme-versioned pair: normalized source and emitted IR | Accepted | What a checksum covers, which half decides drift, and how its own format versions |
| [0010](0010-recorded-absence-of-a-default.md) | Recorded prior state distinguishes "no default" from "nothing recorded" | Accepted | How an `alter_column` records what it replaced, and when `irVersion` 1 freezes |
| [0011](0011-driver-contract.md) | The driver contract: a session-scoped connection and an explicitly wired registry | Accepted | What a driver must implement, where connection-scoped work lives, and how a driver reaches the binary |
| [0012](0012-capability-resolution.md) | Capability resolution: one rule per operation, resolved against one server | Accepted | How a driver declares both capability axes, and how an unresolved lock verdict compares against a threshold |
| [0013](0013-migration-history.md) | Migration history is an append-only log, keyed by the run rather than by the row | Accepted | What a history row records, why a rollback appends instead of deleting, and why the store is bound to a session |
| [0014](0014-online-change-capture.md) | Online change capture reads the write-ahead log, and cutover waits for a human | Accepted | How a shadow table stays current without triggers, and who decides the moment of the rename |
| [0015](0015-payload-dependent-refusals.md) | A driver refuses payloads, not operation kinds | Accepted | Where a per-payload refusal lives, how the compiler and the declaration are kept in agreement, and what a version-blind compiler cannot decide |
| [0016](0016-lock-impact-is-a-duration.md) | Lock impact is a duration, not a lock level | Accepted | What the four lock verdicts mean, what a live server can settle, and why raw is permanently unknown |
| [0017](0017-transaction-boundaries.md) | A transaction is a closure the driver brackets, not a handle the caller holds | Accepted | How a migration becomes one transaction, where the history write sits relative to the commit, and why grouping is the engine's |
| [0018](0018-declared-dependencies.md) | A dependency is declared by a migration, never computed by one | Accepted | What `Dependencies()` may return, how an edge names its target, and what resolution hands downstream |
| [0019](0019-history-reconciliation.md) | Reconciliation replays the whole log, and a half-applied migration is a fifth state | Accepted | Where `ErrNoTable` lives, how absence is made unmissable, and why status is folded rather than looked up |
| [0020](0020-lock-contention.md) | Lock contention: one budget, no retry, and a holder read after the fact | Accepted | How long to wait, where the sentinel lives, what an operator is told, and what "recoverable" means per strategy |
| [0021](0021-apply-executor.md) | The apply queue reconciles under the lock, and a batch belongs to one database | Accepted | Where a migration's operations come from, who decides what is pending, what a batch numbers, and where the history write sits |
| [0022](0022-rollback-executor.md) | A rollback inverts the whole batch before it runs any of it | Accepted | Where a down comes from, what order a rollback undoes in, what halts one before it starts, and what a down row records |
| [0023](0023-batch-narrowing.md) | `--to` names the state to end in, and narrowing is a prefix of the run order | Accepted | What `--step` and `--to` select in each direction, which batch a bare `migr down` acts on, and where a refusal sits relative to a scope |

## When to write one

An ADR is required for any change to:

- the operation IR — its shape, its version, or its operation set
- the runtime protocol between the engine and a language SDK
- the database driver contract
- the capability model, on either the support or the lock-impact axis
- the migration checksum scheme — what it covers, how it is composed, or how it is spelled

The test is whether a future contributor would otherwise ask "why is it like this?" and have no
way to find out. Routine implementation choices do not need one.

## How to write one

1. Copy [`0000-template.md`](0000-template.md) to `NNNN-short-title.md`, taking the next
   unused number. Numbers are never reused, including for abandoned drafts.
2. Fill in every section. **Considered Options is not optional** — an ADR listing one option is
   an announcement, not a decision record, and the rejected options are the part future readers
   actually need.
3. For each rejected option, say *why* it lost, and distinguish rejection on merit from
   rejection on timing or cost. An option rejected for timing may deserve revisiting; a reader
   cannot tell which is which unless the ADR says.
4. Write the Consequences section honestly. If it lists only benefits, the trade-off has not
   been found yet.
5. Add a row to the index above, **and to the table in the root `README.md`**, which is the only
   entry point someone who has just cloned the repository has.

## Superseding

ADRs are historical records and are never edited to reflect a later change of mind. To reverse
a decision, write a new ADR that supersedes the old one, and update the old one's Status to
`Superseded by NNNN` — that line is the only permitted edit. The original reasoning stays
readable, because knowing what was tried and abandoned is usually more useful than knowing only
the current answer.
