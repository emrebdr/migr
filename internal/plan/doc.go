// Package plan turns Operation IR into an ordered, compiled execution plan.
//
// Planning is deterministic: no time.Now, no randomness, no map-iteration-order dependence, so
// golden tests stay stable (CLAUDE.md invariant #8). Compiling opens no database, and that is a
// property rather than a convenience — migr plan must print statements without touching one
// (M5-T4), and CI catching a migration that will not compile is what makes the executor's lazy
// operation seam survivable (ADR 0021).
//
// Status: compilation complete (M4-T5), and compiling a rollback's inverses in M4-T6 — a Plan
// carries the direction it was compiled in, which is what a history row records rather than a
// parameter traveling beside it. Decomposing unsafe operations into safe sequences landed in
// M13-T5 (ADR 0085), and every step carries its lock verdict since M13-T6 (ADR 0086).
//
// # A rewrite replaces a step's statements and keeps its operation
//
// Planner.Rewriter is the session and Planner.Rewrites the names safety.rewrites leaves on. Compile
// asks the session about each operation it compiled. Where one answers and its name is on, the
// step's statements become the safer sequence and Step.Rewrite records which rewrite it was, why,
// and how many statements the operation was declared as. So "one declared operation became three
// statements" is printable.
//
// An operation on a table this migration created is never rewritten, and a rename of such a table
// is followed. Nothing can be waiting on a table that did not exist when the migration began, and a
// rewrite's bare statements would split the migration for nothing but lost atomicity.
//
// # A deferred step, and what Compile stopped doing
//
// An operation a driver performs only against a live database answers driver.ErrNeedsSession
// (ADR 0053). M8-T4 had Compile call the session there and then; M11-T7 made it mark the step
// Deferred instead, because an expansion is rendered from the catalog and the operations ahead of it
// have not run yet — measured on SQLite, a rebuild compiled before the alter_column before it ran
// re-rendered the table without the NOT NULL that alter had just written (ADR 0071).
//
// Expand is what fills a deferred step in, and the caller is what knows when: internal/engine calls
// it immediately before the step's statements execute, inside the transaction the migration is. An
// expansion goes through the same checkCompiled a compilation does, since a session that reports
// success while producing nothing is no different from a compiler that does.
//
// So Compile and Step take no context, and Expand is the one method here that is honest about
// needing a database. A planner with no Expander still returns the sentinel rather than deferring,
// which is what migr validate reads to report the operation unchecked (ADR 0040): deferring there
// would hand it a step nothing could ever fill in.
//
// Plan.Len is therefore the count of statements compiled so far, not the count a run will send, and
// Plan.Deferred is how many operations are still to be compiled. Both callers of the pair say which
// they mean: a dirty row renders a partial total as one, and migr plan qualifies its own count.
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
// the entry point takes only what varies per call. M13-T5 added rewrites and M13-T6 a capability
// set as fields, so neither changed a signature and neither can be passed at one call site and
// forgotten at another.
//
// # Every step carries what it costs
//
// Planner.Capabilities is the session's declaration, and Compile resolves each operation against it
// into Step.Verdict -- nil leaves the verdict zero, which is what migr validate offline compiles
// with. Step.Lock is the one answer a gate and a plan read (ADR 0086): nothing for an operation on a
// table this migration creates, since nothing can be waiting on it; else the rewrite's cost where
// the step was rewritten; else the verdict's. The verdict itself stays the declaration, so a plan
// can say what a rewrite saved, and an expansion keeps it. Assessing a verdict against the table is
// I/O, so it is the preview's (internal/engine) and never Compile's.
package plan
