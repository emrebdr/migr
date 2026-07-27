package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Scheme is the checksum scheme this build computes; it names the whole construction.
const Scheme = "migr1"

// separator joins the scheme and the two hex digests, and cannot occur inside either.
const separator = ":"

// hexLen is how many characters one digest occupies in the canonical form.
const hexLen = sha256.Size * 2

// supported lists every checksum scheme this build can compute, oldest first.
var supported = []string{Scheme}

// ErrMalformed reports a recorded value this build cannot read as a checksum at all.
var ErrMalformed = errors.New("malformed checksum")

// ErrNotComputed reports an attempt to render a checksum that was never computed.
var ErrNotComputed = errors.New("checksum was never computed")

// Supported returns every checksum scheme this build can compute.
func Supported() []string {
	return slices.Clone(supported)
}

// SchemeError reports a checksum recorded under a scheme this build cannot compute.
type SchemeError struct {
	Received  string
	Supported []string
}

// Error names both the recorded scheme and the ones this build computes.
func (e *SchemeError) Error() string {
	return fmt.Sprintf("unsupported checksum scheme %q: this build of migr computes %s",
		e.Received, describeSchemes(e.Supported))
}

// describeSchemes renders the supported set, pluralizing once there is more than one.
func describeSchemes(schemes []string) string {
	if len(schemes) == 1 {
		return "scheme " + schemes[0]
	}

	return "schemes " + strings.Join(schemes, ", ")
}

// Parse reads a recorded checksum, refusing a scheme this build does not compute.
func Parse(s string) (Checksum, error) {
	scheme, rest, found := strings.Cut(s, separator)
	if !found || !schemeShaped(scheme) {
		return Checksum{}, fmt.Errorf("%w: %q does not begin with a scheme (expected %s%s...)",
			ErrMalformed, s, Scheme, separator)
	}

	if !slices.Contains(supported, scheme) {
		return Checksum{}, &SchemeError{Received: scheme, Supported: Supported()}
	}

	source, emitted, found := strings.Cut(rest, separator)
	if !found {
		return Checksum{}, fmt.Errorf("%w: %q carries one digest, not two", ErrMalformed, s)
	}

	return parsePair(source, emitted)
}

// parsePair decodes both halves, naming which one is wrong when either is.
func parsePair(source, emitted string) (Checksum, error) {
	sourceDigest, err := parseDigest("source", source)
	if err != nil {
		return Checksum{}, err
	}

	emittedDigest, err := parseDigest("ir", emitted)
	if err != nil {
		return Checksum{}, err
	}

	return Checksum{Source: sourceDigest, IR: emittedDigest}, nil
}

// parseDigest decodes one half, accepting only the canonical lowercase hex spelling.
func parseDigest(half, s string) (Digest, error) {
	if len(s) != hexLen {
		return Digest{}, fmt.Errorf("%w: %s digest is %d characters, want %d",
			ErrMalformed, half, len(s), hexLen)
	}

	if s != strings.ToLower(s) {
		return Digest{}, fmt.Errorf("%w: %s digest %q must be lowercase hex", ErrMalformed, half, s)
	}

	raw, err := hex.DecodeString(s)
	if err != nil {
		return Digest{}, fmt.Errorf("%w: %s digest %q is not hex: %w", ErrMalformed, half, s, err)
	}

	return Digest(raw), nil
}

// schemeShaped reports whether a prefix could name a scheme at all.
func schemeShaped(s string) bool {
	if s == "" {
		return false
	}

	for _, r := range s {
		if !schemeRune(r) {
			return false
		}
	}

	return true
}

// schemeRune reports whether a character may appear in a scheme name.
func schemeRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
}
