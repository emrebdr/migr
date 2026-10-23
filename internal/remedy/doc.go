// Package remedy carries what a person should do next about an error, beside the error itself.
//
// Every user-facing failure names what failed, why, and what to do next (M14-T7, invariant #9). The
// first two belong in the message. The third is kept apart, because the command line renders it as
// a line of its own under the message and --json carries it as a field a script can show without
// parsing prose -- and because a remedy kept apart can be asserted present for every ending in
// internal/cli's exit-code matrix, where one written into a sentence cannot.
//
// A remedy is attached where something knows it: With wraps any error, and an error type that knows
// its own implements Next. Of reads the outermost one in a chain, so a command that knows its own
// flags can restate one a deeper layer attached in more general terms. Fallback is the other
// direction: a caller that knows less than the layer that failed attaches a remedy only where that
// layer said nothing, so it never hides the more precise one.
//
// This is a leaf package with no migr dependencies, deliberately, so that core, drivers and runtimes
// can all attach one and only internal/cli reads it. It is the same shape internal/version has for
// the same reason.
//
// A remedy is an imperative a person can act on: it names a command, a flag, a migr.yaml key or a
// file. "Check your configuration" is not one.
package remedy
