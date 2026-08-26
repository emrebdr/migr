// Package wire is this SDK's half of the runtime protocol: framing, the
// envelope, the method names, the error codes, and the payloads.
//
// # Why it is duplicated
//
// The engine has all of this already, in internal/runtime/protocol. This SDK
// cannot import it. The Go internal rule would permit it — sdk/go sits under
// github.com/emrebdr/migr — but the import would make this module require the
// CLI module, dragging pgx, cobra, and a YAML parser into every program that
// writes a migration. Not doing that is the whole reason sdk/go is its own
// module.
//
// So the two halves are written twice, and neither is held to the other. Both
// are held to docs/protocol.md: internal/runtime/protocol has a spec test that
// reads the document, and so does this package, over the same file and the same
// byte-exact vectors. A change to one implementation that the document did not
// authorize fails on that side alone, which is what makes the document the
// contract rather than a description of whichever half was written first.
//
// # Why it is internal
//
// A migration author never sees these types. Exporting them would make the wire
// format part of this module's compatibility surface, and it is docs/protocol.md
// that owns that.
package wire
