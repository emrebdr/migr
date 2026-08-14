// Package driver is the conformance suite every database driver must pass unmodified.
//
// A driver's whole conformance run is three lines, because the suite supplies the operations and
// offers no way to opt out of any of them:
//
//	conformance.Suite{Driver: pg, Conn: conn}.Run(t)
//
// Status: complete (M2-T4). No driver passes it yet — M3-T2 is the first to run it.
//
// # The declaration is authoritative, and this is what proves Compile agrees
//
// A driver says the same thing twice. Compile refuses an operation with driver.UnsupportedError;
// the capability set refuses it with a Verdict. Those are two statements of one fact and they can
// disagree, and the capability declaration wins, because it is what migr validate and migr plan
// read before anything touches a database.
//
// So the suite asserts the biconditional: Resolve(op).Allowed() is false exactly when
// errors.Is(compileErr, driver.ErrUnsupported), both carrying a reason. Each direction fails a
// different way. A driver that compiles what it declared unsupported tells an operator the change
// is impossible when it is not. A driver that refuses what it declared native passes validation
// and fails at apply time, which is the direction that reaches production.
//
// It takes both a Driver and a Conn because neither alone can be asked. Compile lives on the
// driver and needs no database; the capability set is built inside Open against a detected server
// version and lives on the session (ADR 0012). A Conn has no Compile method.
//
// # There are two third possibilities, not one
//
// "Either compilable or refused with a reason" is the rule, and an error that is not a refusal is
// the obvious way to break it. The other is quieter: a Compile returning no statements, or a
// statement carrying no SQL, reports success and changes nothing. Both fail here.
//
// Two further structural checks earn their place by failing open rather than closed:
//
// An allowed verdict carrying an unrecognized lock impact — the zero value included — satisfies no
// ceiling at all, so --require-safe and safety.max_lock_impact are both switched off for that one
// operation while appearing to be on. LockUnknown is a verdict a driver may report and passes;
// a value nobody recognizes is a bug and does not.
//
// A capability set whose Facts.Driver is not the driver under test describes a different engine,
// and every refusal it produces names that engine to the operator. The same check catches the zero
// capability.Set, which refuses all twenty operations while naming nobody — without it, a driver
// that never built its set would look like one that honestly declared everything unsupported.
//
// # A raw operation names the engine it targets
//
// After the walk, the suite compiles a raw operation addressed to another engine and requires both
// sides to refuse it. ir.Raw.Validate checks only that the driver and the SQL are non-blank, so
// nothing below a driver enforces this.
//
// It is also the first case where a Rule receiving the whole operation rather than its kind is
// load-bearing rather than merely available: raw resolves to native or to unsupported by payload,
// which a table keyed by kind cannot express.
//
// # What the read path owes, and why it is not here
//
// M6-T3 grew the contract by two methods -- Query, for a migration's own read,
// and Perform, for its own statement -- and neither is checked here. They
// cannot be: this suite is untagged so that every linter sees it (M2-T4), and a
// read needs a live server. A Conn that has none can only refuse, which proves
// nothing about what a real one returns.
//
// Two things hold them instead. driver.Rows.Validate refuses a result whose
// rows are not the shape its own columns describe, and internal/runtime/host
// calls it on every read, reporting -32603 and naming the driver -- so a driver
// with that bug is found at the first callback rather than never. And the
// read-only guarantee is each driver's own integration test, because the case
// that decides it is a data-modifying CTE against a real server (ADR 0032, B).
//
// # No exemptions, structurally
//
// Suite has two fields. There is no case list, no options, and no skip hook, so "no per-driver
// exemptions" is a shape rather than a rule someone has to remember. The operations come from
// drivertest.Operations, and coverage is checked from the kinds the walk actually asked about, so
// both a fixture that went short and a loop that skipped one fail: an operation nobody asks about
// can disagree with nothing.
//
// This suite reads no golden file and drivertest sees no capability set, so neither layer subsumes
// the other. Where they overlap — a refusal must name a reason, the right kind, and the right
// driver — they overlap deliberately, and their failure messages say different things.
//
// # It takes no registry
//
// An earlier note recorded that this suite would take a driver.Registry rather than read a global.
// It takes neither. Registry.New already enforces that a driver answers to the name it was
// registered under, tested where that check lives, so a registry parameter would prove nothing
// this suite does not; and depguard forbids a driver package from importing another driver, so a
// caller could only ever register itself and read it straight back.
//
// # No build tag
//
// The suite needs no database and no language toolchain, so it runs in make test and in CI like
// any other package. A //go:build conformance tag would have made it invisible to every linter
// but the comment checker, since golangci-lint is invoked with no --build-tags, and invisible to
// CI, which calls lint, test, and build. A skeleton nothing runs is worth less than no skeleton.
//
// A driver may still need one. Conformance needs an open Conn, and where that means a live server
// the driver's own conformance test belongs behind the integration tag; a driver that can build a
// session around a known server version runs it untagged.
//
// # Two notes for whoever adds a driver
//
// The package is named driver and imports internal/driver, which is legal because a package's own
// name is never an identifier in its own files. A caller needing both aliases one — conformance
// for this package reads best.
//
// This package must never declare an -update flag. Importing drivertest links the one it declares
// into this test binary, and two declarations of one flag name panic at init before any test runs.
package driver
