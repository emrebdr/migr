# migr Runtime Protocol — `protocolVersion` 1

How the engine and a language runtime talk. The engine is a Go binary; a migration is a program in
another language. This document specifies the conversation between them, completely enough to write
an SDK from.

## 1. Scope

### 1.1 What this document specifies

The transport and its framing, the session lifecycle and its handshake, every method in both
directions with its parameters and result, the error codes, cancellation, the timeouts, and what a
runtime process may assume about how it was started.

It is written to be implementable without reading the Go. If something here is ambiguous enough
that two implementations could disagree on the bytes or on the sequence, that is a defect in this
document.

The reference implementation is `internal/runtime/protocol`.

### 1.2 What it deliberately does not specify

- **What an operation means.** The schema operations carried by `host.schema.apply` are the
  Operation IR, specified in [`ir.md`](ir.md). This document carries them; it does not define them.
- **What a statement compiles to.** Translating an operation into engine SQL is the driver
  contract. No engine SQL appears here.
- **How a runtime is found.** Locating a Node installation, resolving `node_modules`, generating
  and building a Go runner — that is each adapter's, in `internal/runtime/<language>`. This
  document specifies what the process may assume once it is running ([§9](#9-the-process)).
- **Policy.** Whether a destructive operation is permitted, whether an operation exceeds a lock
  ceiling, whether a migration may run at all — the validator's and the capability model's.
- **Ordering.** Which migrations run, and in what order, is decided by the engine before a runtime
  is asked to execute anything. A runtime reports what each migration *declares*
  ([`migration.discover`](#migrationdiscover)); it never computes an order.

There is deliberately **no JSON Schema**, for the reason [`ir.md`](ir.md#13-how-to-read-it) gives:
it would be a second specification that can drift from this one.

### 1.3 How to read it

Every fenced block tagged `json` is a complete, valid JSON-RPC message and is **checked by the test
suite**: it must parse, it must carry `"jsonrpc": "2.0"`, and it must survive a round trip through
the reference codec **byte for byte**. Blocks tagged `jsonc` are fragments — a `params` payload on
its own, a shape sketch — and are not checked.

Level-3 headings carry three shapes and the checker tells them apart by which:

- **A method** is its wire name in backticks and nothing else, so `#migrationexecute` addresses it.
  The set of those headings is checked against `protocol.Methods()` — a method in the code with no
  section fails the build, and so does a section naming a method that does not exist.
- **A numbered subsection** opens with its number: `### 2.1 Two streams, two jobs`.
- **A worked conversation** ([§12](#12-two-conversations-in-full)) opens with neither, and every
  checked message beneath it belongs to it. Each must show both directions and must not reuse a
  request `id` within one direction.

The error-code tables in [§7.3](#73-the-application-band) are checked against `protocol.Codes()`
the same way, on both the number and the slug.

Because the round trip is byte-exact, member order, spelling, and the absence of whitespace are part
of what these examples specify, exactly as they are in [`ir.md`](ir.md). An encoder that renders
`id` after `method`, or that pretty-prints, fails the suite here rather than failing an SDK author
later.

Two spellings run through this document and they do not mix:

- **The envelope is camelCase.** `protocolVersion`, `irVersion`, `transactionalDDL`. JSON-RPC's own
  members — `jsonrpc`, `id`, `method`, `params`, `result`, `error`, `code`, `message`, `data` — are
  its own and are never renamed.
- **A payload keeps its own specification's spelling.** The operations inside `host.schema.apply`
  are snake_case because [`ir.md`](ir.md) says so, and re-spelling a payload the engine is about to
  hash is not a liberty this document takes.

---

## 2. Transport and framing

### 2.1 Two streams, two jobs

The engine spawns the runtime as a child process.

| Stream | Direction | Carries |
|---|---|---|
| the child's **stdin** | engine → runtime | protocol messages, one per line |
| the child's **stdout** | runtime → engine | protocol messages, one per line |
| the child's **stderr** | runtime → engine | log output, passed through verbatim, never parsed |

**stdout carries protocol and nothing else.** A runtime whose language prints a warning to stdout
has corrupted the channel. Every SDK must redirect its language's default output to stderr before
the first message, and say so in its own documentation — this is the single most common way a new
SDK fails, and it fails as a parse error three messages later rather than where the print happened.

stderr is passed through as log output. It is never parsed, so it may hold anything: a stack trace,
a progress bar, a language runtime's own diagnostics. Structured logging that should reach the
engine's own logger goes through [`host.log`](#hostlog) instead.

### 2.2 One message per line

Each message is a single JSON-RPC 2.0 object, encoded as UTF-8, followed by one `\n` (U+000A).

- No message contains a raw newline. JSON string escaping guarantees this: U+000A inside a string
  is written `\n`, and the encoder must not pretty-print.
- No byte-order mark, on either stream, at any point.
- `\r\n` is accepted on input and never emitted. A reader strips a trailing `\r`.
- An empty line is ignored. It is not an error, and it is not a message.

There is no `Content-Length` header. The line is the frame.

### 2.3 Line length

A reader must accept lines up to **16 MiB**. A message longer than that is a protocol error: the
reader reports it and the session ends. It is not silently truncated, and it does not cause the
reader to block waiting for a newline it will never get.

16 MiB is not arbitrary. One `host.schema.apply` carries a whole migration's operations, and a
generated migration against a wide schema is the realistic worst case. A runtime that would exceed
it should send several `host.schema.apply` calls — the operations accumulate in order across calls
([`host.schema.apply`](#hostschemaapply)).

### 2.4 What of JSON-RPC 2.0 is not used

- **Batch requests are not supported.** An array at the top level of a line is a protocol error,
  never an implicit batch. Batching saves nothing over a pipe and would make the ordering rules in
  [§2.5](#25-ordering-and-concurrency) considerably harder to state.
- **Only two notifications exist**: [`host.log`](#hostlog) and [`$/cancel`](#cancel). Every other
  method is a request and carries an `id`. A notification for anything else is a protocol error,
  because a caller that does not wait cannot know whether the work happened.
- **`id` is an integer.** JSON-RPC permits strings and null; this protocol does not. Both sides
  number from 1 and increment. There is exactly one exception, and it is not a request: the single
  error response [§2.6](#26-a-line-that-cannot-be-framed) permits for a line that could not be
  attributed to any request carries `"id": null`, because there is nothing else it could carry.

### 2.5 Ordering and concurrency

The channel is bidirectional and both sides may have a request in flight at once. Two rules make
that unambiguous:

**Each side numbers its own requests.** An `id` is unique only within its own direction. An engine
request with `id` 3 and a `host.*` request with `id` 3 are unrelated messages, and a response is
matched to the request traveling the *other* way on the same stream. An implementation that keeps
one table of pending requests for both directions is wrong and will mismatch responses.

**The engine has at most one request in flight.** It sends `migration.execute` and waits. A runtime
may therefore assume it is answering one engine call at a time, and never has to interleave two
migrations.

**A runtime may have several `host.*` requests in flight**, and responses may return out of order.
An SDK that awaits each callback before making the next is correct and simpler; one that pipelines
is also correct. The host answers each by `id`.

While an engine request is in flight, the only message the engine sends is `$/cancel`. It does not
send another request until the current one is answered.

### 2.6 A line that cannot be framed

A line a receiver cannot turn into a message has no readable `id`, so there is no request to answer
and nobody waiting on a response. Three rules cover it, and together they are why a malformed stream
**ends rather than hangs**.

**The session ends** — not that line, the session. A receiver that met a line it could not frame
stops reading, and every later read reports the same failure.

**There is no recovery.** Skipping the line and carrying on is what JSON-RPC suggests for a parse
error, and over a pipe it is wrong: the `id` is exactly the part that could not be read, so whoever
was waiting on that request waits forever. A wedged migration holds the migration lock.

**One error is written first, and it carries `"id": null`.**

```json
{"jsonrpc":"2.0","id":null,"error":{"code":-32700,"message":"line 4 is not JSON","data":{"slug":"parse_error"}}}
```

That is the only place `null` is an `id` ([§2.4](#24-what-of-json-rpc-20-is-not-used)), and the only
message either side sends without having been asked anything. Writing it is best-effort — the peer
may already be gone, and the session ends either way. It exists so that a peer which sent something
malformed learns *what*, rather than finding a closed pipe and an exit code.

These are the failures that reach it:

| Condition | Code |
|---|---|
| the line is not JSON, opens with a byte-order mark, or is not valid UTF-8 | `-32700` |
| the line is longer than [§2.3](#23-line-length) allows | `-32700` |
| the stream ends part way through a line, with no terminating newline | `-32700` |
| the line is JSON and is not a message: a top-level array, a missing or wrong `jsonrpc`, an `id` that is not an integer, a notification for a method that is not one, a response carrying both a result and an error or neither | `-32600` |

A line's length is refused **at the budget**, never after a delimiter that may never arrive. A reader
that accumulates until it finds a newline has a bound on neither memory nor time, and a peer that
opens a line and stops writing is indistinguishable from one that is merely slow.

Two things are deliberately *not* framing failures. An **empty line** is skipped
([§2.2](#22-one-message-per-line)). And a request naming a **method this version does not define**
decodes perfectly well: it is answered `-32601` and the session continues, which is what makes
adding a method a compatible change ([§10.2](#102-what-is-a-breaking-change)).

---

## 3. The session

### 3.1 Lifecycle

```
spawn ──▶ initialize ──▶ [ migration.discover | migration.execute | migration.validate ]* ──▶ shutdown ──▶ exit
```

`initialize` is the first message on the channel and no other request may precede it. A request
that arrives before it is answered with [`session_state`](#73-the-application-band); so is a second
`initialize`, and so is any request after `shutdown` has been answered.

The session is stateless between calls except for what `initialize` established. A runtime is not
required to hold anything from one `migration.execute` to the next, and the engine does not assume
it does.

### `initialize`

Engine → runtime. The handshake.

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersions":[1],"irVersion":1,"client":{"name":"migr","version":"0.1.0"},"migrations":{"dir":"/srv/app/migrations","language":"go"},"database":{"name":"default","driver":"postgres"}}}
```

| Parameter | Meaning |
|---|---|
| `protocolVersions` | every version of this protocol the engine speaks, ascending. Never empty. |
| `irVersion` | the newest Operation IR version the engine reads. Stated, not negotiated; every older version is a subset of it ([`ir.md` §2.2](ir.md#22-irversion)). |
| `client` | the engine's own name and version, for the runtime's diagnostics. |
| `migrations.dir` | absolute path to the migrations directory. Not relative: a runtime's working directory is the project root and the two are not the same thing. |
| `migrations.language` | the language **this runtime was started for**, spelled as `migr.yaml` spells it. One directory may hold several, so this is the one the engine will route to this process and not necessarily the one `migrations.language` holds. |
| `database.name` | the key `migr.yaml` holds the target under. For messages; a runtime never connects. |
| `database.driver` | the engine that will compile the operations, which a migration may branch on. |

No DSN is sent, ever. A runtime does not connect to anything: every read goes through
[`host.db.query`](#hostdbquery).

The result names one version and identifies the implementation:

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"runtime":{"name":"node","version":"24.16.0"},"sdk":{"name":"@migr/sdk","version":"0.1.0"}}}
```

`runtime` is the language runtime — `node`, `go`, `python` — and its version. `sdk` is the library
implementing this protocol. The engine logs both and uses neither to change its behavior.

### 3.2 Version negotiation

The engine offers a set; **the runtime picks one and its choice is binding**. Everything after the
handshake is spoken at that version, by both sides.

A runtime that supports none of the offered versions fails the call with
[`unsupported_protocol`](#73-the-application-band) and lists what it does support:

```json
{"jsonrpc":"2.0","id":1,"error":{"code":-32001,"message":"this SDK speaks protocol versions 2 and 3; the engine offered 1","data":{"slug":"unsupported_protocol","supported":[2,3]}}}
```

A runtime that answers with a version the engine did not offer is a protocol error and the engine
ends the session. Picking from the set is the whole mechanism; there is no counter-offer.

This is a set rather than a single number because an SDK installed from a package registry and a
CLI installed from a release move on different clocks, and neither can make the other upgrade. It
is the runtime that picks rather than the engine because the runtime is the side that knows what it
implements — the engine's list is what it can *accept*.

`irVersion` is **not** negotiated. The engine states the newest version it reads, and because each
version is a superset of the one before ([`ir.md` §2.2](ir.md#22-irversion)) that is a ceiling
rather than a requirement: a runtime **accepts any stated version from 1 up**, emits what it likes
beneath it, and refuses — with [`unsupported_ir`](#73-the-application-band), as the failure of the
`migration.execute` that tried — only an operation newer than the stated version, naming the
operation and both versions. A stated version below 1 fails `initialize` with the same code. So an
SDK newer than the engine runs every migration that does not use what is newer, and an SDK older
than the engine runs everything it can express ([ADR 0077](adr/0077-data-operations.md)). The two
versions are independent lines ([§10.3](#103-four-independent-version-lines)), and before this rule
an IR addition was a protocol break in all but name: an exact-match runtime refused every engine
that had added an operation.

### `shutdown`

Engine → runtime. Asks the runtime to stop.

```json
{"jsonrpc":"2.0","id":9,"method":"shutdown","params":{}}
```

```json
{"jsonrpc":"2.0","id":9,"result":{}}
```

After answering, the runtime closes its stdout and exits **0** within the shutdown grace period
([§8.2](#82-timeouts)). The engine does not send `shutdown` while another request is in flight; it
cancels first ([§8](#8-cancellation-and-timeouts)).

`shutdown` is a request rather than a notification so that the engine knows the runtime received it
before it starts the grace timer. A runtime that exits without answering is indistinguishable from
one that crashed.

---

## 4. Engine → runtime

Three methods beyond the two the session is made of. Each is a request, each names one migration by
its version except `migration.discover`, and each may fail with any code
[§7.3](#73-the-application-band) marks as a runtime's — in particular
[`unknown_migration`](#73-the-application-band), for an `id` naming nothing this runtime loaded.

### `migration.discover`

Asks what migrations this runtime can execute, and what each one declares.

```json
{"jsonrpc":"2.0","id":2,"method":"migration.discover","params":{}}
```

```json
{"jsonrpc":"2.0","id":2,"result":{"migrations":[{"version":"20260727T153000","file":"20260727T153000_add_users.go","dependencies":[]},{"version":"20260727T153100","file":"20260727T153100_add_teams.go","dependencies":["20260727T153000"]}]}}
```

The runtime loads every migration in `migrations.dir` that it can execute and reports, for each:
its `version`, the `file` it was loaded from, and the versions its `Dependencies()` **declares**.

Three things this deliberately is not:

- **It is not the file discovery.** The engine has already read the directory and parsed every
  filename against the grammar; a file that is not a migration has already failed the command. This
  call reports what the runtime could *load*, which is a different question, and the engine
  reconciles the two — a version the engine discovered and the runtime did not load is a failure
  naming the file.

  One directory may hold several languages, so "every migration in `migrations.dir` that it can
  execute" is what it says: a file whose extension belongs to another runtime is **skipped**, not
  refused, and a file that is not a migration at all is still refused. The engine routed every file
  by its extension and holds this result against what it routed here — in **both** directions, so a
  migration two runtimes both claim is a failure as surely as one nothing loaded. That reconciliation
  is what lets a loader skip anything at all: nothing is quietly ignored by every runtime at once.
  See [ADR 0048](adr/0048-multi-language-projects.md).
- **It is not an ordering.** The result's order carries no meaning. The engine sorts by version and
  overlays the declared dependencies itself — over **every** runtime's answer at once, so an edge may
  name a version another runtime loaded, and a cycle or an edge naming nothing fails the command
  before any migration runs. Ordering therefore settles after every session has been started, which
  is why a command that starts none prints version order and says so. See
  [ADR 0049](adr/0049-reading-a-declared-dependency.md).
- **It is not a computation.** `dependencies` is read from the migration, never derived from a
  query. A graph that could branch on live data would let `migr plan` and `migr up` order the same
  migrations differently, and would put `migr status` behind a language toolchain. See
  [ADR 0018](adr/0018-declared-dependencies.md).

`dependencies` names **versions**, not filenames. The version is the key the engine proved unique,
and the one `--to` and a history row already use.

A migration that is **one step of a change carried out across deploys** also reports `phase`, and a
migration that is not omits the member. A `phase` names the `change` it belongs to, the step's own
`name`, and — where the step waits on another — the step it comes `after`, which is omitted where it
waits on none:

```json
{"jsonrpc":"2.0","id":2,"result":{"migrations":[{"version":"20260727T160300","file":"20260727T160300_backfill_email.go","dependencies":[],"phase":{"change":"users.email","name":"backfill","after":"expand"}}]}}
```

The engine resolves every `after` to the migration declaring that step of the same change, refusing
one that names nothing and one that runs later, and then runs a step only under a **release** after
the one its prior step was applied under — so the steps of one change cannot land in one deploy. Like
`dependencies` it is read from the migration and never computed. It was added as an optional result
member, which [§10.2](#102-what-is-a-breaking-change) makes no break: an engine that does not know it
ignores it, and an SDK that does not send it declares no step. See
[ADR 0087](adr/0087-release-gated-phases.md).

A migration that runs against a live table **through a shadow table** rather than as a plain `ALTER`
reports `online`, naming the strategy, and a migration that does not omits the member. The one
strategy is `shadow_table`: the table is copied into a shadow kept current from the server's
replication log, and swapped in only once someone confirms the cutover:

```json
{"jsonrpc":"2.0","id":2,"result":{"migrations":[{"version":"20260727T160400","file":"20260727T160400_retype_amount.go","dependencies":[],"online":"shadow_table"}]}}
```

The engine refuses a strategy it does not know at discovery, naming the migration, and a server that
cannot read its own log is refused before anything is created. Like `phase` it is an optional result
member, read from the migration and never computed. See
[ADR 0089](adr/0089-shadow-table-lifecycle.md).

### `migration.execute`

Runs one migration in one direction. This is where a migration's own code runs.

```json
{"jsonrpc":"2.0","id":3,"method":"migration.execute","params":{"id":"20260727T153000","direction":"up","mode":"apply"}}
```

| Parameter | Values |
|---|---|
| `id` | the migration's version, as `migration.discover` reported it |
| `direction` | `"up"` or `"down"` |
| `mode` | `"apply"` or `"plan"` — see [§6](#6-plan-mode) |

The migration's operations do **not** come back in the result. They arrive during the call, through
[`host.schema.apply`](#hostschemaapply), in the order the migration emits them. The result reports
how many were emitted, so the engine can check its own count against the runtime's:

```json
{"jsonrpc":"2.0","id":3,"result":{"operations":2}}
```

A disagreement between that number and what the host recorded is a protocol error, not a rounding
difference. It means one side dropped a call.

**A `down` has three honest answers**, and two of them are errors rather than empty results
([§7.4](#74-the-two-refusals-that-are-not-failures)): the operations; `no_down_authored`, which has
the engine derive an inverse; and `declared_irreversible`, which halts the rollback with the
author's reason.

`mode` is a parameter of this call rather than of `initialize` because it belongs to the question
being asked, not to the session. It is stated once, here, and nowhere else — and the reason it has
to be per-call is the next paragraph.

**An `up` is sometimes asked for during a rollback, and it is always asked for in `plan` mode.**
When a migration answers `no_down_authored`, the engine asks that same migration what it *does* and
inverts the result. That second call is a question, not a run: nothing the engine is about to
execute comes from it directly, and letting [`host.db.exec`](#hostdbexec) fire while merely asking
would perform a side effect the operator never requested. So a rollback of a migration with no
authored down issues `{"direction":"up","mode":"plan"}` even though the run itself is an apply. An
SDK sees an ordinary `up`; the difference is entirely the host's.

### `migration.validate`

Runs one migration's own `Validate()` and nothing else.

```json
{"jsonrpc":"2.0","id":4,"method":"migration.validate","params":{"id":"20260727T153000"}}
```

```json
{"jsonrpc":"2.0","id":4,"result":{}}
```

A migration that considers itself invalid fails the call with
[`migration_failed`](#73-the-application-band) and a message its author wrote.

This is the migration's self-check, and it is **not** `migr validate`. That command reads every
migration, compiles both directions for the configured engine, and checks that something can undo
each one, reporting every problem rather than the first — see
[ADR 0028](adr/0028-validation-report.md). This call is one of the things it may ask.

`Validate()` must not emit operations. A `host.schema.apply` during `migration.validate` is a
protocol error: the engine has nothing to do with them and recording them would make validation
change what a later apply emits. A `host.db.exec` is refused for the same reason stated the other
way round — a self-check that wrote to the database would not be one.

**There is no `mode` here, and [`host.info`](#hostinfo) reports `"plan"` inside this call.** A
self-check is a question by construction: nothing it says is executed, and the two callbacks that
could change anything are refused outright rather than recorded. A runtime that branches on the mode
therefore sees a validation as the preview it is.

---

## 5. Runtime → engine

The callbacks. These are what make a migration a program rather than a document: it can ask the
database a question and branch on the answer.

### 5.1 What a callback may assume

A `host.*` request is only valid while an engine request is in flight — during
`migration.execute`, or during `migration.validate` for the read-only ones. Outside that, the host
answers [`session_state`](#73-the-application-band).

`host.*` is reserved to this direction, permanently. A runtime never serves a method under that
prefix and the engine never sends one.

### `host.schema.apply`

Records the operations a migration performs. This is how a migration says what it changes.

```json
{"jsonrpc":"2.0","id":1,"method":"host.schema.apply","params":{"operations":[{"op":"create_table","table":"users","columns":[{"name":"id","type":"int64","nullable":false}]}]}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"recorded":1}}
```

The `operations` array is an Operation IR operations array — **the array alone, without the
envelope**. `irVersion` was agreed at `initialize` and is not repeated per call.

**The array is decoded and re-encoded canonically before it is hashed**, and it is that canonical
form the checksum covers ([`ir.md` §4](ir.md#4-the-checksum-boundary)). So an SDK's whitespace, its
member order, or its choice to send ten operations in one call rather than one in each cannot change
a migration's identity — which is what lets two SDKs be *semantically* identical without being
byte-identical here. Byte-identical IR is still what `test/conformance/runtime` requires of them,
and it is required for a different reason: it is the proof that they mean the same thing.

Several calls accumulate, in the order made. A migration that emits ten operations in one call and
a migration that makes ten calls of one produce the same list.

**It records in both modes, and that is not a plan-mode exception — it is what this callback is.**
The engine collects a migration's whole operation list, hashes it, compiles it through the driver,
groups the statements, and executes them inside one transaction with the history row written before
it commits. None of that can begin until the migration has finished emitting. So `host.schema.apply`
never executes anything, in any mode; see [§6](#6-plan-mode).

An operation that is not a valid IR document fails the call with
[`invalid_operation`](#73-the-application-band), naming its position in the array. The migration
sees an error at the point it made the call, which is where its author can act on it.

### `host.db.query`

Reads from the database. **This is the only way a runtime sees any data.**

```json
{"jsonrpc":"2.0","id":2,"method":"host.db.query","params":{"sql":"SELECT count(*) AS n FROM users WHERE email IS NULL","args":[]}}
```

```json
{"jsonrpc":"2.0","id":2,"result":{"columns":["n"],"rows":[["4711"]]}}
```

**Every value is a string or `null`.** Not a number, not a boolean. A 64-bit integer does not
survive a round trip through a JSON number in JavaScript, and a migration that backfills a table
keyed on `bigint` is exactly the case that breaks — silently, at row 2^53. Each SDK converts to its
own types from the string, where it knows the column's type and JSON does not.

`args` are bound parameters, also strings, and positional. The placeholder style is the target
engine's — `$1`, `$2` on PostgreSQL, `?` on MySQL and SQLite — and `database.driver` from
`initialize` is how a migration knows which. That is a genuine leak of the engine into a migration,
and it is the reason the Operation IR exists: a migration that only emits operations is portable,
and one that queries is portable only as far as its SQL is. String concatenation into `sql` is an
SDK's own decision to make badly.

**Read-only is enforced by the host, not by convention.** A statement that could write fails with
[`not_read_only`](#73-the-application-band) and nothing runs. How the host establishes that is its
own business and is not specified here; that it does is. A migration that needs to write does so
through `host.schema.apply`, which is what keeps plan mode meaningful.

It executes live in **both** modes. A preview that did not let a migration read would take a
different branch than the apply for a reason that had nothing to do with the database.

### `host.db.exec`

The escape hatch: run a statement the Operation IR cannot express.

```json
{"jsonrpc":"2.0","id":3,"method":"host.db.exec","params":{"sql":"ANALYZE users","args":[]}}
```

```json
{"jsonrpc":"2.0","id":3,"result":{"rowsAffected":"0","executed":true}}
```

`rowsAffected` is a string for the reason `host.db.query`'s values are.

**This is the one callback plan mode changes.** In `plan` mode it is recorded and not executed, and
the result says so with `"executed": false` — so a migration that branches on `rowsAffected` can
tell it is being previewed rather than being told a lie.

**It runs outside the transaction the migration's operations will run in**, and that is the second
thing it costs. Every callback fires while the migration is deciding what it does, which is before
the engine has compiled anything or opened a transaction — so an escape-hatch write is committed on
its own, under the migration lock, and a migration that fails afterwards **does not undo it**. A
statement whose undo matters belongs in `host.schema.apply`, where the engine can invert it, or in
an authored `down`.

Using it also costs what `raw` costs in the IR: the engine cannot read the statement, so it cannot
invert it, cannot classify its lock impact, and cannot tell another engine what it means. Reach for
it last.

### `host.capability.get`

Asks what the target engine makes of an operation, before emitting it.

```json
{"jsonrpc":"2.0","id":4,"method":"host.capability.get","params":{"operation":{"op":"drop_column","table":"users","column":"legacy"}}}
```

```json
{"jsonrpc":"2.0","id":4,"result":{"support":"emulated","strategy":"table rebuild","lock":"table_rewrite"}}
```

`support` is `native`, `emulated`, or `unsupported`. `strategy` is present only for `emulated`;
`reason` only for `unsupported`. `lock` is `lock_free`, `brief_exclusive`, `long_exclusive`,
`table_rewrite`, or `unknown`.

The parameter is a **whole operation**, not an operation kind. A driver refuses payloads rather than
kinds — MySQL's answer for `alter_column` depends on the column's type — so a question phrased as a
kind cannot be answered. See [ADR 0015](adr/0015-payload-dependent-refusals.md).

A migration is not obliged to ask. This exists so one can adapt — take a different route when an
engine would rewrite a table — not so that every operation must be cleared first.

### `host.info`

Reports the facts about this run a migration may branch on.

```json
{"jsonrpc":"2.0","id":5,"method":"host.info","params":{}}
```

```json
{"jsonrpc":"2.0","id":5,"result":{"database":"default","driver":"postgres","server":"17.2","transactionalDDL":true,"mode":"apply"}}
```

`server` is the version string the driver detected at `Open`, verbatim. A migration that parses it
is coupled to one engine's spelling; `host.capability.get` is the portable question.

`mode` is **the mode of the call this callback arrived inside**, not a property of the session —
which is what lets one rollback ask the same migration for its `down` as a run and then for its `up`
as the question the inverse is derived from ([§4](#migrationexecute)). Inside a
[`migration.validate`](#migrationvalidate) it is `"plan"`.

### `host.log`

A structured log line, addressed to the engine's own logger. **A notification** — no `id`, no
response.

```json
{"jsonrpc":"2.0","method":"host.log","params":{"level":"info","message":"backfilling in chunks","fields":{"chunk":"5000","table":"users"}}}
```

`level` is `debug`, `info`, `warn`, or `error`, and the engine applies its own `-v` threshold. Every
value in `fields` is a string.

This is not the same channel as stderr. stderr is passed through verbatim and is for whatever the
language prints; `host.log` reaches `migr --json`'s log stream as an object a script can filter.
Both exist because a migration has both kinds of thing to say.

---

## 6. Plan mode

`migration.execute` runs the migration's real code in both modes. What changes is small enough to
state in one table:

| Callback | `apply` | `plan` |
|---|---|---|
| `host.schema.apply` | records | records |
| `host.db.query` | executes | executes |
| `host.db.exec` | executes | records, `"executed": false` |
| `host.capability.get` | answers | answers |
| `host.info` | answers, `mode: "apply"` | answers, `mode: "plan"` |
| `host.log` | logs | logs |

That `host.schema.apply` is identical in both is the point, not an oversight. The engine never
executes an operation as it arrives: it collects a migration's whole list, hashes it, compiles it
through the driver, and runs the statements in one transaction. So there is nothing for plan mode
to switch off — the recording *is* the behavior, and `migr plan` and `migr up` share it exactly.

Only the escape hatch differs, because it is the only callback that reaches the database directly.

**A migration that branches on live data may plan differently than it applies.** If it reads a row
count and takes one of two paths, the plan reflects the state at plan time and the apply reflects
the state at apply time. That is inherent to the expressiveness this transport was chosen for. It is
documented rather than hidden, because a preview that could be wrong in a way a user cannot detect
is worse than no preview. See [ADR 0002](adr/0002-runtime-protocol.md).

---

## 7. Errors

### 7.1 The shape

A failure is a JSON-RPC error object. `message` is one line, in the terms whoever reads it can act
on. `data` is an object and always carries `slug`:

```jsonc
{
  "code": -32012,
  "message": "this migration authors no down of its own",
  "data": { "slug": "no_down_authored" }
}
```

The slug is what an SDK branches on. Branching on the number works and reads badly; branching on
`message` is wrong, because the message is prose and prose changes.

### 7.2 The reserved codes

JSON-RPC 2.0's own, used exactly as it defines them. They carry a slug like every other error, since
[§7.1](#71-the-shape) admits no exemption and an SDK that has to branch on `-32700` by number is one
that will write the number down wrong:

| Code | Slug | When |
|---|---|---|
| `-32700` | `parse_error` | the line is not JSON, or could not be framed ([§2.6](#26-a-line-that-cannot-be-framed)) |
| `-32600` | `invalid_request` | the line is JSON but not a valid request — a batch array, a missing `jsonrpc`, a notification for a method that is not one |
| `-32601` | `method_not_found` | the method is not one this protocol version defines |
| `-32602` | `invalid_params` | the parameters are the wrong shape, or a required one is missing |
| `-32603` | `internal_error` | an internal failure with no better description |

`-32601` is what a runtime returns for a method from a **later** protocol version. That is what
makes adding a method a compatible change: an engine that speaks version 2 and negotiates down to 1
never sends it, and an engine with a bug that does sends it gets a clear answer rather than silence.

### 7.3 The application band

JSON-RPC 2.0 reserves `-32000` to `-32099` for the implementation. Every migr code lives there, and
they are all assigned here.

> LSP's `-32800` for a canceled request is deliberately **not** reused. It sits in `-32768` to
> `-32000`, which JSON-RPC reserves for its own predefined errors, and a specification that claims
> to be unambiguous should not borrow from a band it does not own.

**Reported by a runtime:**

| Code | Slug | Meaning |
|---|---|---|
| `-32001` | `unsupported_protocol` | none of the offered protocol versions is supported |
| `-32002` | `unsupported_ir` | a stated `irVersion` below 1, or an operation newer than the stated one |
| `-32003` | `session_state` | the method arrived when the session could not answer it |
| `-32010` | `unknown_migration` | the `id` names no migration this runtime loaded |
| `-32011` | `migration_failed` | the migration's own code raised |
| `-32012` | `no_down_authored` | the migration carries no down of its own |
| `-32013` | `declared_irreversible` | its author stated it cannot be undone |
| `-32014` | `irreversible_operation` | an operation could not be inverted |

**Reported by the host:**

| Code | Slug | Meaning |
|---|---|---|
| `-32020` | `not_read_only` | `host.db.query` was given a statement that could write |
| `-32021` | `invalid_operation` | a schema operation is not a valid IR document |
| `-32022` | `database` | the server refused the statement |

**Reported by either:**

| Code | Slug | Meaning |
|---|---|---|
| `-32030` | `canceled` | the request was abandoned because the other side asked |

### 7.4 The two refusals that are not failures

`no_down_authored` and `declared_irreversible` are the reason this section exists. Both are answers
to *"how is this migration undone"*, both are ordinary, and the engine does something specific with
each.

**`no_down_authored`** — the migration has an `Up` and no `Down`. The engine asks for its `up`
operations instead and inverts them through the IR's own reversal rules, so the author gets a
rollback without writing one. An operation with no inverse then halts the **whole** rollback before
its first statement, because a rollback runs newest first and one that stopped partway would leave
migrations whose successors were already undone. See
[ADR 0022](adr/0022-rollback-executor.md).

**`declared_irreversible`** — the author stated, in the migration, that nothing can undo it, and
gave a reason. `data.reason` carries it, and the engine refuses the rollback with **that** reason
rather than with a generic one:

```json
{"jsonrpc":"2.0","id":3,"error":{"code":-32013,"message":"this migration declares that it cannot be undone","data":{"slug":"declared_irreversible","reason":"the rows are gone; restore from a backup"}}}
```

Every SDK owes both. Without `no_down_authored` a migration would have to author an inverse the
framework could have derived; without `declared_irreversible` a legitimately one-way migration could
never pass validation, since there would be no way to say so. That second hole was found and closed
for `.sql` migrations in [ADR 0028](adr/0028-validation-report.md), and this is the same hole on
the wire.

An SDK that reports a missing `Down` as `migration_failed` is not merely imprecise: it turns every
derivable rollback into a failed one.

**A `Down` that emits no operations is a third answer, and it is not a refusal.** The result is an
ordinary one carrying no operations, and the engine records the rollback with nothing run: the
author is saying there is nothing to undo, as the backfill phase of a column replacement does. An
`up` that emits nothing is still refused, as is a `down` the engine would have had to derive. See
[ADR 0079](adr/0079-expand-contract-helper.md).

---

## 8. Cancellation and timeouts

### `$/cancel`

Either side may send it. **A notification** — no `id` of its own, no response.

```json
{"jsonrpc":"2.0","method":"$/cancel","params":{"id":3}}
```

`params.id` names a request the sender previously made and is still waiting on. Canceling an
unknown or already-answered id is ignored, silently: the race is unavoidable and reporting it would
mean answering a notification.

The receiver stops as soon as it can and answers the named request with
[`canceled`](#73-the-application-band). Answering it is not optional — the canceled request is
still a request, and the sender is still waiting on that `id`.

`$` prefixes it because it is protocol machinery rather than a domain call, following LSP's
convention. It is the one method that is neither `host.*` nor a migration operation, and the one a
receiver may implement as a no-op without failing the handshake — but see below for what that
costs.

### 8.1 The guarantee is the kill

If the runtime does not answer the canceled request within the cancel grace period, the engine
kills the process. That is the guarantee; the notification is the courtesy.

Both halves matter. Without the notification, a migration backfilling a table in chunks has no
chance to stop between chunks and its resume point has to be reconstructed. Without the kill, a
wedged runtime holds the migration lock until `lock.timeout` and cancellation is a request rather
than a promise.

An SDK that ignores `$/cancel` is correct-but-rude: it works, and `Ctrl-C` costs it a SIGKILL
mid-migration.

### 8.2 Timeouts

| What | `migr.yaml` | Default | Exceeded |
|---|---|---|---|
| handshake — spawn to `initialize` result | `runtime.handshake_timeout` | 30s | kill; report that the runtime never started |
| `migration.discover`, `migration.validate` | `runtime.request_timeout` | 60s | `$/cancel`, then kill |
| `migration.execute` | none | **none** | bounded by the command's own context, not by a timer here |
| cancel grace — `$/cancel` to the canceled response | `runtime.cancel_grace` | 5s | kill |
| shutdown grace — `shutdown` result to process exit | `runtime.shutdown_grace` | 5s | SIGTERM, then SIGKILL 5s later |

`migration.execute` has no timeout of its own on purpose. A backfill over a large table legitimately
runs for hours, and a default that killed it would be a data-loss bug wearing a safety flag. It ends
when the operator's `migr up` ends: `Ctrl-C` cancels the context, which sends `$/cancel`. It has no
`migr.yaml` key either, so there is nothing for an operator to set to the value that would break it.

These defaults are the protocol's, and the middle column is where a project overrides one. An engine
that read a default from anywhere but this specification would let two installations disagree about
what "the handshake timeout" is while both claiming to implement version 1.

---

## 9. The process

What a runtime may assume, and what it owes. How it is *found* and *started* is the adapter's, and
is not specified here ([§1.2](#12-what-it-deliberately-does-not-specify)).

**On start:**

- stdin, stdout, and stderr are connected as [§2.1](#21-two-streams-two-jobs) describes. All three
  are pipes; none is a terminal.
- The working directory is the **project root** — the directory holding `migr.yaml`. It is not the
  migrations directory, whose absolute path arrives in `initialize`.
- The environment is the engine's own, plus whatever the adapter adds for its language. No secret is
  passed: a runtime never connects to a database, so it never needs a DSN.
- No command-line contract is specified. A runtime is started however its adapter starts it and must
  not depend on argv.

**On exit:**

- After answering `shutdown`, close stdout and exit **0**.
- Any other exit is a failure. The engine reports the exit code together with the last lines of
  stderr, which is the only reason stderr is buffered at all.
- A runtime must not exit while an engine request is unanswered. If it must, the unanswered request
  becomes a failure naming the exit code, which is strictly less useful than an error response.

**The engine owes the process one thing:** it does not outlive the engine. An orphaned runtime
holding a database connection through `host.*` callbacks that nothing will answer is a leak with a
lock behind it.

The engine enforces it by starting the runtime in a **process group of its own** and ending that
group — not that process — on every path out: a clean `shutdown`, a failed handshake, a canceled
request the runtime ignored, and the operator's `Ctrl-C`. A runtime that forked a helper therefore
cannot leave one behind either. There is one edge the engine cannot cover, and a runtime is what
covers it: if the engine is itself killed outright, **the child's stdin reaches end of file**,
because the engine held the only other end of that pipe. A runtime that reads EOF on stdin has lost
its engine and must exit — that is the last thing this section asks of it, and the only thing
standing between a killed `migr` and a runtime that runs forever.

---

## 10. Versioning and stability

### 10.1 What `protocolVersion` 1 covers

The framing, the set of methods and their names, every parameter and result member with its
spelling and type, the error codes and their slugs, the notification set, the ordering rules, and
the lifecycle.

### 10.2 What is a breaking change

Anything a current implementation on either side could send that the other would reject: removing
or renaming a method, parameter, or result member; changing a member's type; making an optional
parameter required; extending a closed enum such as `direction`, `mode`, `support`, or `level`;
changing an error code's number or slug; or changing the ordering rules.

A breaking change bumps `protocolVersion`, updates this document, and updates the conformance run
for every SDK — [CLAUDE.md invariant #5](../CLAUDE.md).

**Adding** a method, an optional parameter, or a result member is not breaking. An implementation
that does not know a method answers `-32601` and one that does not know a member ignores it. This is
why the version is a *set* offered at `initialize`: an engine may speak 1 and 2, and talk to two
SDKs of different ages in the same project.

### 10.3 Four independent version lines

The framework version, the `irVersion`, this `protocolVersion`, and the checksum scheme. Nothing
couples them. A reader who assumes an IR addition implies a protocol bump will make a wrong change;
`initialize` carries both numbers precisely so that neither has to imply the other.

---

## 11. What an SDK owes

Everything above, restated as a list, because a specification that cannot be checked off is one an
implementer has to re-read to know whether they are finished.

**The channel**

- [ ] Nothing but protocol reaches stdout. The language's default print target is redirected to
      stderr before the first message.
- [ ] One message per line, UTF-8, no pretty-printing, no BOM.
- [ ] Lines up to 16 MiB are read without blocking or truncating, and a longer one is refused at
      the budget rather than after a newline that may never arrive.
- [ ] A top-level array is a protocol error, not a batch.
- [ ] A line that cannot be framed ends the session, after one best-effort `"id": null` error and
      with no attempt to recover and read the next one ([§2.6](#26-a-line-that-cannot-be-framed)).
- [ ] Requests are numbered from 1 in this direction's own sequence, and a response is matched
      against the requests traveling the other way.

**The session**

- [ ] `initialize` is answered with exactly one version from `protocolVersions`, or with
      `unsupported_protocol` listing what is supported.
- [ ] Any stated `irVersion` of 1 or above is accepted; an operation newer than it is
      `unsupported_ir` before it is sent, naming the operation and both versions — never sent and
      left for the engine to call unknown.
- [ ] Any request before `initialize`, a second `initialize`, or anything after `shutdown` is
      `session_state`.
- [ ] `shutdown` is answered, then stdout is closed and the process exits 0.
- [ ] **End of file on stdin ends the process.** It means the engine is gone, and it is the one
      case the engine's own process-group kill cannot reach ([§9](#9-the-process)).

**The methods**

- [ ] `migration.discover` reports every migration this SDK can load, with its declared
      dependencies as versions — read from the migration, never computed.
- [ ] `migration.execute` emits through `host.schema.apply` and returns the count it emitted.
- [ ] A migration with no `Down` answers `no_down_authored`. **Not** `migration_failed`, and not an
      empty operation list.
- [ ] A migration declaring itself one-way answers `declared_irreversible` with `data.reason`.
- [ ] `migration.validate` runs the migration's own check and emits no operations.
- [ ] An unknown `id` is `unknown_migration`; a method from a later version is `-32601`.

**Behaviour**

- [ ] `mode` is honoured only where [§6](#6-plan-mode) says it differs — which is
      `host.db.exec` and nothing else.
- [ ] `mode` is read from each `migration.execute` rather than remembered from the session: one
      rollback issues both, and an SDK that cached the first would preview the wrong half.
- [ ] `$/cancel` abandons the named request and answers it with `canceled`.
- [ ] Every error carries `data.slug`, the five reserved codes included.

**Proof**

- [ ] The SDK passes the runtime conformance suite (`test/conformance/runtime`), producing
      **byte-identical IR** to every other SDK for the same fixture migrations. That suite is what
      turns "identical semantics across languages" into a claim a build can fail.

---

## 12. Two conversations, in full

### Applying one migration

`migr up` against a project with one Go migration. Engine requests are numbered from the engine's
sequence, `host.*` requests from the runtime's; the two sequences are unrelated
([§2.5](#25-ordering-and-concurrency)).

```json
{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersions":[1],"irVersion":1,"client":{"name":"migr","version":"0.1.0"},"migrations":{"dir":"/srv/app/migrations","language":"go"},"database":{"name":"default","driver":"postgres"}}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"protocolVersion":1,"runtime":{"name":"go","version":"1.26.3"},"sdk":{"name":"github.com/emrebdr/migr/sdk/go","version":"0.1.0"}}}
```

```json
{"jsonrpc":"2.0","id":2,"method":"migration.discover","params":{}}
```

```json
{"jsonrpc":"2.0","id":2,"result":{"migrations":[{"version":"20260727T153000","file":"20260727T153000_add_users.go","dependencies":[]}]}}
```

```json
{"jsonrpc":"2.0","id":3,"method":"migration.execute","params":{"id":"20260727T153000","direction":"up","mode":"apply"}}
```

The migration asks a question before deciding what to emit:

```json
{"jsonrpc":"2.0","id":1,"method":"host.db.query","params":{"sql":"SELECT count(*) AS n FROM information_schema.tables WHERE table_name = $1","args":["users"]}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"columns":["n"],"rows":[["0"]]}}
```

```json
{"jsonrpc":"2.0","id":2,"method":"host.schema.apply","params":{"operations":[{"op":"create_table","table":"users","columns":[{"name":"id","type":"int64","nullable":false}]}]}}
```

```json
{"jsonrpc":"2.0","id":2,"result":{"recorded":1}}
```

```json
{"jsonrpc":"2.0","id":3,"result":{"operations":1}}
```

Nothing has touched the schema yet. The engine now hashes the operations array, compiles it through
the PostgreSQL driver, and runs the statements in one transaction with the history row written
before it commits.

```json
{"jsonrpc":"2.0","id":4,"method":"shutdown","params":{}}
```

```json
{"jsonrpc":"2.0","id":4,"result":{}}
```

### Rolling back a migration that authors no down

The same session, up to `migration.execute`. The engine asks for the `down` first, and the runtime
says there is not one:

```json
{"jsonrpc":"2.0","id":3,"method":"migration.execute","params":{"id":"20260727T153000","direction":"down","mode":"apply"}}
```

```json
{"jsonrpc":"2.0","id":3,"error":{"code":-32012,"message":"this migration authors no down of its own","data":{"slug":"no_down_authored"}}}
```

That is not a failure. The engine asks the same migration what it *does*, and inverts it:

```json
{"jsonrpc":"2.0","id":4,"method":"migration.execute","params":{"id":"20260727T153000","direction":"up","mode":"plan"}}
```

```json
{"jsonrpc":"2.0","id":1,"method":"host.schema.apply","params":{"operations":[{"op":"create_table","table":"users","columns":[{"name":"id","type":"int64","nullable":false}]}]}}
```

```json
{"jsonrpc":"2.0","id":1,"result":{"recorded":1}}
```

```json
{"jsonrpc":"2.0","id":4,"result":{"operations":1}}
```

The inverse of `create_table` is `drop_table`, and the engine derives it without asking the runtime
again. Note the `mode`: the engine is asking what the migration *would* do in order to invert it,
and must not let the escape hatch run while it does.

Had the migration instead declared itself irreversible, `id` 3 would have returned `-32013` with the
author's reason, and the rollback would have stopped there — for every migration in the batch, not
only this one.

---

## 13. Decision records

The reasoning behind this protocol, including the options that were rejected:

| # | Decision |
|---|---|
| [0002](adr/0002-runtime-protocol.md) | Bidirectional JSON-RPC over stdio for language runtimes |
| [0018](adr/0018-declared-dependencies.md) | A dependency is declared by a migration, never computed |
| [0022](adr/0022-rollback-executor.md) | A rollback inverts the whole batch before it runs any of it |
| [0028](adr/0028-validation-report.md) | A validator reports in phases |
| [0030](adr/0030-runtime-protocol-specification.md) | The runtime protocol, specified |
| [0031](adr/0031-protocol-codec-and-framing.md) | The codec, and what ends a session |
| [0032](adr/0032-host-callback-server.md) | The host callback server, and the SQL a migration wrote |
| [0033](adr/0033-where-the-mode-lives.md) | The mode belongs to the call, and the engine owns it |
| [0034](adr/0034-runtime-process-lifecycle.md) | The runtime process, and what outlives what |
| [0049](adr/0049-reading-a-declared-dependency.md) | A declared dependency is read once the runtimes are up |
