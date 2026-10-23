// Package runtime defines the language runtime contract — discover, spawn,
// invoke — and the name-to-adapter registry.
//
// A runtime adapter launches migrations written in a given language as a child
// process and speaks the protocol in the protocol subpackage. Go is not a
// privileged special case: it goes through the same pipe as every other
// language, so one code path keeps them all honest.
//
// # Import convention
//
// This package's name shadows the standard library's runtime. A file that needs
// both imports this one plain and aliases the standard library:
//
//	import (
//		stdruntime "runtime"
//
//		"github.com/emrebdr/migr/internal/runtime"
//	)
//
// M0-T3 enforces this with the importas linter rather than leaving it to habit.
//
// # What an adapter owns, and what it does not
//
// docs/protocol.md draws the line, and M6-T1 drew it deliberately. The wire --
// framing, methods, errors, cancellation, the handshake -- belongs to the
// protocol subpackage and is the same for every language. Finding a toolchain,
// resolving dependencies, building a runner, and choosing an argv belong to an
// adapter and are different for each.
//
// The one thing every adapter owes the protocol is what §9 of that document
// states from the runtime's side: three pipes, the project root as the working
// directory, no secret in the environment -- a runtime never connects to a
// database -- and a child that does not outlive the engine.
//
// # An adapter describes a command; this package starts it
//
// Runtime.Command returns a Command -- path, args, env, dir -- and not an
// exec.Cmd. Every guarantee below lives in a field an adapter could otherwise
// overwrite: Stdin decides whether the child can be talked to, SysProcAttr
// whether it can be ended, Cancel whether it can be left behind. None of the
// three is a language's business.
//
// # The session, and the one rule §2.5 makes structural
//
// Session is one child process and the conversation with it: spawn, handshake,
// one read loop over its stdout, the engine's own id sequence, and a pending
// table that holds only the engine's requests. An id is unique within its own
// direction, so a response nothing here is waiting on means the two sequences
// crossed -- it ends the session rather than being discarded, because that
// mismatch is silent otherwise.
//
// Callbacks are served inline and therefore one at a time. §2.5 lets a runtime
// pipeline several, and the host still answers them in turn: they all reach one
// driver.Conn, which is a pinned session rather than a pool.
//
// A Session is the engine.Source. Emit opens the host window, sends
// migration.execute in the mode the engine stated, and holds the runtime's own
// operation count to what the host recorded -- a disagreement means one side
// dropped a host.schema.apply (§4).
//
// # What outlives what
//
// The child is started in a process group of its own and every path out ends
// the group rather than the process: a clean shutdown, a failed handshake, a
// canceled request the runtime ignored, and the operator's Ctrl-C. So a runtime
// that forked a helper cannot leave one behind either.
//
// That group is also why cmd/migr handles signals. A terminal signals the
// foreground group, and a child in its own group never sees Ctrl-C, so without
// a handler an interrupted migr up would orphan exactly what this exists to
// prevent.
//
// One edge is not coverable from here and is stated rather than papered over:
// an engine killed outright runs no ladder. What covers it is the pipe -- the
// child's stdin reaches EOF, because the engine held the only other end -- so
// §9 and §11 require a runtime to exit when it reads one.
//
// # The two streams are not ordered against each other
//
// stdout is read by the session's own loop and stderr by a goroutine of its
// own, so a line a runtime writes to stderr has no ordering against a message
// it writes to stdout afterwards. Everything the engine does with stderr
// respects that: the tail is drained by settle before any exit is reported, and
// a caller who wants a stderr line to have arrived must wait for it rather than
// infer it from a response.
//
// The fixtures here are held to the same rule, because getting it wrong makes a
// test that passes on an idle machine and fails on a loaded one. A fake that
// wants to prove it observed something the engine sent says so through
// host.log, which the read loop serves before it delivers the response that
// releases the waiter.
//
// Status: the contract, the registry, and the session (M6-T5); the Go adapter
// (M6-T6); and, since M6-T10, internal/cli starts one for every command that
// runs or previews a migration in a language migr does not read itself.
package runtime
