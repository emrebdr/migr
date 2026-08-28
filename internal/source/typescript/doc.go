// Package typescript is the .ts migration format, as far as the CLI is concerned.
//
// It holds the extension and the template `migr create --lang ts` writes, and nothing
// else. Where internal/source/sql also holds a parser, this package has none and never
// will, for the reason internal/source/golang has none: a TypeScript migration is read
// by Node, and what comes back is IR over the wire rather than a file to parse.
//
// It lives here rather than beside the adapter in internal/runtime/node for one
// mechanical reason: internal/cli must import it, and depguard forbids internal/cli
// from importing anything under internal/runtime/ (invariant #1). The template is the
// one part of the TypeScript format the CLI legitimately owns, so it lives under the
// prefix internal/cli is carved out of.
//
// The template is written against the contract M7-T2 landed and nothing beyond it. The
// schema builder is M7-T3 and the runtime that executes the file is M7-T5, so a
// migration created today typechecks, states both its directions, and cannot yet run.
package typescript
