package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Marshal renders a snapshot as JSON, in its canonical order, refusing one that is not valid.
func Marshal(s Snapshot) ([]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, fmt.Errorf("encoding a schema snapshot: %w", err)
	}

	var buf bytes.Buffer

	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(s.Canonical()); err != nil {
		return nil, fmt.Errorf("encoding a schema snapshot: %w", err)
	}

	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// Unmarshal decodes a snapshot, refusing a member this build does not know and one that is invalid.
func Unmarshal(data []byte) (Snapshot, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	var decoded Snapshot

	if err := dec.Decode(&decoded); err != nil {
		return Snapshot{}, fmt.Errorf("decoding a schema snapshot: %w", err)
	}

	if err := decoded.Validate(); err != nil {
		return Snapshot{}, fmt.Errorf("decoding a schema snapshot: %w", err)
	}

	return decoded, nil
}
