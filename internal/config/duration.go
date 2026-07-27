package config

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Duration is a time.Duration written in YAML as a string such as "30s".
type Duration time.Duration

// UnmarshalYAML parses a duration string, rejecting bare numbers.
func (d *Duration) UnmarshalYAML(unmarshal func(any) error) error {
	var s string
	if err := unmarshal(&s); err != nil {
		return fmt.Errorf("duration must be a string such as \"30s\": %w", err)
	}

	parsed, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid duration %q%s", s, durationHint(s))
	}

	*d = Duration(parsed)

	return nil
}

// durationHint suggests a unit when the value looks like a bare number.
func durationHint(s string) string {
	if _, err := strconv.Atoi(strings.TrimSpace(s)); err != nil {
		return ""
	}

	return fmt.Sprintf(": missing unit (try %q)", s+"s")
}

// MarshalYAML renders the duration back to its string form.
func (d Duration) MarshalYAML() ([]byte, error) {
	return []byte(d.String()), nil
}

// MarshalJSON renders the duration as a JSON string.
func (d Duration) MarshalJSON() ([]byte, error) {
	return json.Marshal(d.String())
}

// String renders the duration in Go's canonical form.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// Duration returns the underlying time.Duration.
func (d Duration) Duration() time.Duration {
	return time.Duration(d)
}
