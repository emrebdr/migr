# 0011. The driver contract: a session-scoped connection and an explicitly wired registry

## Status

Accepted

## Date

2026-07-28

## Context

`migr` translates a neutral operation IR into statements a particular database engine
understands. The boundary where that happens is the driver contract: the set of methods every
supported engine implements, and the mechanism by which a driver becomes reachable by name from a
configuration file.

The whole extensibility claim rests on this contract. Adding PostgreSQL, SQLite, MySQL, and later
MongoDB or DynamoDB must require no change to anything outside the new driver's own package. A
contract that is wrong here does not merely need refactoring later — it needs refactoring in every
driver at once, which is the situation this project exists to avoid.

An earlier sketch of the contract existed as a single interface: a stateless driver with seven
methods, three of which took a connection as a parameter. Writing it out as code surfaced three
problems.

**Three of its methods name types that do not exist yet.** The capability model, the history store,
and the schema snapshot each arrive in later work. One of them — the history store — is scheduled
*after* this contract, so the contract would name a type whose own definition depends on it.

**The history store cannot bind to a connection.** It sat on the stateless driver, alongside
methods that each received a connection separately. A history store that cannot reach a database
is not a store, and there was no point at which one could be given a connection to use.

**A connection could be handed to the wrong driver.** With `Introspect(ctx, conn)` and
`Lock(ctx, conn)` on a stateless driver, passing a MySQL connection to the PostgreSQL driver is a
type-correct program. Nothing but discipline prevents it.

There is also nowhere to record the connected server's version. That is not a nicety: a driver's
lock-impact verdict must be derived from the server it is actually talking to, never from
documentation, and a connection-free method cannot know which server that is.

Separately, the registration mechanism had been assumed rather than decided. The convention
sketched was the one Go's own standard library uses — each driver registers itself from an `init`
function, pulled in by a blank import — but that convention cannot report an error, and this
contract is required to treat a duplicate name as one.

## Decision Drivers

- **Wrong pairings should not compile.** A contract that relies on callers being careful is a
  contract that will eventually be used carelessly.
- **No type may exist before something can honestly implement it.** A method whose only possible
  implementation returns a plausible empty value is worse than a missing method.
- **Errors are returned to somewhere that can act on them.** This project forbids `panic` in
  library code, and a duplicate driver name is required to be an error.
- **Every driver pays for a mistake here.** The contract is the one artifact whose cost of change
  scales with the number of engines supported.
- **Locks must always be released.** Whatever shape the contract takes, it must make the release
  path obvious and hard to get wrong.

## Considered Options

### Question A: Where do connection-scoped operations live?

#### Option A1: A stateless driver whose methods each take a connection

**Pros**

- One driver value per process, constructed once, with no per-connection state to manage.
- Compiling, declaring capabilities, and opening a connection all read from the same object.

**Cons**

- A connection produced by one driver can be passed to another and the program still compiles.
  The contract's own type system does not express the pairing it depends on.
- The history store has no connection, so it cannot be given one without either widening every
  one of its methods or reaching for a global.
- The connected server's version has no home, which is fatal to deriving lock impact from the
  server rather than from documentation.
- Every implementation begins by converting the contract's connection type back into its own,
  which is a check deferred from compile time to run time for no gain.

**Rejected because:** it makes an invalid pairing representable and leaves two required pieces of
state — the history store's connection and the server's version — with nowhere to live. Both
defects are in the shape, not in any implementation of it.

#### Option A2: Opening a connection returns a value carrying every operation that needs one

The driver answers only what can be answered with no database: its name, how to compile an
operation, and how to open a connection. Everything else — executing a statement, acquiring the
migration lock, introspecting, recording history, reporting the server version — belongs to the
connection.

**Pros**

- An operation on the wrong connection cannot be written, because the connection is the receiver.
- The server version and the history store's connection both have an obvious owner.
- A test double implements three methods rather than seven, which matters because the golden-file
  harness and the conformance suite will each need one.
- It is the shape Go's own database driver interface uses, so it will read as familiar.

**Cons**

- A connection carrying execution, locking, introspection, and history is not a small interface,
  which sits awkwardly beside a convention of keeping interfaces small.
- Two types to hold rather than one, in every caller.

#### Option A3: No driver value at all — a package-level open function per engine

**Pros**

- The smallest possible surface. Nothing exists until a connection does.

**Cons**

- A registry resolves a name to a *value*; a package-level function cannot be looked up by name
  without wrapping it in one, which is Option A2 with extra steps.
- Compiling an operation and declaring capabilities must both be answerable with no database, and
  neither has anywhere to live.

**Rejected because:** it removes the one thing the registry needs to hand back, and strands the two
operations that must work without a connection.

### Question B: How does a driver become reachable by name?

#### Option B1: Self-registration from an `init` function, panicking on a duplicate

The convention Go's standard library uses for database drivers and image formats: each driver
registers itself when its package is loaded, and a duplicate name is a panic.

**Pros**

- Established, immediately recognizable, and the blank import is a well-understood idiom.
- The entrypoint names each driver exactly once, in an import block.

**Cons**

- This project forbids `panic` in library code and enforces that mechanically. This would be its
  first exemption, added to the file that defines the central contract.
- An unrecovered panic terminates with an exit status this project has already assigned a
  different meaning. A wiring mistake would be indistinguishable from a failed validation to
  anything reading exit codes, and would print a stack dump rather than a sentence.
- It assumes registration is always a compile-time act. Once drivers can be supplied from outside
  the binary, a duplicate name becomes an ordinary runtime condition — and a panic is not a way to
  report one.

**Rejected because:** the contract is required to make a duplicate name an error, and this makes
it a crash whose exit status means something else.

#### Option B2: Self-registration from `init`, with the error recorded for later inspection

Registration still happens on package load; a duplicate is recorded, and the entrypoint checks for
recorded failures before doing anything.

**Pros**

- Keeps the blank-import idiom exactly as it is.
- A duplicate is genuinely an error rather than a crash.

**Cons**

- The error is returned to a caller that structurally cannot act on it — an `init` function has
  nowhere to return anything.
- It depends on every entrypoint remembering to ask. A program that forgets runs against a
  registry it never validated, and nothing marks the omission.
- Registration is global, so tests need a way to reset it and cannot run independently otherwise.

**Rejected because:** an error whose only recipient cannot handle it is a convention dressed as a
contract, and the safety of the whole scheme rests on a check nothing enforces.

#### Option B3: Explicit registration at the entrypoint

Each driver exports a constructor. The entrypoint builds a registry, registers each driver by name,
and handles the error.

**Pros**

- The error is returned where a caller exists to act on it, which is what the requirement asks for.
- The registry is an ordinary value with no global state, so two of them share nothing and tests
  need no reset mechanism.
- The conformance suite can be handed a registry rather than reading whatever the process happens
  to have accumulated.
- It extends unchanged to drivers supplied from outside the binary, where registration is genuinely
  a runtime act.

**Cons**

- Gives up the blank-import idiom that Go users will expect.
- The entrypoint gains a wiring function that is edited once per driver — though it was already
  going to be edited once per driver.

### Question C: What does this contract define now, and what does it defer?

#### Option C1: Define all of it, with placeholder types where the real ones do not exist

**Pros**

- The contract can be reviewed as a finished whole rather than in pieces.
- Later work fills types in rather than widening the interface.

**Cons**

- An interface method that nothing can honestly implement invites an implementation that returns a
  plausible empty value, and a plausible empty value is exactly the failure this project's rules
  about honest refusals exist to prevent.
- One of the placeholders would sit in a core package, visible to anyone reading the code, for the
  better part of the roadmap, carrying a doc comment that cannot be true yet.

**Rejected because:** it trades a real risk — a stub that lies — for the convenience of reviewing
the contract in one sitting.

#### Option C2: Define all of it, with placeholders built so a dishonest implementation is impossible

The placeholder types get unexported fields and no constructor, so nothing outside their own
package can fabricate one.

**Pros**

- Keeps the contract complete while making the failure mode of C1 unrepresentable rather than
  merely discouraged.
- Follows a pattern already used elsewhere in this codebase to make invalid states impossible.

**Cons**

- Carries a type that does nothing through most of the roadmap, in exchange for saving a single
  line in each driver much later.

**Rejected on timing rather than merit.** The technique is sound and is used elsewhere here. It
simply is not worth its keep for a type whose real definition is many milestones away.

#### Option C3: Define what can be implemented honestly; record the rest as decided

The contract ships with the methods whose types exist. The three deferred method signatures are
written down here and in the package documentation, so the later work inherits a decision rather
than a blank page.

**Pros**

- Nothing in the tree is a stub that lies, and the first driver compiles honestly on its first day.
- The deferred signatures are still decided, so the value of settling them early is kept without
  the cost of typing them prematurely.

**Cons**

- The contract is not reviewable as a finished whole until the history store lands.
- Each deferred method is a small change to every driver that exists when it arrives — though for
  two of the three, that number is zero.

## Decision Outcome

**A2, B3, and C3.** The driver answers what needs no database; the connection carries everything
that does; drivers are registered explicitly by the entrypoint against a registry value; and the
methods whose types do not yet exist are recorded rather than stubbed.

What that commits to:

- **A connection means one pinned session, not a pool.** Session-scoped advisory locks belong to a
  specific backend, so acquiring on one pooled connection and releasing on another silently fails
  to release — which would violate the rule that locks are always released while appearing to
  honor it. A driver that pools must pin a connection for the lock's lifetime.
- **The release path must not inherit a cancelled context.** A deferred release that reuses the
  context which just failed will fail too, precisely when releasing matters most.
- **The history store never imports the driver package.** The store is bound to a connection by the
  driver, so its own methods stay connection-free. Stated now because the alternative is an import
  cycle that would only be discovered while writing it.
- **A compiled statement declares whether it can run inside a transaction.** Not every operation
  can — creating an index concurrently is the motivating case — and an executor written on the
  assumption that one migration is one transaction cannot be retrofitted cheaply. The declaration
  exists before the first executor is written, so the executor is written to group statements
  rather than to wrap them.
- **There is exactly one way to say an operation is unsupported.** The conformance suite asserts
  that every operation is either compilable or refused with a reason, and that has meaning only if
  a refusal is distinguishable from an ordinary failure.
- **A driver name has one spelling.** The configuration file deliberately does not validate driver
  names against a list, because that would require core to know which drivers exist. The registry
  is therefore the only place a name is ever checked, and it rejects a variant spelling rather than
  quietly accepting a second way to say the same thing.

## Consequences

### Positive

- Handing a connection to the wrong driver stops compiling, rather than being a rule people are
  asked to remember.
- The server version and the history store's connection both have an owner, which is what makes it
  possible to derive lock impact from the server rather than from documentation.
- A test double for the contract implements three methods, which keeps the golden-file harness and
  the conformance suite cheap to write.
- A registry is a value, so nothing in a test can leak into another test, and the conformance suite
  runs against a registry it was given rather than one it inherited.

### Negative

- **A compiled statement is a string of text.** Every current target engine speaks SQL, so this is
  the honest name today — but a document database's "statement" is a structured command, and that
  driver will have to encode into a string and decode on the other side, or this type will need a
  second representation.
- **The contract is incomplete until the history store lands**, so the driver package cannot be
  reviewed as a finished piece of work before then.
- **"Adding a driver requires no changes to core" is now "no changes to core, plus two lines in the
  entrypoint."** That is still true in the sense that matters, but it is not the stronger claim.
- **The connection interface is not small**, and will grow with each deferred method. It carries
  execution, locking, introspection, history, and version reporting because all five need the same
  session — but the reason it is cohesive is not the same as it being small.
- Giving up self-registration means giving up a widely recognized idiom, and contributors familiar
  with it will look for it first.

### Neutral

- Compiling is the only operation that touches no database and no clock. Naming that separately
  costs three lines and documents the boundary that dry-run output, golden files, and validation
  all depend on.
- A statement carries no parameters. No target engine parameterizes schema changes, so the field
  would be permanently empty for the statements this type exists to carry, and its presence would
  invite routing ordinary queries through it. A driver's own internal queries are not this type's
  concern.
- No sentinel for lock contention is defined yet. What contention means — how long to wait, how to
  recover a lock held by a dead process, what to tell the operator — is genuinely undecided, and a
  sentinel with no implementation would be the same kind of empty promise this record rejects
  elsewhere.

## Notes

Revisit the statement representation if a non-SQL engine is taken seriously. The change is
mechanical but touches every driver, so it is worth doing deliberately rather than discovering it
while writing the first one.

Revisit self-registration if drivers ever ship as separate binaries. At that point registration
genuinely happens at run time from user-supplied configuration, and the explicit form chosen here
is already the one that fits — but the entrypoint's wiring function would move behind a discovery
step.
