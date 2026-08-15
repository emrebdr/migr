# 0034. The runtime process, and what outlives what

## Status

Accepted

## Date

2026-08-15

## Context

M6-T1 specified the session, the timeouts, and what a runtime process owes ([§3](../protocol.md),
§8, §9). M6-T2 built the codec and stopped at framing. M6-T3 built the callback server and recorded
in [ADR 0032](0032-host-callback-server.md), D1, that *the loop that decides which messages reach
`Serve` is M6-T5's*. M6-T4 left the `engine.Mode` → `protocol.Mode` conversion with no home.

Nothing in this repository had ever started a process — `grep os/exec` found nothing outside the Go
toolchain's own use. So this task introduces process management, and §9's guarantee rests on it:

> **The engine owes the process one thing:** it does not outlive the engine. An orphaned runtime
> holding a database connection through `host.*` callbacks that nothing will answer is a leak with a
> lock behind it.

`internal/runtime` was `doc.go` and nothing else, despite CLAUDE.md §3 naming both a `Runtime`
interface and a registry.

**The decision that is not in the specification:** putting the child in a process group of its own
is what makes the kill reliable, and it is also what breaks `Ctrl-C`. A terminal signals the
foreground process *group*; a child in its own group never sees SIGINT. `cmd/migr` passed
`context.Background()` and handled no signals, so the moment the group existed, an interrupted
`migr up` would orphan the runtime — the exact failure §9 forbids, in the most common case there is.

## Decision Drivers

- The done-when: *"spawn, handshake, graceful shutdown, kill-on-timeout, and stderr passthrough all
  work; an orphaned child process cannot survive engine exit."*
- §2.5: *"an implementation that keeps one table of pending requests for both directions is wrong
  and will mismatch responses."*
- §8.2: `migration.execute` has **no** timeout, because a backfill legitimately runs for hours.
- ADR 0031, A: `protocol.Reader` is spent after a framing failure and reports the same error
  forever. The loop's job on one is to write `protocol.Refuse(err)` and end the session.

## Considered Options

### A. Where the session lives

- **A1: `internal/runtime`, the parent package** ✔, holding the adapter contract, the registry, and
  the session — the same shape `internal/driver` already has. depguard gains one allow entry for
  `internal/runtime/host`, on the criterion `internal/runtime/protocol` already meets: it serves the
  six callbacks §5 specifies, is identical for every language, and names no adapter.
- A2: the loop inside `internal/runtime/host`. **Rejected**: ADR 0032 D1 decided the loop is not
  host's, and the contract and registry belong in the parent regardless — so the split would be two
  packages either way, with the awkward half in the one whose `doc.go` is about callbacks.
- A3: invert the dependency behind a `Serve(ctx, *Message) *Message` interface, so nothing imports
  `host`. **Rejected**: whoever constructs the host server needs a `driver.Conn` and the allow entry
  anyway; `internal/cli` will be the second such caller at M6-T10. An interface that exists only to
  avoid a one-line lint entry is a layer with no reader.

### B. What an adapter hands back

- **B1: a `Command` description — path, args, env, dir** ✔. The session sets the pipes, the process
  group, the cancel function, and the wait delay.
- B2: an `*exec.Cmd`. **Rejected**: every §9 guarantee lives in a field an adapter could then
  overwrite. `Stdin`, `SysProcAttr`, and `Cancel` are exactly the three that decide whether the
  child can be talked to, ended, or left behind, and none of them is a language's business. §9
  already draws the line from the other side: *"how it is found and started is the adapter's"*.

### C. How callbacks are served

- **C1: inline in the read loop, and therefore serialized** ✔. §2.5 lets a runtime pipeline several
  `host.*` requests and the host answers them one at a time.
- C2: a goroutine per callback. **Rejected**: they all reach one `driver.Conn`, which ADR 0011 made
  a pinned session rather than a pool, and pgx is not safe for concurrent use. Serializing is not a
  simplification here — it is the contract. Nothing deadlocks, because serving never requires
  reading.

### D. What ends the process

- **D1: its own process group, ended on every path, plus a signal handler in `cmd/migr`** ✔. The
  close ladder is `shutdown` → grace → SIGTERM → grace → SIGKILL, and each rung ends the group. The
  first interrupt cancels the run — which sends `$/cancel`, waits the cancel grace, and kills — and
  the second restores the default disposition.
- D2: leave the child in the engine's group and handle no signals. **Rejected**: the terminal would
  kill both, but a wrapper, a CI runner, or a supervisor that kills only `migr` still orphans the
  child; and a backfill would take SIGKILL mid-chunk rather than a chance to stop between them,
  which is the whole point of §8's courtesy.
- D3: `Pdeathsig`. **Rejected**: Linux only, and this project builds on macOS too.

There is one edge none of these covers, and it is stated rather than papered over: an engine that is
itself killed outright runs no ladder. What covers it is the pipe — the child's stdin reaches end of
file, because the engine held the only other end — so §9 and §11 now say plainly that a runtime
which reads EOF on stdin has lost its engine and must exit.

### E. What stderr becomes

- **E1: one log record per line, on the engine's own logger, plus a bounded ring of the last 20** ✔.
- E2: raw passthrough to the engine's stderr. **Rejected**: `--json` promises that stderr is a
  stream of JSON objects (CLAUDE.md §5), and a Node stack trace written raw onto that stream breaks
  the one command an operator pipes into `jq`. The text is still verbatim; it is the *framing* that
  changes.

The ring is why stderr is buffered at all (§9): a non-zero exit reports the code together with the
last lines, which are the ones that say why.

### F. What an unmatched response means

- **F1: it ends the session** ✔. A response to an id this side never issued means the two sequences
  crossed, and §2.5 exists because that mismatch is otherwise silent.
- F2: log it and carry on. **Rejected**: the task named this hazard from the other direction — *"a
  response handed to `Serve` is answered with nothing, so a routing bug is silent"* — and discarding
  an unmatched response is the same silence one layer up.

## Decision Outcome

`internal/runtime` gains the `Runtime` adapter contract, a registry mirroring `internal/driver`'s,
and `Session`: spawn, handshake, one read loop over the child's stdout, a per-direction pending
table, `$/cancel` with a grace, the close ladder, and the stderr tail.

`*Session` **is** the `engine.Source` — `Emit` opens the host window, sends `migration.execute` in
the stated mode, and returns what the host recorded, holding the runtime's own count to it (§4).
§7.4's two refusals come back as `engine.ErrNoDownAuthored` and `engine.DeclaredIrreversible` with
the author's reason.

`migr.yaml` gains a `runtime:` block for §8.2's four bounds, defaulted from `protocol.Default*` —
the same move `applyDefaults` already makes with `driver.DefaultLockWait`, because §8.2 says these
defaults are the protocol's. `migration.execute` has no key, so there is nothing for an operator to
set to the value that would break it.

## Consequences

### Positive

- The orphan guarantee is tested against a real process, including a runtime that forked a
  grandchild: both are gone after `Close`, and the test proves it by signalling the pids.
- A runtime that ignores `shutdown`, ignores `$/cancel`, never answers the handshake, picks a
  version the engine never offered, writes an unframeable line, or answers a request nobody sent is
  each ended rather than waited on, and each has a test.
- `migration.execute` is bounded by the caller's context and by nothing else, which one test proves
  by holding a request open past every other bound in the table.
- A non-zero exit reports the status and the last 20 lines of stderr, so a runtime that dies during
  startup says why instead of leaving a closed pipe.

### Negative

- **The process group is unix-only.** A `_windows.go` fallback signals the process alone, so on that
  platform a runtime that forked can leave a child behind. CI runs Linux and macOS; nothing here is
  tested on Windows, and the file says so rather than pretending otherwise.
- **Callbacks are serialized**, so an SDK that pipelines them gains nothing. That is a consequence
  of one pinned connection, not of this loop, and it is the same answer M6-T3 gave.
- **`Session.Close` must be called** for the ladder to run. `exec.CommandContext` with a lifetime
  context and a group-killing `Cancel` is the backstop when it is not, but a caller that leaks the
  session leaks the process until its own exit.

### Neutral

- The two mode types now have a converter, `wire`, and `TestTheWireSpellsModeAsTheEngineDoes` (M6-T4)
  is what keeps the two sets level. That was the seam M6-T4 could not exercise.
- Nothing constructs a `Session` outside tests yet. `internal/cli`'s `sourceFor` still refuses Go by
  name, pointing at M6-T10, and `cmd/migr` registers no adapter because there is none to register.

## Notes

M6-T6 writes the first adapter: a generated runner `main`, built with `go build` and cached by
content hash, returned as a `Command`. It is also where `cmd/migr` gains a runtime registry, and
where `Call.Statements()` — the escape-hatch statements a preview held rather than ran — finally
has a producer worth rendering (ADR 0033).

M7-T6 does the same for Node, and owes nothing to this package beyond `Discover` and `Command`.
