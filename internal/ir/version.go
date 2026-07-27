package ir

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// Version is the irVersion this build produces. A breaking wire change bumps it.
const Version = 1

// supported lists every irVersion this build can read, oldest first.
var supported = []int{Version}

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

// NewDocument wraps operations in an envelope stamped with this build's version.
func NewDocument(ops Operations) *Document {
	if ops == nil {
		ops = Operations{}
	}

	return &Document{IRVersion: Version, Operations: ops}
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
