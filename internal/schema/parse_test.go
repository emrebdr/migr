package schema

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// canonical lists one canonical spelling per shape the type system must express.
var canonical = []string{
	"string(1)",
	"string(255)",
	"text",
	"bool",
	"int16",
	"int32",
	"int64",
	"float32",
	"float64",
	"decimal(1,0)",
	"decimal(10,2)",
	"decimal(38,38)",
	"uuid",
	"json",
	"timestamp",
	"timestamptz",
	"date",
	"time",
	"binary",
	"enum(a)",
	"enum(draft,live,archived)",
	"enum(EN,FR,DE)",
	"enum(in_progress,not-started)",
	"array(int32)",
	"array(text)",
	"array(string(255))",
	"array(decimal(10,2))",
	"array(enum(draft,live))",
	"array(array(int32))",
}

func TestRoundTripIsLossless(t *testing.T) {
	for _, s := range canonical {
		t.Run(s, func(t *testing.T) {
			parsed, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", s, err)
			}

			if got := parsed.String(); got != s {
				t.Errorf("round trip changed the spelling: %q -> %q", s, got)
			}
		})
	}
}

func TestParseIsIdempotent(t *testing.T) {
	for _, s := range canonical {
		t.Run(s, func(t *testing.T) {
			once, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", s, err)
			}

			twice, err := Parse(once.String())
			if err != nil {
				t.Fatalf("reparsing %q failed: %v", once.String(), err)
			}

			if !reflect.DeepEqual(once, twice) {
				t.Errorf("reparsing produced a different value: %#v vs %#v", once, twice)
			}
		})
	}
}

func TestCanonicalFormIsUnique(t *testing.T) {
	seen := make(map[string]string, len(canonical))

	for _, s := range canonical {
		parsed, err := Parse(s)
		if err != nil {
			t.Fatalf("Parse(%q) failed: %v", s, err)
		}

		if prior, ok := seen[parsed.String()]; ok {
			t.Errorf("%q and %q share the canonical form %q", prior, s, parsed.String())
		}

		seen[parsed.String()] = s
	}
}

func TestWhitespaceNormalizes(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "space after comma", input: "decimal(10, 2)", want: "decimal(10,2)"},
		{name: "spaces throughout", input: "decimal( 10 , 2 )", want: "decimal(10,2)"},
		{name: "enum values", input: "enum(a, b, c)", want: "enum(a,b,c)"},
		{name: "leading and trailing", input: "  int32  ", want: "int32"},
		{name: "before parenthesis", input: "string (255)", want: "string(255)"},
		{name: "nested element", input: "array( decimal(10, 2) )", want: "array(decimal(10,2))"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse(tt.input)
			if err != nil {
				t.Fatalf("Parse(%q) failed: %v", tt.input, err)
			}

			if got := parsed.String(); got != tt.want {
				t.Errorf("Parse(%q).String() = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "empty", input: "", want: "type is empty"},
		{name: "whitespace only", input: "   ", want: "type is empty"},
		{name: "uppercase", input: "DECIMAL(10,2)", want: "did you mean \"decimal\""},
		{name: "mixed case", input: "Int32", want: "did you mean \"int32\""},
		{name: "unknown name", input: "blob", want: "expected one of"},
		{name: "decimal missing scale", input: "decimal(10)", want: "a precision and a scale, got 1"},
		{name: "decimal bare", input: "decimal", want: "requires a precision and a scale"},
		{name: "decimal scale over precision", input: "decimal(2,5)", want: "scale 5 exceeds precision 2"},
		{name: "decimal zero precision", input: "decimal(0,0)", want: "precision must be at least 1"},
		{name: "decimal negative scale", input: "decimal(10,-1)", want: "scale must not be negative"},
		{name: "decimal non integer", input: "decimal(10,x)", want: `scale must be an integer, got "x"`},
		{name: "decimal empty arg", input: "decimal(10,)", want: "scale is missing"},
		{name: "string bare", input: "string", want: "requires a length"},
		{name: "string zero", input: "string(0)", want: "length must be at least 1"},
		{name: "string negative", input: "string(-5)", want: "length must be at least 1"},
		{name: "string non integer", input: "string(many)", want: `length must be an integer, got "many"`},
		{name: "string two args", input: "string(10,2)", want: "takes one length, got 2"},
		{name: "scalar with parameters", input: "text(10)", want: "text takes no parameters"},
		{name: "scalar with empty parens", input: "bool()", want: "bool takes no parameters"},
		{name: "enum empty", input: "enum()", want: "at least one value"},
		{name: "enum bare", input: "enum", want: "requires at least one value"},
		{name: "enum duplicate", input: "enum(a,a)", want: `"a" appears more than once`},
		{name: "enum illegal space", input: "enum(hello world)", want: "contains ' '"},
		{name: "enum illegal dot", input: "enum(a.b)", want: "contains '.'"},
		{name: "enum empty value", input: "enum(a,,b)", want: "is empty"},
		{name: "array empty", input: "array()", want: "requires an element type"},
		{name: "array bare", input: "array", want: "requires an element type"},
		{name: "array two args", input: "array(int32,int64)", want: "takes one element type, got 2"},
		{name: "array bad element", input: "array(blob)", want: "array element:"},
		{name: "array nested bad element", input: "array(decimal(10))", want: "array element: decimal takes a precision"},
		{name: "unclosed parenthesis", input: "array(int32", want: "missing closing parenthesis"},
		{name: "extra closing parenthesis", input: "text)", want: "unexpected closing parenthesis"},
		{name: "unbalanced nested", input: "array(string(255)", want: "missing closing parenthesis"},
		{name: "no name", input: "(255)", want: "type name is missing"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parsed, err := Parse(tt.input)
			if err == nil {
				t.Fatalf("Parse(%q) should fail, got %s", tt.input, parsed)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse(%q) error = %q, want it to mention %q", tt.input, err, tt.want)
			}

			if !parsed.Zero() {
				t.Errorf("a failed parse must return the zero Type, got %#v", parsed)
			}
		})
	}
}

func TestEveryRejectionIsSpecific(t *testing.T) {
	inputs := []string{
		"", "DECIMAL(10,2)", "decimal(10)", "decimal(2,5)", "string(0)", "string",
		"text(10)", "enum()", "enum(a,a)", "enum(hello world)", "array()", "array(int32",
	}

	messages := make(map[string]string, len(inputs))

	for _, in := range inputs {
		err := parseErrorFor(t, in)

		if err.Message == "" {
			t.Errorf("Parse(%q) produced an error with no message", in)
		}

		if prior, ok := messages[err.Message]; ok {
			t.Errorf("Parse(%q) and Parse(%q) share the message %q; precision requires distinct diagnoses",
				prior, in, err.Message)
		}

		messages[err.Message] = in
	}
}

func TestParseErrorCarriesItsInput(t *testing.T) {
	err := parseErrorFor(t, "decimal(10)")

	if err.Input != "decimal(10)" {
		t.Errorf("ParseError.Input = %q, want %q", err.Input, "decimal(10)")
	}

	if err.Hint == "" {
		t.Error("a recoverable mistake should carry a hint")
	}

	if !strings.Contains(err.Error(), "decimal(10,2)") {
		t.Errorf("the hint should show the canonical form, got %q", err.Error())
	}
}

func TestArrayNestingIsBounded(t *testing.T) {
	deep := strings.Repeat("array(", maxArrayDepth+2) + "int32" + strings.Repeat(")", maxArrayDepth+2)

	if _, err := Parse(deep); err == nil {
		t.Fatal("nesting past the limit must be rejected, not accepted")
	}

	ok := strings.Repeat("array(", 4) + "int32" + strings.Repeat(")", 4)
	if _, err := Parse(ok); err != nil {
		t.Errorf("nesting within the limit must parse: %v", err)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range canonical {
		f.Add(s)
	}

	f.Add("")
	f.Add("decimal(10, 2)")
	f.Add("array(array(array(int32)))")
	f.Add("enum(a,,b)")
	f.Add("((((")
	f.Add("array(" + strings.Repeat("array(", 200))

	f.Fuzz(func(t *testing.T, s string) {
		parsed, err := Parse(s)
		if err != nil {
			return
		}

		again, err := Parse(parsed.String())
		if err != nil {
			t.Fatalf("Parse(%q) produced %q, which does not parse: %v", s, parsed, err)
		}

		if !reflect.DeepEqual(parsed, again) {
			t.Fatalf("Parse(%q) does not round-trip through its canonical form %q", s, parsed)
		}
	})
}

// parseErrorFor parses input that must fail and returns the structured error.
func parseErrorFor(t *testing.T, input string) *ParseError {
	t.Helper()

	_, err := Parse(input)
	if err == nil {
		t.Fatalf("Parse(%q) should have failed", input)
	}

	var pe *ParseError
	if !errors.As(err, &pe) {
		t.Fatalf("Parse(%q) returned %T, want *ParseError", input, err)
	}

	return pe
}
