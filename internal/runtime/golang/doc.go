// Package golang is the runtime adapter for migrations written in Go.
//
// Go migrations cannot be loaded into an already-built binary, so this adapter
// generates a small runner main inside the user's own module, builds it with
// go build, caches the result by content hash, and hands back the path. Starting
// it is not this package's: an adapter returns a Command description and
// internal/runtime.Session owns the pipes, the process group, and the kill
// (ADR 0034, B).
//
// The directory is named golang rather than go because go is a reserved keyword
// and cannot be a package name.
//
// # What is written, and where
//
// Everything lands in <module root>/.migr/go: the generated main.go, the built
// runner, and the stamp the runner was built from. A .gitignore holding "*" is
// written beside them the first time, and never over one somebody has edited, so
// migr's build products stay out of a commit without migr touching the user's own
// ignore file.
//
// A directory whose name begins with "." is invisible to the go tool's own
// patterns, so the generated runner cannot break the user's go build ./..., go vet
// ./..., or go test ./... . It is still buildable by explicit path, which is what
// this adapter does.
//
// # What the generated runner names
//
// Exactly two things, and neither is an identifier from the user's code: a blank
// import of their migrations package, and runner.Main from
// github.com/emrebdr/migr/sdk/go/runner. Each migration registers itself from an
// init, so adding one is a one-file change and the generated main never has to
// know a name the user chose. That package arrives in M6-T7; this adapter is what
// states the contract it owes.
//
// # What the stamp covers, and what it does not
//
// The digest is over the generated runner, every file in the migrations directory,
// go.mod and go.sum, the toolchain's own identity, and migr's version. A hit skips
// go build entirely.
//
// It does not cover a package the migrations import from elsewhere in the module.
// Editing one of those does not force a rebuild, and the runner keeps whatever
// version of it was compiled in. Deleting .migr/go is the way out. This is stated
// here, in ADR 0035, and in a test that asserts the hole -- so widening the stamp
// fails that test rather than quietly making these three paragraphs wrong.
//
// Implemented in M6-T6 (see TASKS.md).
package golang
