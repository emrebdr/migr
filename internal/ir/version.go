package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Version is the newest irVersion this build reads, which is what the engine states at initialize.
const Version = 3

// supported lists every irVersion this build can read, oldest first: each is a superset of the last.
var supported = []int{1, 2, Version}

// ErrNoVersion reports an IR document that carries no irVersion at all.
var ErrNoVersion = errors.New(`document has no "irVersion" field`)

// Supported returns every irVersion this build can read.
func Supported() []int {
	return slices.Clone(supported)
}

// VersionError reports an IR document this build cannot read.
type VersionError struct {
	Received  int
	Supported []int
}

// Error names both the received version and the ones this build understands.
func (e *VersionError) Error() string {
	return fmt.Sprintf("unsupported irVersion %d: this build of migr reads %s",
		e.Received, describeVersions(e.Supported))
}

// CheckVersion reports whether an irVersion can be read by this build.
func CheckVersion(v int) error {
	if slices.Contains(supported, v) {
		return nil
	}

	return &VersionError{Received: v, Supported: Supported()}
}

// describeVersions renders the supported set, pluralizing once there is more than one.
func describeVersions(versions []int) string {
	parts := make([]string, len(versions))
	for i, v := range versions {
		parts[i] = strconv.Itoa(v)
	}

	if len(parts) == 1 {
		return "irVersion " + parts[0]
	}

	return "irVersion " + strings.Join(parts, ", ")
}

// Document is the versioned envelope: the unit an SDK emits and the engine reads.
type Document struct {
	IRVersion  int        `json:"irVersion"`
	Operations Operations `json:"operations"`
}

// NewDocument wraps operations in an envelope stamped with the oldest version that can hold them.
func NewDocument(ops Operations) *Document {
	if ops == nil {
		ops = Operations{}
	}

	return &Document{IRVersion: VersionOf(ops), Operations: ops}
}

// VersionOf returns the oldest irVersion that carries every operation in a list.
func VersionOf(ops Operations) int {
	version := 1

	for _, op := range ops {
		version = max(version, Since(op))
	}

	return version
}

// noTransactionSince is the irVersion that first carries raw's no_transaction (ADR 0084).
const noTransactionSince = 3

// Since returns the oldest irVersion that carries one operation, which a field can raise above its kind's.
func Since(op Operation) int {
	if raw, ok := op.(Raw); ok && raw.NoTransaction {
		return noTransactionSince
	}

	return op.Kind().Since()
}

// sinceWhat names what an operation needs a newer version for: its kind, or the field that raised it.
func sinceWhat(op Operation) string {
	if Since(op) > op.Kind().Since() {
		return string(op.Kind()) + " carrying no_transaction"
	}

	return string(op.Kind())
}

// NewerError reports an operation a document or a session's stated version does not yet carry.
type NewerError struct {
	// Kind is the operation that needs the newer version.
	Kind Kind
	// What names the kind, or the kind and the field that needs the newer version.
	What string
	// Needs is the oldest version that carries it.
	Needs int
	// Stated is the version the document or the session stated.
	Stated int
}

// Error names the operation, the version it needs, and the one that was stated.
func (e *NewerError) Error() string {
	return fmt.Sprintf("%s needs irVersion %d, and what carried it states irVersion %d: "+
		"upgrade migr to read it", e.What, e.Needs, e.Stated)
}

// CheckCarried reports the first operation a stated irVersion does not carry, and nothing otherwise.
func CheckCarried(ops Operations, stated int) error {
	for _, op := range ops {
		if Since(op) > stated {
			return &NewerError{Kind: op.Kind(), What: sinceWhat(op), Needs: Since(op), Stated: stated}
		}
	}

	return nil
}

// MarshalJSON renders the envelope, refusing a document that was never stamped.
func (d Document) MarshalJSON() ([]byte, error) {
	if d.IRVersion == 0 {
		return nil, ErrNoVersion
	}

	type alias Document

	return Marshal(alias(d))
}

// documentHeader reads the version alone, so it can be checked before anything else.
type documentHeader struct {
	IRVersion *int `json:"irVersion"`
}

// UnmarshalDocument decodes an IR document, checking its version before its operations.
func UnmarshalDocument(data []byte) (*Document, error) {
	version, err := readVersion(data)
	if err != nil {
		return nil, err
	}

	if err := CheckVersion(version); err != nil {
		return nil, err
	}

	var doc Document
	if err := Unmarshal(data, &doc); err != nil {
		return nil, err
	}

	if doc.Operations == nil {
		doc.Operations = Operations{}
	}

	if err := CheckCarried(doc.Operations, doc.IRVersion); err != nil {
		return nil, err
	}

	return &doc, nil
}

// readVersion extracts the irVersion without decoding anything else in the document.
func readVersion(data []byte) (int, error) {
	var head documentHeader
	if err := json.Unmarshal(data, &head); err != nil {
		return 0, fmt.Errorf("reading the irVersion: %w", err)
	}

	if head.IRVersion == nil {
		return 0, fmt.Errorf("%w (expected %s)", ErrNoVersion, describeVersions(supported))
	}

	return *head.IRVersion, nil
}
