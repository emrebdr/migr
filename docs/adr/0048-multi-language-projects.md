# 0048. One directory, several languages: route each migration by its own extension

## Status

Accepted

## Date

2026-09-28

## Context

`migr`'s central claim is that a migration can be written in any supported language and behave
identically. Until this record, a *project* could be written in one: `migrations.language` in
`migr.yaml` named it, `internal/cli` built exactly one source from that key, and every file in the
directory was assumed to be that language's.

M7-T8 found out what "assumed" cost. Pointed at a directory holding both a `.go` and a `.ts` file, the
one runtime that was started loaded the files it recognized, `migr up` applied them, and the run then
failed on the first file that runtime had never heard of — with the SDK's own `unknown_migration`,
which is true and useless. The project was left **half applied**, which is the worst failure this tool
has, and the failure blamed a migration that was fine. M7-T8 closed it with a refusal: a file whose
extension was not the project language's failed the command, naming this task.

That refusal was always a placeholder, and it was also slightly wrong. It compared each file's
extension against `templates[language].ext`, one value per language — so a `.js` or `.mjs` migration
was refused in a project that was already TypeScript, even though `migr-runtime-node` loads all three
and M7-T2 chose `erasableSyntaxOnly` partly to keep `.js` working. The relation is one language to
*many* extensions.

Four decisions were needed, and each is a fork rather than a line of code: how a runtime learns which
files in a mixed directory are its own; what `migrations.language` then means; where the history row's
per-migration language comes from, given that `history.Record.Runtime` is a column and
`engine.Options.Runtime` was one string for a whole run; and what it means that several runtime
sessions are then alive over one pinned `driver.Conn`.

## Decision Drivers

- **A half-applied database must stay unreachable.** Whatever replaces the refusal has to make the
  same promise: every file the directory holds either runs or fails the command *before* anything runs.
- **CLAUDE.md §9's strictness is load-bearing and cheap to lose.** "Discovery is strict, and dotfiles
  are the only exemption" exists because a migration written, committed, reviewed and then quietly
  skipped is found missing in production. Any exemption has to be narrow and has to be re-made
  somewhere.
- **One fact, one definition.** The extension set a runtime loads is already written in that SDK; a
  second copy in the engine that can disagree with it is how the `.js` bug happened.
- **`Source` is a one-method interface and worth keeping** — `Emit` is the whole contract, and the
  engine asks one migration at a time, which is what makes everything below safe.
- **Nothing in `migr.yaml` should have to change.** Every existing project's config must keep working
  unchanged, since a config migration for a pre-alpha convenience is a bad trade.

## Considered Options

### How a runtime learns which files are its own

#### Option 1: Extension ownership, with the engine reconciling

Each SDK skips a file whose extension it does not load. The engine owns a language-to-extensions map,
routes every discovered file through it, and then — this is the half that makes it safe — calls
`migration.discover` and holds what each runtime reports against what it routed there.

**Pros**

- No protocol change at all.
- Each SDK already has the list: `sdk/typescript`'s `EXTENSIONS` is `[".ts", ".js", ".mjs"]`, and the
  Go SDK needs nothing, since its registry only ever holds what registration gave it.
- The reconciliation is worth having on its own. Nothing in `internal/cli` called `Session.Discovered`
  before this — only the conformance suite did — which is precisely why M7-T8's failure surfaced as
  `unknown_migration` instead of as a sentence about a file.

**Cons**

- The extension set exists twice, in the SDK and in the engine, and they can disagree.
- A loader that skips anything has stopped being strict on its own.

#### Option 2: `initialize` carries the file set

The engine tells each runtime which files it is responsible for; both loaders load exactly that list.

**Pros**

- Routing is stated on the wire, so the reconciliation is trivially exact.
- The SDK's extension list stops deciding anything.

**Cons**

- A protocol addition. Compatible per §10.2 — payloads are open — but an older SDK that ignores the
  member still refuses its neighbour's file, so mixed projects would require a current SDK anyway.
- It does not actually remove a definition. The engine still needs its own map to decide *which
  runtime to start*, and each SDK still needs its extensions for the grammar, so the duplication
  survives and a protocol member joins it.

**Rejected because:** it costs a wire change for a guarantee the engine can make on its own side of
the wire. The reconciliation gives Option 1 the same diagnostics — a routed file nothing loaded, and a
migration two runtimes both claim, are each a failure naming the file — so the member would buy
precision that is already there.

### What `migrations.language` means afterwards

#### Option 3: the default for `migr create`, and nothing else

**Pros**

- No config schema change; every existing `migr.yaml` keeps working, key still required and validated.
- A directory that gains a `.ts` file runs it, with no config edit, which is the feature.

**Cons**

- Nothing declares which toolchains a project needs, so a machine missing one finds out at `migr up`.

#### Option 4: a declared list

`languages: [go, typescript]`, and a migration whose language is not listed is refused.

**Pros**

- Adding a second language becomes deliberate, and `migr status` could say what a run will require.

**Cons**

- A config schema change, a migration path for the old key, and a second place the truth lives — the
  files say one thing and the list says another, and the list can be wrong.

**Rejected because:** the directory already states which languages the project uses, in a form that
cannot be stale. A list would be a second statement of it, and the first thing it would do is disagree.

### Where the history row's language comes from

#### Option 5: `Options.Runtime` becomes a function of the migration

**Pros**

- `Source` stays one-method and `Emitted` is untouched; the blast radius is two test fixtures.
- `internal/cli` supplies it from the same map the mux routes on, so the row and the runtime that ran
  it cannot disagree.

**Cons**

- A function on an options struct is a slightly looser shape than a string.

#### Option 6: `Emitted` gains a `Runtime` field

**Pros**

- Literally "answered by the migration": whoever answered the call says what language it answered in.

**Cons**

- Three `Source` implementations must each remember to fill it, a forgotten one is an empty string
  discovered at record time rather than by `Options.check`, and the conformance suite constructs
  `Emitted` too.

**Rejected because:** it spreads one fact across three implementations to move it one call closer to
its source, and the failure mode of forgetting is silent.

## Decision Outcome

**A language owns a set of extensions, and each migration is routed by its own.** `extensions` in
`internal/cli/create.go` holds the map beside `templates`; the first entry is what `migr create`
writes, and `languageOf` reads it backwards. Tests hold the two maps together and refuse an extension
two languages claim. A migration whose extension **no** language owns is refused at discovery — exit
2, naming the file and every extension that is routed — so §9's strictness survives with the answer
becoming a set rather than one value. This also fixed `.js` and `.mjs`, which M7-T8 had made
unusable.

**`target.sources` builds one source per language the directory needs and returns a `mux` over them.**
`sql` needs no process; `go` and `typescript` each get a session. The closer ends every one of them,
newest first, and a failure part-way closes what already started — M7-T8's rule with more than one
subject. `migr validate` takes the same path, so every runtime the directory needs starts behind
`driver.Offline`.

**The engine re-makes the guarantee it stopped making.** After starting a session, `sourceFor` asks it
what it loaded and holds that to what was routed there, in **both** directions: a routed file the
runtime did not load, and a migration two runtimes both claim, are each a failure naming the file and
the language. That is what lets a loader skip anything at all.

**The TypeScript loader's skip is narrower than "anything I don't recognize".** `foreign` in `name.ts`
reports a file whose name is a well-formed migration *in another extension* — it substitutes its own
extension and re-parses — so `20260727T153000_add_users.go` is skipped and `README.md`, `helpers.mts`,
and `20260727T153000-add-users.go` are all still refused. The SDK keeps refusing what is not a
migration; it skips only what is one in a language it cannot run.

**`initialize`'s `migrations.language` is the language that session was started for**, which is what
`docs/protocol.md` §3.1's own trailing clause always described ("so a runtime serving more than one can
tell which it was started for"). No example moved and no version line moved.

**`migrations.language` is the default for `migr create` and nothing else reads it.** It stays
required and validated.

**`engine.Options.Runtime` is `func(migration.Migration) string`.** `Options.check` nil-checks it and
`ErrNoRuntime`'s sentence — *"a runtime name is required: every history row records one"* — is
unchanged.

**Several runtime sessions are alive over one pinned `driver.Conn`, and that is written down because it
is safe only for one reason: the engine asks one migration at a time.** It is also detected rather than
trusted — `internal/cli`'s fake counts calls in flight on that session instead of serializing them, and
the untagged suite drives two scripted child runtimes through it, so a future executor that overlapped
two migrations fails a test rather than corrupting a session.

## Consequences

### Positive

- The two-dimensional independence claim is now true of one directory rather than one project: a `.ts`
  migration changes the table a `.go` one created, in timestamp order, under one `migr up`.
- Each history row records its own migration's language, which is what the column always meant.
- The reconciliation is a strictly better diagnostic than what it replaced, and it applies to
  single-language projects too: a `.go` file that forgot to `Register` is now a sentence naming the
  file rather than an `unknown_migration` at execute time.
- `.js` and `.mjs` migrations work again.

### Negative

- Two sessions mean two processes, two handshakes and two `migration.discover` round trips on every
  `up`, `down`, `plan`, and `validate` over a mixed directory. A single-language project pays one
  extra `discover`.
- The Go runner's stamp covers the migrations directory (ADR 0035), so editing a `.ts` file in a mixed
  project invalidates it and rebuilds the runner. Narrowing the stamp to the files Go owns is a change
  to that ADR's own claim and was not attempted.
- The extension set is stated twice — in `internal/source/typescript` and in `sdk/typescript`'s
  `EXTENSIONS` — with a test in the SDK reading the Go constant to hold them together. That is the
  same mitigation the IR's three copies use, and it has the same hole: a fourth language could add a
  set to one side only.
- Nothing declares which toolchains a project needs, so a machine with one of the two finds out when
  it runs rather than when it reads the config.

### Neutral

- `migr status` and `migr history` still build no source, so a mixed directory is reportable on a
  machine with neither toolchain — which is what makes `status` the command to reach for during a
  deploy (ADR 0027).
- Declared dependencies are still not wired into the CLI: `discover` calls `migration.Order(found,
  nil)`, so every `Dependencies()` an SDK reports is collected and thrown away. The mux now holds them
  in its hand, which makes it tempting and still wrong here — ordering happens before any runtime
  starts, and `migr status` has no toolchain to ask. It is its own task.
- One thing this made provable rather than changed: a migration that sends a statement through
  `host.db.exec` and then fails leaves that statement committed (ADR 0032). M7-T8 could not show it
  without leaving a table behind in a database other suites digest; `pgtest.Drop` is what cleans up,
  because nothing in a migration can — a compensating migration whose only effect is an exec compiles
  to no statements and the engine refuses it, and one that dropped the table as an operation would be
  recorded as applied and skipped on the next run.

## Notes

The condition that would justify revisiting the routing decision is a language whose unit of loading
is not a file, or a project that wants two versions of one language at once. Both would make the
extension a weaker key than it is here, and Option 2 is the shape to reach for: the seam is
`initialize`'s `migrations` object, and the reconciliation this record adds is what would hold an
older SDK to the new member's meaning.

[ADR 0040](0040-running-a-go-migration.md) and [ADR 0045](0045-node-runtime-binary.md) are what this
routes between; neither is superseded. The refusal this replaces was M7-T8's, recorded in `TASKS.md`
rather than in an ADR, because a placeholder naming a task is not a decision.
