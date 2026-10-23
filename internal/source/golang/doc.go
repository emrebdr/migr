// Package golang is the .go migration format, as far as the CLI is concerned.
//
// It holds the extension and the template `migr create --lang go` writes, and
// nothing else. Where internal/source/sql also holds a parser, this package has
// none and never will: a Go migration is read by the Go toolchain, compiled into
// a runner, and executed as a process. What comes back is IR over the
// wire rather than a file this package could parse.
//
// It lives here rather than beside the adapter in internal/runtime/golang for one
// mechanical reason: internal/cli must import it, and depguard forbids internal/cli
// from importing anything under internal/runtime/ (invariant #1). The template is
// the one part of the Go format the CLI legitimately owns, so it lives under the
// prefix internal/cli is carved out of.
package golang
