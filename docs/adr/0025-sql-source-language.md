# 0025. SQL is a source language, and `migr create` writes what `Parse` reads

## Status

Accepted

## Date

2026-08-12

## Context

M5's goal is *"a tool a person can actually use, against PostgreSQL, with SQL-file migrations as the
interim source format until M6 lands the SDKs."* M5-T1 is the first command that **writes** anything,
and everything it writes is read back by code that already exists: `config.Load` reads the config,
`migration.Parse` and `Discover` read the filename, and M5-T2's `engine.Source` will read the file's
contents.

Three things were already decided and one was left open.

[ADR 0018](0018-declared-dependencies.md) and M4-T1 fixed the filename grammar, and its package doc
anticipated this task exactly: *"M5-T1's `migr create` has to produce exactly what Parse accepts, and
a layout string written down in two places is a file format with two definitions. Build a Migration
and call Filename; do not restate the layout."* `versionLayout` is unexported, so the CLI cannot
render a version at all — the package owes a constructor.

M4-T1 also recorded the open question: *"`config.Language` admits only `go` and `typescript`, but
M5's own goal is SQL-file migrations… so a `migr.yaml` for that phase cannot validate today.
Discovery does not care, since it records an extension without judging it, but `migr init` has to
write a language the loader accepts."*

And `engine.Options.Runtime` is documented as *"the language migrations are written in, spelled as
migr.yaml spells it"* — so a source format with no spelling in `migrations.language` is one nothing
can be asked to run.

What SQL costs was not written down anywhere. `ir.Raw` carries a `Driver` and its `Reverse` always
fails, so a `.sql` migration is engine-specific and can never have an inverse derived for it. Both
follow from [ADR 0008](0008-reverse-operations.md) and the v1 operation set, and neither is stated
where somebody choosing the format would read it.

## Decision Drivers

- M5-T2 parses the file this task writes. The writer and the reader must not be able to disagree.
- Invariant #3: *"No raw SQL outside a driver package. Ever."* The one file migr itself authors that
  is named `.sql` is squarely inside that rule.
- Invariant #10: every migration has both directions. A format whose inverse cannot be derived puts
  that obligation on the author, in the file.
- Invariant #6, in temperament: nothing migr writes may silently replace something already there.
- M4-T1's discovery rules — a duplicate version is refused naming both files, and `migr create` uses
  second resolution, *"so two creates in one second is how it happens."*

## Considered Options

### A. What language a scaffolded project names

- **A1: `sql` joins `config.Language`** ✔. `init` writes `language: sql`, `create` writes `.sql`,
  M5-T2 reads it, and `Options.Runtime` has a spelling. `--lang go` and `--lang ts` are refused
  naming M6-T7 and M7-T2 — an honest `Unsupported`, per CLAUDE.md §11.
- A2: write `typescript` and create `.ts` files. **Rejected**: every file created through the whole
  of M5 would be one nothing can execute until M7, in the milestone whose goal is a usable tool.
- A3: special-case SQL in the CLI, leaving `migrations.language` as it was. **Rejected**: it makes
  `Options.Runtime` unfillable for the format M5 actually runs, and puts a fourth language in a
  place the loader would refuse if anyone wrote it down.

### B. Where the file format lives

- **B1: a new `internal/source/sql`, holding the extension, the markers, and the template** ✔. The
  package M5-T2 fills with the parser is born holding the writer — the same reason `internal/migration`
  exports `Filename` beside `Parse`.
- B2: `internal/migration`, beside the filename grammar. **Rejected on that package's own written
  boundary**: *"the extension is recorded and never judged"*, and which runtime executes a file is not
  its question. Section markers for one extension would break the rule it wrote down.
- B3: `internal/cli`, beside the writer, and move it when M5-T2 decides. **Rejected**: the format's
  home would be provisional, and the move is a second chance to restate it.

### C. How `migr create` produces a filename

- **C1: `migration.New(at, name, ext)`, validating by parsing what it just rendered** ✔. `create`
  cannot write a file `Discover` would refuse, and a bad name gets `internal/migration`'s own
  `NameError` — the grammar, the example, and what to do.
- C2: format the timestamp in the CLI. **Rejected** by the package doc quoted above: two spellings of
  one file format.
- C3: normalize the name — lowercase, spaces to underscores. **Rejected**: a second opinion about
  what a name may be, and the file on disk would not be the one the operator asked for. Refusing
  costs one retry and teaches the grammar.

`New` converts to UTC itself. A caller holding a local `time.Now` would otherwise write local
wall-clock digits into the field that defines run order, and every future SDK's `create` would have
to remember not to.

### D. Two migrations created in one second

- **D1: refuse eagerly, naming the file that already claims the version** ✔. One directory read.
- D2: let `O_EXCL` catch the identical filename and leave the differing-name case to
  `Discover`'s `DuplicateError`. **Rejected**: `create` is the only place a duplicate can be
  prevented at all, and left to discovery the failure arrives at the next `migr up`, far from the
  command that caused it, blocking every migration in the project until someone renames a file.
- D3: advance the timestamp until one is free. **Rejected**: it records a version that is not when
  the migration was created — a silent lie in the field that orders everything.

### E. What a scaffolded `migr.yaml` contains

- **E1: only the keys that have no default, plus a comment pointing at `migr config`** ✔.
- E2: the full documented block, every knob with its default. **Rejected**: a scaffold restating
  `lock.timeout: 30s` is a second definition of a value `applyDefaults` owns, frozen into every
  project ever created, and `migr config` exists precisely to print the resolved set.

The DSN is `${DATABASE_URL:-postgres://localhost:5432/mydb}` rather than a bare reference. A bare one
makes `config.Load` fail with *"environment variable DATABASE_URL is not set and has no default"* —
which means **writing a text file requires a database credential**, since every command loads the
whole config. The fallback is a placeholder rather than a database: nothing connects anywhere by
accident, and a literal connection string is never scaffolded into a file that gets committed.

### F. Whether the template carries example SQL

- **F1: no SQL at all — markers and guidance comments** ✔, held by a test that walks every line.
- F2: a commented-out `CREATE TABLE` to show the shape. **Rejected**: invariant #3 says *ever*, and
  this is the one file migr authors that is named `.sql`. An exception here would ship in every
  migration anyone ever creates.

## Decision Outcome

**A1, B1, C1, D1, E1, F1.**

`internal/source/sql` holds four constants — `Ext`, `MarkerUp`, `MarkerDown`, `Template` — and
M5-T2's `Emit` will be added beside them. `-- migr:` is the prefix the golden files already use.

`migration.New` is the only way to reach `versionLayout` from outside the package:

```go
func New(at time.Time, name, ext string) (Migration, error) {
	return Parse(Migration{Version: at.UTC().Format(versionLayout), Name: name, Ext: ext}.Filename())
}
```

`migr init` refuses an existing `migr.yaml` before writing anything, and there is no `--force`;
`migr create` refuses a claimed version before writing anything. Both write through `O_EXCL`, so the
check that reads the world and the write that changes it cannot disagree.

Exit codes follow one rule, stated here so M5-T6 inherits it rather than a habit: **the argument is
wrong → 2** (a name the grammar refuses, a language nothing can write); **the world is not ready →
1** (a config already there, a directory that is not, a version already claimed).

## Consequences

### Positive

- What `migr create` writes is what `Discover` reads, by construction rather than by two rules being
  maintained in agreement. A test creates two migrations and discovers them back.
- The config `migr init` writes is one `config.Load` accepts, and its `migrations.dir` is the
  directory `init` created — which holds `init`'s anchoring to `config.anchor` mechanically. A new
  required field breaks `init`'s test rather than a user's first command.
- `migr init && migr create add_users` works in a directory where nobody has exported anything.
- M11-T5's `migr diff` writes a migration file too, and `migration.New` plus this template are what
  it writes with.

### Negative

- **A `.sql` migration gives up database independence.** `ir.Raw` names a driver, so the format that
  makes M5 usable is the one that abandons the framework's first claim. It is the interim format for
  a reason, and the package doc says so where an author will find it.
- **A `.sql` migration can never have a derived down.** `Raw.Reverse` always fails, so invariant #10
  is satisfied by the author writing the undo and by nothing else. The template says so in the file
  itself, which is where somebody stands when they decide to skip it.
- **Every `raw` operation is destructive by default** (invariant #7), so once M10's validator exists,
  every `.sql` migration is one `--allow-destructive` would gate. M10-T1 has to settle that; it is
  recorded here because this is the ADR that made `.sql` the default format.
- A second create in the same second is refused rather than served. The message says to try again in
  a moment, which is a real, if brief, refusal to do what was asked.

### Neutral

- `internal/source/` gets its own depguard rule rather than joining the driver deny: it is a concrete
  implementation of a core interface, but unlike a driver it is not registry-wired — `internal/cli`
  builds it. The carve-out is therefore `cmd/migr` **and** `internal/cli`, and keeping it a separate
  rule set is what stops that widening from also widening the driver rule, which `cli` is still held
  to.
- `--lang ts` is an alias resolved before support is checked, so it reports M7-T2 rather than an
  unknown language. `migr.yaml` itself accepts only the canonical spellings.

## Notes

M5-T2 parses `MarkerUp` and `MarkerDown` rather than restating them, attaches
`databases.<name>.driver` to each `ir.Raw`, and reports `engine.ErrNoDownAuthored` for a file whose
down section is empty — after which [ADR 0022](0022-rollback-executor.md)'s derivation runs, fails on
`Raw.Reverse`, and tells the author to write the undo. That chain is the reason the template says so
first.

When M6 lands the runtimes, `internal/source/sql` either moves under `internal/runtime/` or stays as
the one source that needs no subprocess. M5-T2 owns the wiring decision; this ADR only fixes where
the format lives while the CLI is the only thing reading it.
