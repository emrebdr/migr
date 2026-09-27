// Package checksum hashes migrations so a modified one can be caught.
//
// Applied migrations are immutable and a mismatch is a hard error that no flag repairs
// (CLAUDE.md invariant #6), which makes a false positive as expensive as a missed one.
//
// Status: complete (M1-T6), with verification and drift reporting wired up in M4-T7.
//
// # The migr1 scheme
//
// A checksum is a scheme tag and two SHA-256 digests:
//
//	migr1:<64 hex of the normalized source>:<64 hex of the emitted IR>
//
// The source is the migration file's raw bytes with every \r\n and every lone \r rewritten
// to \n, and nothing else changed. The IR is the bare operations array, ir.Marshal of
// Recording.Operations(). Both digests are lowercase hex, and the whole value is 135 ASCII
// characters. That is the entire specification; docs/adr/0009-migration-checksum.md records
// why it is that and not something else.
//
// # Two halves, two jobs
//
// They are kept separable because they answer different questions, and only one of them can
// answer it offline.
//
// The source digest decides drift. Recomputing it needs the file and nothing else, so migr
// status can resolve every migration without a language toolchain or a database.
//
// The IR digest is provenance: what one run actually emitted. It cannot decide drift, because
// migrations may branch on live query results, so re-planning against a changed database can
// legitimately produce different operations from the apply that was recorded. A mismatch there
// has three possible causes — the file changed, the database changed, the SDK changed — and
// only the first is drift. Reporting the other two as exit code 4 is how operators learn to
// ignore exit code 4.
//
// # Where the comparison lives, and how many there are
//
// One, and it is not here. history.stateOf compares a file's recomputed source digest against the
// anchor run's recorded one and reports StateDrifted; internal/engine refuses on that state and
// renders engine.DriftError; internal/cli maps engine.ErrDrifted to exit code 4. Nothing else in
// the repository compares two checksums, and nothing should — a second comparison is how two
// answers about one migration start to disagree, and a comparison written against Checksum rather
// than against Checksum.Source is the exact mistake ADR 0009 spent its Context section on.
//
// This package therefore exposes no Equal, no Verify, and no drift type. Checksum is comparable,
// so == exists for a test that wants it, and the halves are separate fields so a caller has to
// name the one it means.
//
// # Normalization covers transport, not authorship
//
// The rule is narrow on purpose: normalize a byte difference only if version control can
// introduce or remove it without producing a diff anyone can see.
//
// Line endings qualify. Git rewrites them on checkout and on checkin, so one commit is CRLF on
// a Windows laptop and LF on Linux CI. Calling that drift is an accusation nobody can act on —
// git diff shows nothing and there is no repair flag.
//
// Nothing else qualifies. A reindent, a trailing space, an added blank line, and a byte order
// mark are all visible, revertible diffs, so they move the hash. A rewrite is a rewrite. The
// tempting rule — normalize whatever cannot change the emitted IR — is the wrong one: it would
// also excuse comment edits and formatting runs, and every extra clause is a second
// specification that every future SDK must reimplement identically forever.
//
// # What is deliberately outside the hashed bytes
//
// The irVersion envelope. ADR 0007 puts the hashable unit at the operations array, so a version
// bump that reshapes no operation leaves every applied migration's checksum untouched rather
// than firing drift detection across every installation at once.
//
// Source attribution. A call site is a machine-specific path, so hashing it would make one
// migration read as drifted on CI and clean on a laptop, and the Go and TypeScript SDKs report
// call sites differently for the same migration anyway.
//
// The migration ID and direction. The history row already names both. Hashing them would
// create fields that can disagree with their own container — the same reasoning that kept them
// out of the IR envelope.
//
// # Why migr1 and not sha256
//
// The prefix has exactly one job: answer whether this build can compare against a recorded
// value at all. Naming the primitive answers a question nobody asked. Adding a third input,
// changing the normalization rule, or reordering the halves would each keep SHA-256 while
// changing every digest, so a sha256 tag would assert a compatibility that does not exist —
// worse than no tag, because it invites the comparison. migr1 versions the whole construction.
// Go's h1: is the precedent, and it also declines to say sha256 despite being sha256.
//
// A build that computes two schemes verifies old rows under the scheme they were written with
// and writes new rows under the current one. That is the upgrade path invariant #6 requires,
// and it is why SchemeError exists before a second scheme does.
//
// Note that this is a fourth independent version line, alongside the framework version, the
// irVersion, and the protocol version. Nothing couples them.
//
// # Stability
//
// Determinism is not enough on its own. Hashing the same input twice in one process proves
// nothing about a Go release that changes the encoder, because both runs change together. The
// committed vectors in vector_test.go are the only guard that can catch it: each was derived
// from the specification above by a second implementation, and each pins the normalized source
// bytes and the emitted IR bytes as well as the final value, so a failure names which of the
// two inputs moved. They are a contract, not a convenience — regenerating one from this
// package's own output silently blesses whatever changed.
//
// One vector has moved, once, and the circumstances are what a later reader needs. In M7-T9
// ir.Marshal stopped escaping U+2028 and U+2029, which docs/ir.md §3.3 had always said were
// emitted as UTF-8 (ADR 0047), so the lineSeparators vector's IR half changed while its source
// half and every other vector stayed put. Its new value was derived by hashing the expected
// bytes outside this package, and hashing the old bytes the same way reproduced the old value —
// which is the difference between deriving a vector and accepting one. A decision recorded in an
// ADR is the only thing that may move another, and v1 having never been released is the
// condition that made it payable at all: after a release, the same change reads as every applied
// migration in every installation having drifted.
//
// Two things on the hash path look wrong and are not. Operations() clones each operation
// through a map[string]json.RawMessage, which is a Go map on a path invariant #8 requires to be
// deterministic; encoding/json sorts map keys, and the result is decoded back into a struct
// before the final marshal, so field order comes from the struct declaration either way. And no
// JSON number reaches the hashed bytes at all — every v1 field is a string, a bool, or a list
// of them, and a logical type marshals as text — which is what keeps Go and JavaScript number
// formatting out of the checksum entirely.
//
// # Limitation
//
// A checksum covers one file. A migration that calls into a helper package whose body changed
// has an unchanged source digest. Closing that needs the content-hashed build the Go runtime
// adapter already performs, and it is not attempted here.
package checksum
