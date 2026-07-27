package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// Digest is one SHA-256 sum, fixed width so an ill-formed digest is unrepresentable.
type Digest [sha256.Size]byte

// String renders the digest as 64 lowercase hex characters.
func (d Digest) String() string {
	return hex.EncodeToString(d[:])
}

// Zero reports whether the digest was never computed.
func (d Digest) Zero() bool {
	return d == Digest{}
}

// Checksum identifies a migration by its normalized source and the IR it emitted.
type Checksum struct {
	Source Digest
	IR     Digest
}

// Of pairs both halves into the value a history row records.
func Of(source []byte, ops ir.Operations) (Checksum, error) {
	emitted, err := OfOperations(ops)
	if err != nil {
		return Checksum{}, err
	}

	return Checksum{Source: OfSource(source), IR: emitted}, nil
}

// OfSource digests a migration's normalized source, the half that decides drift.
func OfSource(source []byte) Digest {
	return sha256.Sum256(normalize(source))
}

// OfOperations digests the emitted IR, the half that records what one run produced.
func OfOperations(ops ir.Operations) (Digest, error) {
	encoded, err := ir.Marshal(ops)
	if err != nil {
		return Digest{}, fmt.Errorf("hashing the emitted IR: %w", err)
	}

	return sha256.Sum256(encoded), nil
}

// String renders the checksum in the canonical form a history row stores.
func (c Checksum) String() string {
	return Scheme + separator + c.Source.String() + separator + c.IR.String()
}

// Zero reports whether the checksum was never computed.
func (c Checksum) Zero() bool {
	return c == Checksum{}
}

// MarshalText renders the canonical form, refusing a checksum that was never computed.
func (c Checksum) MarshalText() ([]byte, error) {
	if c.Zero() {
		return nil, ErrNotComputed
	}

	return []byte(c.String()), nil
}

// UnmarshalText reads a recorded checksum through Parse.
func (c *Checksum) UnmarshalText(text []byte) error {
	parsed, err := Parse(string(text))
	if err != nil {
		return err
	}

	*c = parsed

	return nil
}
