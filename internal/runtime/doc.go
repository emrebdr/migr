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
// Status: scaffolding. Implemented across M6 (see TASKS.md).
package runtime
