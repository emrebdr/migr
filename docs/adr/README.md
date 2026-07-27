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

## When to write one

An ADR is required for any change to:

- the operation IR — its shape, its version, or its operation set
- the runtime protocol between the engine and a language SDK
- the database driver contract
- the capability model, on either the support or the lock-impact axis

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
5. Add a row to the index above.

## Superseding

ADRs are historical records and are never edited to reflect a later change of mind. To reverse
a decision, write a new ADR that supersedes the old one, and update the old one's Status to
`Superseded by NNNN` — that line is the only permitted edit. The original reasoning stays
readable, because knowing what was tried and abandoned is usually more useful than knowing only
the current answer.
