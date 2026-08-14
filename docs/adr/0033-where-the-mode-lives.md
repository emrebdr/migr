# 0033. The mode belongs to the call, and the engine owns it

## Status

Accepted

## Date

2026-08-14

## Context

M6-T3 built the plan-mode branch and put the mode in the wrong place. `host.Options.Mode` is a
field of the **session**, fixed when the server is constructed, and
[`docs/protocol.md` §4](../protocol.md#migrationexecute) says the opposite in plain words:

> `mode` is a parameter of this call rather than of `initialize` because it belongs to the question
> being asked, not to the session.

The case that makes the difference concrete is §12's second conversation, the one this framework's
credibility rests on. Rolling back a migration that authors no `down` issues two `migration.execute`
calls **on one session**: `{"direction":"down","mode":"apply"}`, which comes back
`no_down_authored`, and then `{"direction":"up","mode":"plan"}` — because that second call is a
question asked in order to invert the answer, and letting `host.db.exec` fire while merely asking
would perform a write the operator never requested. A server carrying one mode cannot serve that.

This is the third disagreement between the specification and the code found in M6, and the first
where the code is the wrong one. ADR 0030's plan-mode plural and ADR 0031's unframeable line were
both holes in the document.

**The other half of the same gap: nothing could ask for a mode.** `engine.Source.Emit(ctx, m, dir)`
carried no mode, so five call sites had no way to state one:

| Call site | Owed |
|---|---|
| `applyOne` → `forward` | `apply` |
| a rollback's `undo` | `apply` |
| a rollback's `derive` | **`plan`, inside an apply run** |
| `Preview` → `compileOne`, both directions | `plan` |
| `validate.Runner`, both directions | `plan` |

Nothing was broken *yet*: the only real `Source` is `internal/source/sql`, which reads a text file
and makes no callbacks. It breaks the day a Go migration is previewed — `migr plan` would fire its
`host.db.exec` writes against a production database while reporting what it *would* do — and again
the day a rollback derives an inverse. Both failures are silent, and the blast radius is at its
smallest now: four `Source` implementations, three of them test fakes.

## Decision Drivers

- The done-when: *"in `plan` mode `host.db.exec` records and returns synthetic success while read
  queries execute live; a test proves plan and apply produce identical IR."*
- §4 is normative and states where the mode lives. A host that disagrees with the specification is a
  bug in the host.
- CLAUDE.md §4.3 already promised the derivation asks in plan mode. Nothing enforced it.
- A mode nothing can vary is not a mode. The identical-IR proof requires asking for one.

## Considered Options

### A. Where the mode lives at the host

- **A1: on the `Call`, stated by `Execute`** ✔. `Execute(migration, dir, mode)` refuses a mode this
  protocol version does not define, `exec` branches on `call.mode`, and `info` reports it. One
  server on one `driver.Conn` serves §12's rollback conversation, which is the whole point.
- A2: on `Options`, as M6-T3 had it. **Rejected** by that conversation: the two calls carry
  different modes and share a session, so the field cannot answer both. It also contradicts §4
  directly, and a normative document losing an argument to an implementation detail is how a
  specification stops being worth reading.
- A3: two servers, one per mode, over one connection. **Rejected**: the window's mutual exclusion
  is per server, so two servers could each hold an open call against one session — which is exactly
  the state `ErrCallOpen` exists to make impossible. It also gives `host.info` two possible answers
  for one session with nothing to choose between them.

### B. Which type the engine names

- **B1: `engine.Mode`, with `protocol.Mode` as the wire spelling** ✔. Core owns the distinction
  because it exists with no runtime in sight: `migr plan` over `.sql` migrations is plan mode, and
  `migr validate` opens no database at all. `internal/source/sql` implements `Source`, speaks no
  JSON-RPC, and should not have a signature claiming otherwise.
- B2: reuse `protocol.Mode` everywhere. **Rejected**: it would make `internal/engine`,
  `internal/validate`, and `internal/source/sql` the first core packages to import the wire package
  — for a value none of them transports. `.golangci.yml` permits that import deliberately, for
  packages that *do* speak the protocol; being permitted is not a reason.
- B3: `ir.Mode`, beside `ir.Direction`. **Rejected**: nothing in `ir` would use it, and that
  package's bytes are a compatibility surface — every applied migration's checksum depends on what
  it emits, so it is the last place to put a type that has no business there.

The cost of B1 is a conversion in M6-T6's adapter and a drift risk, and the drift risk is closed the
way this repository already closes the same one for `capability.Support`:
`TestTheWireSpellsModeAsTheEngineDoes` holds the two sets level.

### C. How the engine states it

- **C1: a parameter of `Emit`, with the invocation's mode on the executor** ✔. `Apply` and
  `Rollback` construct in `ModeApply`, `Preview` in `ModePlan`, and two named helpers make each call
  site read as what it is: `emit` asks in the invocation's mode, `ask` asks as a question whatever
  the invocation is. `derive` calls `ask`, so one apply run issues both modes.
- C2: a value on the context. **Rejected**: an invisible parameter a `Source` can ignore with no
  compiler complaint, whose plan-ness is decided by whoever last wrapped the context rather than by
  the call site that knows.
- C3: leave `Emit` alone and let the runtime-backed source infer the mode. **Rejected**: it cannot.
  Whether an `Emit` is a preview, a run, or a derivation is known only to the executor calling it.

### D. What the identical-IR proof can reach today

- **D1: prove it at the host, with a fixture that plays the SDK** ✔. A Go func issues callbacks
  through the codec; the same func runs twice against one fake session, once per mode; the two
  `ir.Marshal` outputs must be byte-identical. A second fixture branches on a live read and emits
  the same IR while the data is unchanged, and a **different** one across a change — which makes §6's
  documented caveat mechanical instead of merely written down.
- D2: wait for M6-T6's Go runtime. **Rejected**: the property under test is the *host's*. Waiting
  would leave the branch M6-T3 shipped unproven across two more tasks, and the fixture is the same
  move M5-T2 made with a fake driver.

## Decision Outcome

`engine.Mode` is `apply` or `plan`, and `Source.Emit` takes one. The executor carries the
invocation's mode; `derive` overrides it, so a rollback that derives asks the `down` as a run and
the `up` as a question in the same invocation. `migr validate` asks both directions as questions.

At the host, the mode moves from `Options` onto the `Call`. `Execute` states it and refuses one this
protocol version does not define; `Validate` opens a `plan` window, since a self-check is a question
by construction and the two callbacks that could change anything are already refused inside one.

`docs/protocol.md` gains what it never said: what `host.info` reports during a `migration.validate`,
and that `mode` is read per call rather than remembered.

## Consequences

### Positive

- §12's second conversation is serveable, and one test asserts both modes on one server.
- `migr plan` over a language runtime cannot fire the escape hatch, and neither can a derivation.
  Both were latent, silent, and aimed at a production database.
- The identical-IR property is proved rather than assumed, alongside a counterexample that keeps
  the proof from being vacuous — if the fixture could not emit different IR, the first test would
  hold for a migration that never branches on anything.
- `internal/source/sql` gains the same proof at its own level: a text file emits identical
  operations in either mode, which is the floor a language runtime is held to.

### Negative

- **Two Go types spell one concept.** `engine.Mode` and `protocol.Mode` carry the same two values,
  and M6-T6's adapter converts. The agreement test is what keeps that from being a slow leak, and
  the alternative — core importing the wire package — was judged the worse of the two.
- **`Source.Emit` has four parameters**, and `.sql` ignores the fourth. That is the honest shape:
  the mode is meaningless to a text file and load-bearing to a process.
- **The seam between the two mode types is untested until M6-T5.** Nothing converts one to the
  other yet, because nothing yet drives a host from an engine.

### Neutral

- **A Go closure is not an SDK.** This proof shows the host emits identical IR for one migration's
  callbacks in either mode. That a *real* SDK emits identical IR from one migration source is
  `test/conformance/runtime` (M6-T9), and that `migr plan` and `migr up` agree end to end is M6-T10.
- `Call.Statements()` holds what a preview did not run and nothing renders it yet: `engine.Previewed`
  has no field for it, and `Source.Emit` returns only operations. So `migr plan` over a Go migration
  would omit an escape-hatch statement it is about to run. Carrying it out needs `Source` to report
  held statements, which is speculative until something can produce one — M6-T6 is where the first
  `Source` that can.

## Notes

M6-T5 owns the loop that turns a `migration.execute` on the wire into a `Server.Execute` call, and
with it the conversion between the two mode types. `host.Call.Mode()` is what it will report back
through `host.info` for whichever call is in flight.

The mode is stated once per `migration.execute` and nowhere else — not in `initialize`, not in
`migration.validate`. An SDK that caches the first one it sees previews the wrong half of a
rollback, which is why §11 now carries a checklist row about exactly that.
