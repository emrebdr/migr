// Package plan turns Operation IR into an ordered, compiled execution plan.
//
// Planning is deterministic: no time.Now, no randomness, no map-iteration-order dependence, so
// golden tests stay stable (CLAUDE.md invariant #8). Nothing here opens a database, and that is a
// property rather than a convenience — migr plan must print statements without touching one
// (M5-T4), and CI catching a migration that will not compile is what makes the executor's lazy
// operation seam survivable (ADR 0021).
//
// Status: compilation complete (M4-T5), and compiling a rollback's inverses in M4-T6 — a Plan
// carries the direction it was compiled in, which is what a history row records rather than a
// parameter traveling beside it. Decomposing unsafe operations into safe sequences is
// M13-T5, and annotating a plan with lock verdicts is M13-T6.
//
// # A step carries the operation and the statements together
//
// Not statements alone: invariant #9 requires every user-facing error to name the offending
// operation, and a flat statement list cannot answer which operation statement seven came from.
// engine.StatementError reports a position; Plan.At turns that position back into the operation,
// and that is the whole reason a step is a pair rather than a slice.
//
// Not operations alone: the statements have to exist somewhere a database is not, and this is that
// place.
//
// It is also what M13-T5 needs. "An operator must be able to see that one declared operation became
// three statements" is unprintable from a flattened list, and a rewrite that replaces the operation
// in a step is a smaller change than one that has to reconstruct which statements came from what.
//
// # Compile holds a driver to statements that would run
//
// An operation that compiles to no statements, or to a statement with no SQL, reports success and
// changes nothing. The driver conformance suite fails that (M2-T4), and it is checked again here
// because the suite walks a driver's whole operation set once while this walks the operations one
// migration actually emitted — a live compiler bug reaching this package would otherwise be an
// operation that silently never ran.
//
// Operations are validated before they are compiled, so a malformed one is a plan that will not
// build rather than SQL a server rejects. Nothing here is engine-specific: refusing an operation is
// the driver's answer, delivered through driver.ErrUnsupported, and this package only reports it.
//
// # A Planner is a value with a method
//
// The shape matches drivertest.Suite and the driver conformance suite: configuration is fields, and
// the entry point takes only what varies per call. M13-T5 adds rewrites and M13-T6 adds a
// capability set as fields, so neither changes a signature and neither can be passed at one call
// site and forgotten at another.
//
// Capability verdicts are deliberately not here yet. Compile lives on driver.Compiler and needs no
// session; a capability.Set is built inside Open and needs one. A Verdict field would be zero at
// every call site today, which is the stub shape ADR 0011 and the conformance suite both reject.
package plan
