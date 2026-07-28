// Package capability models what a driver can do, along two independent axes.
//
// Support answers whether an operation can run at all: Native, Emulated with a named strategy, or
// Unsupported with a human-readable reason. A capability is never silently degraded — emulation is
// surfaced in migr plan output.
//
// Lock impact answers what the operation costs a live database: LockFree, BriefExclusive,
// LongExclusive, or TableRewrite. It is version-dependent and must be resolved from the connected
// server, never hard-coded; an undeterminable case reports the pessimistic verdict (CLAUDE.md
// invariant #13).
//
// This package is where engine-specific knowledge belongs. An `if driver == "mysql"` branch in the
// engine means this model is missing something and should be extended instead.
//
// Status: both axes complete (M2-T2). Every driver reports LockUnknown until M13-T1 classifies
// them. See docs/adr/0012-capability-resolution.md.
//
// # Resolution answers about an operation, not about a kind
//
// A Set is a map from operation kind to a Rule, and a Rule receives the whole operation. Two
// requirements pull against each other and this is what satisfies both.
//
// The verdict cannot come from a table keyed by name. MySQL 8's choice between INSTANT, INPLACE,
// and COPY depends on the column type and the specific alteration; PostgreSQL 11 made
// ADD COLUMN ... DEFAULT instant where 10 rewrote the table; SQLite gained native DROP COLUMN in
// 3.35 and rebuilds the table before that. Note that the last of those moves the *support* axis
// too, so neither axis can be answered from a static table.
//
// The set must nevertheless be enumerable, because the conformance suite walks the operation set
// and because published documentation carries a per-driver table. Keying by kind is what makes
// NewSet able to reject a driver that never classified an operation, naming the ones it missed.
//
// Fixed wraps a constant verdict, so a driver's declaration still reads as a table wherever the
// answer does not actually vary, and drops to a function only where it does.
//
// # Version-dependence is resolved once; payload-dependence on every call
//
// A driver detects the server version when it opens a connection and builds a Set already
// specialised to that server, so its rules close over the version. There is no version parameter
// anywhere, which means there is no way to hold a Set detached from the server it describes, and
// no way to classify against a server nobody is talking to.
//
// The consequence worth stating: Resolve takes no context, performs no I/O, and returns no error.
// That is what lets migr plan print an annotated plan per statement, and what lets the
// version-dependent cases be tested exhaustively with no database — two Sets built for two server
// versions, asked the same question, disagreeing.
//
// Because Conn.Capabilities cannot return an error, NewSet is called inside Open and a set that
// will not build fails the connection rather than surfacing later.
//
// There is deliberately no connection-free baseline set. One would have to answer the SQLite 3.35
// question before knowing the answer, which is the confidently-wrong verdict ADR 0004 rejected,
// committed one axis over.
//
// # Unknown is a label, not a rank
//
// LockUnknown ranks equal to TableRewrite for every threshold comparison and renders as "unknown"
// everywhere a verdict is shown. That is how invariant #13's "reports the pessimistic verdict" and
// M2-T2's "drivers may return unknown" coexist: the driver's confidence is displayed honestly, its
// severity is assumed to be the worst.
//
// Ranking it above TableRewrite instead would leave no ceiling that accepts it, and a safety gate
// with no opt-out is one people switch off. Ranking it below anything would fail open. Rendering it
// as a table rewrite would claim a measurement nobody took, and would hide M13-T1's own worklist.
//
// An unrecognized impact — the zero value, or a word from a newer peer — is worse than unknown and
// satisfies no ceiling at all. A declared unknown earns the table_rewrite opt-out because the driver
// made a statement; a bug earns nothing.
//
// LockUnknown is a verdict a driver may report and never a ceiling an operator may configure, which
// is why Ceilings and Impacts are separate. internal/config validates against Ceilings, so
// max_lock_impact: unknown is refused by the same code path as a typo.
//
// # Invalid verdicts are unrepresentable rather than merely invalid
//
// Verdict's fields are unexported and Native, Emulated, and Unsupported are the only ways to build
// one. An emulation with no named strategy and a refusal with no reason both degrade to a refusal
// that names the omission, because the conformance suite's "either compilable or refused with a
// reason — no third possibility" is only checkable if the third possibility cannot be written.
//
// Every path fails toward refusing. A Set with no rule for an operation, a Rule returning a verdict
// no constructor built, and the zero Set all resolve to a refusal naming the driver — never a zero
// value passed along for a caller to interpret. That matters because forbidigo bans panic in this
// package, so a nil map or a nil rule must be an answer rather than a crash.
//
// Marshaling a verdict no constructor built is an error, not an empty object. Without that, a
// zero Verdict would reach an SDK over host.capability.get as a document with no content and no
// complaint, since the fields it would render are unexported.
//
// # The declaration is authoritative
//
// A driver's Compile refuses an operation with driver.UnsupportedError; its Set refuses the same
// operation with a verdict. Those are two statements of one fact and they can disagree. The
// capability declaration is the source of truth, and M2-T4 is what proves Compile agrees with it —
// which means that suite needs a Conn, where M2-T3's golden harness needs only a Compiler.
//
// # Not built, and why
//
// Three further facts belong here and are recorded rather than stubbed, following what ADR 0011
// did with history.Store:
//
//	Facts.TransactionalDDL       M4-T9, and MySQL declares it false in M9-T3
//	Facts.Capture                M13-T16: whether this server permits log-based change capture
//	engine limits on a logical type (PG precision 1000, MySQL 65)   deferred from M1-T1
//
// All three are fields on a struct rather than methods on an interface, so adding any of them
// reopens no driver. That is ADR 0004's retrofit-cost argument applied to the model that argument
// created, and it is why Set is a struct.
//
// Capture is a connection-wide fact rather than a per-operation verdict: binlog_format and
// wal_level describe the server, not the operation being asked about. A server that will not
// permit it refuses shadow-table migration with a reason naming the missing setting or privilege,
// rather than silently falling back to triggers — see docs/adr/0014-online-change-capture.md.
//
// Type limits have no consumer before M10, no decided shape, and no gap today: a driver handed
// decimal(1200,2) refuses at Compile with a reason, which the conformance suite already accepts.
//
// # Import direction
//
// This package must never import internal/driver. The driver contract names capability.Set as a
// return type, so the dependency runs one way and the other direction is an import cycle — the same
// rule ADR 0011 recorded for internal/history. depguard does not catch it: it denies the
// internal/driver/ prefix, which leaves the contract package itself importable.
//
// internal/config imports this package for the four ceiling spellings, which it owned first and
// froze in its testdata. LockImpact deliberately implements no encoding.TextUnmarshaler: goccy
// honors one during decode, which runs before config's validator, so an unknown value would abort
// the parse instead of joining the accumulated report that names every problem at once.
package capability
