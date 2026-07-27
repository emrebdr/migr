// Package driver defines the database driver contract and the name-to-constructor registry.
//
// Adding a database must not require changing anything outside its own package. Two rules
// govern this subtree: drivers never import one another — shared logic moves up into this
// package — and raw SQL appears nowhere else in the codebase (CLAUDE.md invariants #2 and #3).
//
// Status: contract and registry complete (M2-T1); capabilities added (M2-T2). See
// docs/adr/0011-driver-contract.md and docs/adr/0012-capability-resolution.md.
//
// # The split: a Driver answers what needs no database
//
// Name and Compile need nothing. Everything that needs a session belongs to the Conn that Open
// returns, and that is the whole of the design.
//
// An earlier sketch put Introspect, Lock, and History on Driver with the connection as a
// parameter. That shape made pgDriver.Introspect(ctx, mysqlConn) a type-correct program, left
// History() with no connection to bind to, and gave the connected server's version — which
// invariant #13 requires in order to derive lock impact from the server rather than from
// documentation — nowhere to live. Making the connection the receiver removes all three.
//
// Compile is deliberately pure: no context, no connection, no clock. That is what lets migr plan
// print statements without touching a database, what lets golden tests run with no engine
// installed, and what the Compiler interface exists to name.
//
// # Capabilities belong to the session
//
// Capabilities returns what the connected server can do, on both axes, already resolved against
// its detected version. It sits here rather than on Driver because invariant #13 requires a lock
// verdict derived from the server rather than from documentation, and a driver holding no
// connection cannot know which server it will be asked about.
//
// A driver builds its set inside Open, after detecting the version, so the method takes no context
// and returns no error. A set that will not build fails the connection instead. What a driver
// declares is authoritative: Compile must refuse exactly the operations the set refuses, and M2-T4
// is what proves the two agree. See internal/capability and
// docs/adr/0012-capability-resolution.md.
//
// # Deferred, but decided
//
// Three methods belong on Conn and are not written yet, because their types do not exist. They
// are recorded here so the tasks that add them inherit a decision rather than a blank page:
//
//	History() history.Store                       M2-T5
//	Introspect(ctx) (*schema.Snapshot, error)     M11-T1
//	ServerVersion() string                        M3-T1
//
// ServerVersion and capability.Set.Server report the same fact, so a driver must derive both from
// one detection inside Open or they can disagree.
//
// They are not stubbed. A method whose only possible implementation returns a plausible empty
// value is worse than a missing one, and the conformance suite's "no third possibility" rule
// exists to forbid exactly that.
//
// One rule follows for M2-T5: internal/history must never import internal/driver. The store is
// bound to a connection by the driver, so its own methods stay connection-free. The alternative
// is an import cycle, discovered while writing it.
//
// # A Conn is one pinned session, not a pool
//
// PostgreSQL session advisory locks belong to a backend. Acquiring on one pooled connection and
// releasing on another silently fails to release — satisfying invariant #11's deferred-release
// requirement while breaking the property that requirement exists to guarantee. A driver that
// pools must pin a connection for the lock's lifetime.
//
// Unlock is safe to call more than once, so an early explicit release and a deferred one do not
// double-release. It returns an error because a failed release leaves the lock held until the
// session ends.
//
// The release path must not inherit a canceled context, or it fails exactly when it matters
// most — the context that caused the failure is already canceled:
//
//	defer func() { err = errors.Join(err, unlock(context.WithoutCancel(ctx))) }()
//
// # Statements
//
// A Statement is the text and one flag: whether it can run inside a transaction. Not every
// operation can — CREATE INDEX CONCURRENTLY is the motivating case — and an executor written on
// the assumption that one migration is one transaction cannot be retrofitted cheaply. The flag
// exists before the first executor does, so M3-T5 groups statements rather than wrapping them.
// It is spelled so that the zero value is the safe answer.
//
// There is no Args field. No target engine parameterizes schema changes, so it would be
// permanently empty for the statements this type carries, and its presence would invite drivers
// to route ordinary queries through it. A driver's own internal queries are its business.
//
// # Refusing an operation
//
// UnsupportedError is the one way a driver says it cannot perform an operation. M2-T4 asserts
// every operation is either compilable or refused with a reason — no third possibility — and
// that is checkable only because a refusal is distinguishable from a compiler bug through
// errors.Is(err, ErrUnsupported).
//
// # Registration is explicit
//
// cmd/migr builds a Registry and calls Register, which returns an error on a duplicate name.
// There is no init() and no blank import, because neither can report that error, and panicking
// instead terminates with status 2 — which this project has already assigned to validation
// failure, making a wiring bug indistinguishable from a bad migration to anything reading exit
// codes.
//
// A Registry is a value, so two of them share no state. Tests need no reset hook, and the
// conformance suite runs against a registry it was handed rather than whatever the process
// accumulated.
//
// A driver name has one spelling. migr.yaml deliberately does not validate driver names against
// a list — that would require core to know which drivers exist — so this registry is the only
// place a name is ever checked, and it rejects a variant rather than quietly accepting a second
// way to say the same thing.
//
// # A note for M2-T3
//
// depguard denies the internal/driver/ prefix everywhere except cmd/migr, so a shared
// internal/driver/drivertest helper package would be denied to internal/driver/postgres and to
// test/conformance/driver. The fix is one allow entry, using the same longer-prefix-beats-shorter
// mechanism that already keeps internal/runtime/protocol importable.
package driver
