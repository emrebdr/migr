// Package golang is the runtime adapter for migrations written in Go.
//
// Go migrations cannot be dynamically loaded into an already-built binary, so
// this adapter generates a small runner main in the user's module, builds it
// with go build, caches the result by content hash, and executes it as a child
// process speaking the standard protocol.
//
// The directory is named golang rather than go because go is a reserved
// keyword and cannot be a package name.
//
// Status: scaffolding. Implemented in M6-T6 (see TASKS.md).
package golang
