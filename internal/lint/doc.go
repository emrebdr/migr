// Package lint enforces the two comment rules that no off-the-shelf linter expresses:
// no comment may appear inside a function body, and a doc comment must be one line.
//
// The reasoning is in CLAUDE.md §9. In short: a block that needs a comment to be
// understood should be a named function instead, because the name is visible at every
// call site and a name that stops fitting the code is obvious, where a stale comment is
// invisible. Longer prose belongs in a package doc or in docs/ — places read
// deliberately rather than skimmed past on the way to the code.
//
// # Owner
//
// M0-T10. Wired into `make lint` ahead of golangci-lint, so the cheap rule fails first.
//
// # Why a filesystem walk rather than a go/analysis pass
//
// The check walks directories and parses files instead of loading packages, which buys
// two properties a go vet plugin would not have.
//
// It crosses module boundaries. This is the inverse of the trap the Makefile is built
// around: `go build ./...` silently skips the nested sdk/go module, but a walk does not
// know modules exist, so one invocation from the repository root covers both.
//
// It ignores build tags. Files behind //go:build integration, conformance, or
// zerodowntime are invisible to a default golangci-lint run, and those tags arrive in
// M2, M3, and M13. A walk checks them from the day they are written.
//
// It also keeps the root module's dependency count where it is: go/ast, go/parser, and
// go/token are standard library.
//
// # Exemptions
//
// Comments beginning //go:, //nolint:, or // Deprecated: are tooling instructions rather
// than commentary, and go doc and staticcheck both parse the last two. They are exempt
// from the in-body rule, and they do not count toward a doc comment's length. A
// // Deprecated: marker additionally ends the count, so a wrapped deprecation note is
// allowed.
//
// The //nolint: exemption is guarded by golangci-lint's nolintlint, which requires every
// directive to name specific linters and carry an explanation. Without that guard, a bare
// //nolint would smuggle arbitrary commentary past this check.
//
// Package doc comments are never checked. They are exactly where multi-line context
// belongs, and this one is an example of that.
package lint
