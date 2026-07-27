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
// Status: scaffolding. Implemented across M6 (see TASKS.md).
package runtime
