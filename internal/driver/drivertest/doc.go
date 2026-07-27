// Package drivertest is the golden-file harness every driver's compiler is held to.
//
// A driver's whole golden suite is four lines, because the harness supplies the operations:
//
//	drivertest.Suite{Driver: "postgres", Compiler: pg}.Run(t)
//
// Status: complete (M2-T3). No golden files exist yet — M3-T2 writes the first.
//
// # Everything here is denied to nothing, so nothing here may reach a driver
//
// depguard denies the internal/driver/ prefix everywhere but cmd/migr, which would put this
// package out of reach of internal/driver/postgres and test/conformance/driver — the only two
// places that need it. One allow entry lifts that, using the same longer-prefix-beats-shorter
// resolution that keeps internal/runtime/protocol importable.
//
// The entry is rule-global, so every package may import this one. That is safe only while this
// package imports no concrete driver, because doing so would reopen the transitive route from
// core to a driver that invariant #1 exists to close. It imports internal/driver, internal/ir,
// internal/schema, and the standard library, and must keep to that.
//
// # The suite walks every operation, and the driver does not get a say
//
// Run resolves its cases from ir.Kinds(), not from what the caller offers. "A missing golden file
// fails rather than silently passing" is empty otherwise: if a driver hands over the operations
// it chose, an omitted one is not missing, it simply never existed. Walking the operation set is
// what turns an omission into a failure, and it is the same rule M2-T4 states as no per-driver
// exemptions.
//
// # The format is written, never parsed
//
// A golden file records the statements under numbered headers:
//
//	-- migr:statement 1
//	ALTER TABLE "users" ADD COLUMN "email" varchar(255) NOT NULL;
//
//	-- migr:statement 2 no-transaction
//	CREATE INDEX CONCURRENTLY "idx_users_email" ON "users" ("email");
//
// The headers are not decoration. Statement.String returns only the SQL, so a driver that moves a
// statement outside its transaction would otherwise produce a byte-identical file — a change to
// what actually runs, with a green test. And separating statements by blank lines alone cannot
// tell two statements from one containing a blank line, which is a real difference once M3-T5
// groups statements around the transaction boundary; create_view and raw both carry multi-line
// SQL, so that case arrives with the ordinary fixtures rather than as a corner.
//
// Nothing ever reads a golden file back. The harness renders and compares bytes, so a raw
// operation whose SQL contains something that looks like a header is untidy but cannot produce a
// wrong answer. Comparison is byte-exact for the same reason trimming would be convenient: an
// editor that rewrites trailing whitespace must show up as a failure rather than as agreement.
//
// # A refusal is a golden file too
//
// An operation a driver declares Unsupported records the refusal rather than being skipped:
//
//	-- migr:unsupported drop_column: requires a full table rebuild before 3.35
//
// So make golden-update runs across the full operation set with no per-driver skip list, and an
// operation that starts or stops being refused arrives as a diff instead of an edited test. A
// skip list would also be the per-driver exemption M2-T4 forbids by name.
//
// Only a refusal that names itself properly is recorded. driver.Unsupported validates none of its
// arguments, so the harness checks that the reason is not blank, that the operation refused is the
// one that was asked about, and that the driver named is the one under test.
//
// Anything else a compiler returns is a bug, not a verdict, and fails without writing a file —
// including a bare ErrUnsupported with no reason, and compiling to no statements at all.
//
// # Two properties a driver cannot see in its own output
//
// Every operation compiles ten times and every run must agree. A Compile that ranges a map emits
// different SQL between runs, and a two-run check finds that only sometimes; invariant #8 exists
// because golden files depend on it.
//
// A golden file no operation compiles to is reported. Renaming or removing an operation leaves
// its expectations behind, where they read as coverage that no longer exists, and -update rewrites
// what is claimed without ever deleting.
//
// # Where the operation set lives
//
// Operations is the representative operation per kind, which M2-T4's conformance walk and M14-T5's
// published tables also need. It cannot move to internal/capability, which has the same need:
// driver imports capability, so capability importing this package is an import cycle. If a third
// consumer appears, the set belongs in a package under internal/ir that neither imports.
//
// The raw and create_view fixtures carry SQL because those operations are defined to. That is IR
// payload rather than the framework emitting SQL, so invariant #3 is intact — and raw's fixture is
// a comment rather than a statement, so no golden file generated here can be mistaken for real
// DDL. raw also names the engine it targets, so the fixture takes the driver name.
//
// # -update
//
// This package declares the -update flag, and it is the only place in the repository that may
// declare it with that name: two declarations in one binary panic at init, before any test runs.
// internal/driver's test package declares a matching one it ignores, because make golden-update
// sweeps ./internal/driver/... and that binary would otherwise reject a flag it has no golden
// files to regenerate. It must therefore never import this package, even from an external test
// package.
//
// Only test binaries link this package, and the flag is inert at false, so registering it costs
// the migr binary nothing — cmd/migr speaks pflag through cobra and never parses flag.CommandLine.
package drivertest
