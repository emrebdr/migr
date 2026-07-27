package checksum

import (
	"errors"
	"strings"
	"testing"
)

// The two halves of a well-formed recorded checksum, spelled out so cases can vary one.
const (
	sourceHex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	irHex     = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	aValid    = "migr1:" + sourceHex + ":" + irHex
)

func TestARecordedChecksumRoundTripsThroughItsString(t *testing.T) {
	const source = "func Up(s *migr.Schema) error { return nil }\n"

	original := mustOf(t, []byte(source), sampleOps(t))

	parsed, err := Parse(original.String())
	if err != nil {
		t.Fatalf("Parse(%q): %v", original, err)
	}

	if parsed != original {
		t.Errorf("Parse(%q) = %s, want the original value back", original, parsed)
	}

	text, err := original.MarshalText()
	if err != nil {
		t.Fatalf("MarshalText: %v", err)
	}

	var decoded Checksum
	if err := decoded.UnmarshalText(text); err != nil {
		t.Fatalf("UnmarshalText(%q): %v", text, err)
	}

	if decoded != original {
		t.Errorf("UnmarshalText(%q) = %s, want the original value back", text, decoded)
	}
}

func TestAnUnknownSchemeNamesBothSides(t *testing.T) {
	_, err := Parse(strings.Replace(aValid, "migr1:", "migr2:", 1))

	var schemeErr *SchemeError
	if !errors.As(err, &schemeErr) {
		t.Fatalf("Parse of a migr2 value = %v, want a *SchemeError", err)
	}

	if schemeErr.Received != "migr2" {
		t.Errorf("Received = %q, want %q", schemeErr.Received, "migr2")
	}

	for _, want := range []string{"migr2", Scheme} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
			t.Log("An operator has to know both what was recorded and what this build can")
			t.Log("compute, or the only available action is to guess.")
		}
	}
}

func TestAMalformedChecksumIsADifferentFailureFromAnUnknownScheme(t *testing.T) {
	_, malformed := Parse("migr1:nothex")
	if !errors.Is(malformed, ErrMalformed) {
		t.Errorf("Parse of a short digest = %v, want ErrMalformed", malformed)
	}

	_, unknown := Parse(strings.Replace(aValid, "migr1:", "migr2:", 1))
	if errors.Is(unknown, ErrMalformed) {
		t.Error("an unrecognized scheme reported as a malformed value")
		t.Log("They call for opposite actions: a malformed value means this history row is")
		t.Log("corrupt, an unknown scheme means upgrade the CLI. Collapsing them into one")
		t.Log("error sends the operator to the wrong place.")
	}
}

func TestParseRefusesAnySpellingButTheCanonicalOne(t *testing.T) {
	tests := []struct {
		name string
		in   string
	}{
		{name: "empty", in: ""},
		{name: "bare hex with no scheme", in: sourceHex},
		{name: "uppercase source digest", in: "migr1:" + strings.ToUpper(sourceHex) + ":" + irHex},
		{name: "uppercase ir digest", in: "migr1:" + sourceHex + ":" + strings.ToUpper(irHex)},
		{name: "one uppercase hex character", in: "migr1:" + sourceHex + ":F" + irHex[1:]},
		{name: "everything uppercased", in: strings.ToUpper(aValid)},
		{name: "uppercase scheme", in: "MIGR1:" + sourceHex + ":" + irHex},
		{name: "leading space", in: " " + aValid},
		{name: "trailing newline", in: aValid + "\n"},
		{name: "one digest short", in: aValid[:len(aValid)-1]},
		{name: "one digest long", in: aValid + "0"},
		{name: "only one digest", in: "migr1:" + sourceHex},
		{name: "an extra separator", in: aValid + ":" + irHex},
		{name: "scheme only", in: "migr1:"},
		{name: "sha256 prefix", in: "sha256:" + sourceHex + ":" + irHex},
		{name: "non-hex character in a digest", in: "migr1:" + sourceHex + ":g" + irHex[1:]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := Parse(tt.in); err == nil {
				t.Errorf("Parse(%q) = %s, want an error", tt.in, got)
				t.Log("Accepting a second spelling of one value puts both into the history")
				t.Log("table, where they compare unequal and read as drift forever after.")
			}
		})
	}
}

func TestTheZeroChecksumRefusesToRender(t *testing.T) {
	var zero Checksum

	if !zero.Zero() {
		t.Error("Zero() = false on an uncomputed checksum")
	}

	if _, err := zero.MarshalText(); !errors.Is(err, ErrNotComputed) {
		t.Errorf("MarshalText on a zero checksum = %v, want ErrNotComputed", err)
		t.Log("Rendering it would write migr1:0000...:0000... into a history row, a value")
		t.Log("that looks recorded and matches nothing.")
	}
}

func TestSupportedIsACopy(t *testing.T) {
	got := Supported()
	got[0] = "tampered"

	if Supported()[0] != Scheme {
		t.Errorf("Supported() = %q after a caller wrote to an earlier result", Supported())
	}
}
