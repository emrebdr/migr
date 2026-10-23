// Package runner is the entrypoint the migr engine generates a call to.
//
// The Go runtime adapter writes a small main into the user's module, builds it,
// and executes it. That generated file names exactly two things: a
// blank import of the user's migrations package, and runner.Main. This package
// is the second of them, and it exists so that symbol stays stable no matter
// where the session is implemented.
//
// A project that would rather write its own main can skip the generated one and
// call migr.Run itself, which is the whole of what Main does around os.Exit.
package runner
