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
| [0024](0024-dirty-runs.md) | A run is dirty when its statements outlived the failure | Accepted | What separates a failed run from a dirty one, what a non-transactional session changes, and what record-before-commit means where DDL auto-commits |
| [0025](0025-sql-source-language.md) | SQL is a source language, and `migr create` writes what `Parse` reads | Accepted | What a scaffolded project names and contains, where the `.sql` file format lives, how a filename is produced, and what the interim format gives up |
| [0026](0026-dry-run-and-sql-sources.md) | A dry run reads what an apply ensures | Accepted | Why a preview is an entry point rather than a flag, what a database with no history table means to it, how a `.sql` file becomes statements, and which database a bare command targets |
| [0027](0027-read-only-reports.md) | A read-only report takes no lock | Accepted | Why `status` and `history` do not lock where a preview does, what a database with no history table prints, where the fold lives, and why a report that finds drift still exits 0 |
| [0028](0028-validation-report.md) | A validator reports in phases | Accepted | What "every problem rather than the first" covers, why validation opens no database, why compiling belongs to it, and how a `.sql` migration declares that nothing can undo it |
| [0029](0029-failure-presentation.md) | A failure is a document too | Accepted | Where a `--json` failure is written and what shape it takes, why the two paragraph errors become fields, and the four exit codes that moved to match the rule the CLI already stated |
| [0030](0030-runtime-protocol-specification.md) | The runtime protocol, specified | Accepted | How the engine and a language runtime agree on a version, cancel work, and tell a refusal from a failure — and the one place plan mode differs |
| [0031](0031-protocol-codec-and-framing.md) | The codec, and what ends a session | Accepted | A line that cannot be framed ends the session after one `"id": null` error, with no recovery — because the unreadable part is the id somebody is waiting on |
| [0032](0032-host-callback-server.md) | The host callback server, and the SQL a migration wrote | Accepted | `Conn` grows `Query` and `Perform`, so read-only is which method you call; the server refuses the write, because a data-modifying CTE defeats any check on the leading keyword |
| [0033](0033-where-the-mode-lives.md) | The mode belongs to the call, and the engine owns it | Accepted | One rollback issues both modes on one session, so a session-level mode cannot serve it; `Source.Emit` states the mode, and a derivation is always a question |
| [0034](0034-runtime-process-lifecycle.md) | The runtime process, and what outlives what | Accepted | The child gets a process group of its own and every path ends the group; that is also what breaks Ctrl-C, so `cmd/migr` handles signals — and an engine killed outright is covered by EOF on the child's stdin |
| [0035](0035-generated-go-runner.md) | Running a Go migration | Accepted | A runner `main` is generated in the user's module, built, and cached by content hash in `.migr/`; there is no in-process fast path, and the stamp's one hole is stated and asserted rather than omitted |
| [0036](0036-escape-hatch-statements-in-a-preview.md) | An escape-hatch statement in a preview | Accepted | `Emit` returns `engine.Emitted`, so what a preview held rather than ran reaches `migr plan`; and a down derived over a migration that used the escape hatch is refused |
| [0037](0037-go-migration-contract.md) | The Go migration contract | Accepted | One required method and four optional interfaces, so "authors no down" is a type fact; identity is read from the call site; and the duplicated wire is held to the specification on both sides rather than to itself |
| [0038](0038-go-schema-builder.md) | The Go schema builder | Accepted | A chain ending in Apply, with a window that fails a migration for one it never applied; §9.1 checked locally so two SDKs refuse the same operations; and the second copy of the IR held to docs/ir.md, decoding every vector in it |
| [0039](0039-runtime-conformance-fixtures.md) | The runtime conformance fixtures | Accepted | A fixture is a neutral manifest neither language owns, whose own bytes are held to being what ir.Marshal emits; the reads it branches on are pinned in it; and every fixture is asked both directions in both modes |
| [0040](0040-running-a-go-migration.md) | Running a Go migration from the CLI | Accepted | The source is built after the connection and the process ends before it; migr validate stays database-free and reports what it could not check; the schema digest lives in a package the driver owns |
| [0041](0041-typescript-package-scaffold.md) | The TypeScript package and its version policy | Accepted | The package major is the protocol version it speaks, as a publishing convention rather than a protocol mechanism; ESM and CJS ship in directories that declare their own type; the test runner is Node itself |
| [0042](0042-typescript-migration-contract.md) | The TypeScript migration contract | Accepted | One required member and four optional parts, two of them data; the module is the registration, so there is no Register; every type-level rule has a runtime twin, because .js migrations are checked by nothing |
| [0043](0043-typescript-schema-builder.md) | The TypeScript schema builder | Accepted | A logical type is its own canonical spelling, branded and thrown at the call; one namespace object so `enum` keeps its name; an ordered shape table decides the bytes rather than insertion order; the window seals the un-awaited apply Go has no hole for |
| [0044](0044-typescript-protocol-client.md) | The TypeScript protocol client | Accepted | The codec rebuilds the envelope rather than re-stringifying what it parsed, so the 43 vectors test an order; the stdout guard takes over process.stdout.write as Go takes over os.Stdout; a response nobody awaits is dropped, because the cancel race makes it ordinary |
| [0045](0045-node-runtime-binary.md) | The Node runtime binary | Accepted | module.registerHooks with an esbuild transform, because Node's own stripping refuses an enum and a synchronous load hook cannot await; the module system is read from the nearest package.json, since Node reports none for TypeScript and a CommonJS namespace carries both export styles at once; the executable is generated into dist/ |
| [0046](0046-node-runtime-adapter.md) | The Node runtime adapter | Accepted | Resolution is one `node -e` probe rather than a node_modules walk, because a workspace hoists its dependencies above the project and Node's resolver already knows that; the version is reported before the missing install, since an old node cannot resolve an exports-only package either; the executable's path comes from @migr/sdk's own bin field |
| [0047](0047-line-separator-escaping.md) | U+2028 and U+2029 are emitted as UTF-8, and a scanner undoes Go's escape | Accepted | Which bytes an IR document carries for the two Unicode line separators, how an implementation whose encoder escapes them regardless complies, what proves two SDKs agree when the engine normalises their difference away, and on what condition a committed checksum vector may move |
| [0048](0048-multi-language-projects.md) | One directory, several languages: route each migration by its own extension | Accepted | How a runtime learns which files in a mixed directory are its own, what migrations.language then means, where a history row's per-migration language comes from, and what several runtime sessions over one pinned connection depend on |
| [0049](0049-reading-a-declared-dependency.md) | A declared dependency is read once the runtimes are up, and a report names the order it has | Accepted | Where the run order settles now that only a started runtime can report a declaration, whether an edge may cross a language, what `migr status` prints when it has no toolchain to ask, and what an unresolvable graph does to the CI gate |
| [0050](0050-sqlite-session-and-pragmas.md) | The SQLite session: one pinned connection, three pragmas, and what an in-process engine changes | Accepted | Which session settings migr owns and why each is read back, what foreign_keys is set to and what that costs the table rebuild, where a driver's live tests belong when the engine needs no daemon, how a dynamically-typed value becomes the text a JSON string carries, and whose decision `lock.strategy`'s default is |
| [0051](0051-sqlite-type-map-and-the-rebuild-boundary.md) | SQLite's type map, and where the table rebuild begins | Accepted | What a declared type name claims when the engine enforces none of it, why decimal is declared and lossy where enum is supported outright, which three operations sit on the boundary of the rebuild M8-T4 owes, and why SQLite's two ADD COLUMN restrictions cannot be compile-time refusals |
| [0052](0052-sqlite-lock-verdicts.md) | SQLite's lock verdicts, and why an emulation cannot be declared before it can be compiled | Accepted | What the four lock verdicts mean on an engine with one database-wide write lock, why nothing here is lock-free and drop_column is a table rewrite, how invariant #13 is measured without a clock, and why the Emulated declaration waits for the commit that can produce its statements |
| [0053](0053-expanding-an-operation-against-a-session.md) | An operation a driver compiles against the database, rather than against nothing | Accepted | How a driver that emulates an operation tells the planner it needs a session, why an expansion returns statements rather than neutral operations, what `migr validate` reports when it has no database to expand against, and the conformance biconditional restated around a third answer |
| [0054](0054-the-sqlite-table-rebuild.md) | The SQLite table rebuild, as the measurements made it | Accepted | Why the rebuild renames nothing and copies the rows twice, why indexes are restored before the rows and triggers after, which foreign keys make a rebuild impossible and why deferral does not save them, and what is copied through verbatim rather than understood |
| [0055](0055-the-sqlite-file-lock.md) | The SQLite file lock: flock decides, the record reports, and nothing wedges | Accepted | Why the lock is the flock and the file only where it lives, why it is never unlinked and is one per database file rather than per installation, the four-rung acquire and what its staleness rung is evidence of, what the network-filesystem story can honestly promise, and where a holder's age comes from when the driver has no clock |
| [0056](0056-the-sqlite-history-store.md) | The SQLite history store: asking the catalog where PostgreSQL reads a code | Accepted | Why history.ErrNoTable is asked for rather than classified when one error code covers three causes, why the lookup folds case and reads table_info rather than table_xinfo, why the instant is rendered here rather than bound, and what an untagged end-to-end round trip can and cannot claim about a rebuilt table |
| [0057](0057-the-mysql-session.md) | The MySQL session: what a read-only transaction does not refuse | Accepted | Why START TRANSACTION READ ONLY lets every DDL statement through and what closes it, which statement reads like one and writes on an engine with no data-modifying CTE, why Perform reports the server's own count where a blob is rendered migr's way, what pinning NO_BACKSLASH_ESCAPES costs a migration's own literals, and why the history store and two operations of M9-T2 landed here |
| [0058](0058-compiling-mysql.md) | Compiling MySQL | Accepted | Why the server refuses an index over a text column and the driver only explains it, why the collation is pinned to utf8mb4_bin and what case-sensitive equality costs, why a column change is a full redefinition that refuses four things rather than guessing, why set_default is refused when both servers accept it and neither performs it, and why a primary key carries no name |
| [0059](0059-mysql-lock-verdicts.md) | MySQL's lock verdicts, and the two things a server will say about its own ALTER | Accepted | Why ALGORITHM=INSTANT is brief_exclusive rather than lock_free and the task's own mapping was wrong, how a moved InnoDB TABLE_ID and a demanded algorithm make two one-directional instruments, why one alter_column payload is metadata-only and a retype is over-reported on purpose, which default a server can store rather than materialise, and why a uuid default is refused only while the binary log is on |
| [0060](0060-the-mysql-migration-lock.md) | The MySQL migration lock, and a holder duration that says what it measures | Accepted | Why the lock name is a digest rather than two identifiers, what GET_LOCK's three answers are and why NULL is neither granted nor given up on, why nothing is reset and nothing retries, why a release whose session is gone reports success, why the holder is a connection id plus an optional row, and why the duration a report carries is labeled with what it measures |
| [0061](0061-refusing-a-dirty-database.md) | Refusing a dirty database, and what the row may be asked to say | Accepted | Why the refusal a dirty database gets became a type, how the two senses of ErrDirty stay tellable apart under one JSON key, why the refusal states that it cannot say how many statements the run had rather than guessing, and why the history row records the statement that failed rather than the paragraph describing the window |

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
