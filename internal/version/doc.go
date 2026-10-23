// Package version reports the running binary's version, commit, and platform.
//
// This is a leaf package with no migr dependencies, deliberately. The migration
// history store records the framework version alongside every applied migration
// (M3-T6), and internal/history is core -- it cannot import internal/cli without
// violating CLAUDE.md invariant #1. Both sides depend on this package instead.
//
// Version is overridden at build time via -ldflags; commit and dirty state come
// from the go tool's VCS stamping, so an ordinary `go build` still reports them.
// A release build sets it to the tag, v-prefixed, through .goreleaser.yaml's
// ldflags (M14-T1, ADR 0098) -- the same form `make build` stamps.
//
// An untagged build stamps Version from `git describe --always`, which is the
// short commit itself, so Info.String omits the parenthesised commit when the
// version already begins with it rather than printing the same hash twice.
package version
