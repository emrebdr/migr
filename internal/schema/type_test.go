package schema

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

func TestKindSetIsComplete(t *testing.T) {
	const declared = 18

	if len(Kinds()) != declared {
		t.Fatalf("CLAUDE.md §4.1 declares %d logical types, Kinds() has %d", declared, len(Kinds()))
	}

	for _, k := range Kinds() {
		if !k.Valid() {
			t.Errorf("Kind %q is listed but not valid", k)
		}
	}

	if Kind("nonesuch").Valid() {
		t.Error("an unlisted kind must not report itself valid")
	}
}

func TestEveryKindParses(t *testing.T) {
	sample := map[Kind]string{
		KindString:  "string(255)",
		KindDecimal: "decimal(10,2)",
		KindEnum:    "enum(a,b)",
		KindArray:   "array(int32)",
	}

	for _, k := range Kinds() {
		s, ok := sample[k]
		if !ok {
			s = string(k)
		}

		t.Run(string(k), func(t *testing.T) {
			parsed, err := Parse(s)
			if err != nil {
				t.Fatalf("kind %q has no working canonical form: %v", k, err)
			}

			if parsed.Kind() != k {
				t.Errorf("Parse(%q).Kind() = %q, want %q", s, parsed.Kind(), k)
			}
		})
	}
}

func TestKindsIsACopy(t *testing.T) {
	first := Kinds()
	first[0] = "tampered"

	if Kinds()[0] == "tampered" {
		t.Error("Kinds() must hand out a copy, not the package's own slice")
	}
}

func TestScalarRejectsParameterizedKinds(t *testing.T) {
	for _, k := range []Kind{KindString, KindDecimal, KindEnum, KindArray} {
		t.Run(string(k), func(t *testing.T) {
			if _, err := Scalar(k); err == nil {
				t.Fatalf("Scalar(%q) should fail; it needs parameters", k)
			}
		})
	}

	if _, err := Scalar("nonesuch"); err == nil {
		t.Error("Scalar of an unknown kind should fail")
	}
}

func TestConstructorsValidate(t *testing.T) {
	tests := []struct {
		name string
		call func() (Type, error)
		want string
	}{
		{name: "string zero", call: func() (Type, error) { return String(0) }, want: "at least 1"},
		{name: "string negative", call: func() (Type, error) { return String(-1) }, want: "at least 1"},
		{name: "decimal zero precision", call: func() (Type, error) { return Decimal(0, 0) }, want: "precision must be at least 1"},
		{name: "decimal negative scale", call: func() (Type, error) { return Decimal(10, -1) }, want: "must not be negative"},
		{name: "decimal scale over precision", call: func() (Type, error) { return Decimal(2, 5) }, want: "exceeds precision"},
		{name: "enum empty", call: func() (Type, error) { return Enum() }, want: "at least one value"},
		{name: "enum duplicate", call: func() (Type, error) { return Enum("a", "a") }, want: "more than once"},
		{name: "enum illegal", call: func() (Type, error) { return Enum("a b") }, want: "contains"},
		{name: "enum empty value", call: func() (Type, error) { return Enum("a", "") }, want: "is empty"},
		{name: "array of zero", call: func() (Type, error) { return Array(Type{}) }, want: "zero Type"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.call()
			if err == nil {
				t.Fatalf("should have failed, got %s", got)
			}

			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to mention %q", err, tt.want)
			}

			if !got.Zero() {
				t.Errorf("a failed constructor must return the zero Type, got %#v", got)
			}
		})
	}
}

func TestAccessors(t *testing.T) {
	str, err := String(255)
	if err != nil {
		t.Fatalf("String(255): %v", err)
	}

	if n, ok := str.Length(); !ok || n != 255 {
		t.Errorf("Length() = %d, %v; want 255, true", n, ok)
	}

	if _, ok := str.Precision(); ok {
		t.Error("Precision() must report false for a string type")
	}

	dec, err := Decimal(10, 2)
	if err != nil {
		t.Fatalf("Decimal(10,2): %v", err)
	}

	p, okP := dec.Precision()
	s, okS := dec.Scale()

	if !okP || !okS || p != 10 || s != 2 {
		t.Errorf("Precision/Scale = %d/%d (%v/%v); want 10/2, true/true", p, s, okP, okS)
	}

	arr, err := Array(str)
	if err != nil {
		t.Fatalf("Array(string(255)): %v", err)
	}

	elem, ok := arr.Elem()
	if !ok || !elem.Equal(str) {
		t.Errorf("Elem() = %s, %v; want string(255), true", elem, ok)
	}
}

func TestEnumValuesAreCopied(t *testing.T) {
	original := []string{"draft", "live"}

	e, err := Enum(original...)
	if err != nil {
		t.Fatalf("Enum: %v", err)
	}

	original[0] = "tampered"

	if e.String() != "enum(draft,live)" {
		t.Errorf("the constructor must copy its input, got %s", e)
	}

	handed, _ := e.Values()
	handed[0] = "tampered"

	if e.String() != "enum(draft,live)" {
		t.Errorf("Values() must hand out a copy, got %s", e)
	}
}

func TestEnumOrderIsSignificant(t *testing.T) {
	ab, err := Enum("a", "b")
	if err != nil {
		t.Fatalf("Enum(a,b): %v", err)
	}

	ba, err := Enum("b", "a")
	if err != nil {
		t.Fatalf("Enum(b,a): %v", err)
	}

	if ab.Equal(ba) {
		t.Error("enum(a,b) and enum(b,a) are different types; PostgreSQL compares labels by declaration order")
	}
}

func TestEqualAgreesWithStructure(t *testing.T) {
	for _, a := range canonical {
		for _, b := range canonical {
			ta, err := Parse(a)
			if err != nil {
				t.Fatalf("Parse(%q): %v", a, err)
			}

			tb, err := Parse(b)
			if err != nil {
				t.Fatalf("Parse(%q): %v", b, err)
			}

			if ta.Equal(tb) != reflect.DeepEqual(ta, tb) {
				t.Errorf("Equal(%q,%q) = %v but DeepEqual = %v", a, b, ta.Equal(tb), reflect.DeepEqual(ta, tb))
			}
		}
	}
}

func TestZeroTypeIsNotAType(t *testing.T) {
	var zero Type

	if !zero.Zero() {
		t.Error("a zero Type must report Zero()")
	}

	if got := zero.String(); got != invalidType {
		t.Errorf("zero.String() = %q, want %q", got, invalidType)
	}

	if _, err := zero.MarshalText(); !errors.Is(err, ErrInvalidType) {
		t.Errorf("marshaling a zero Type must fail with ErrInvalidType, got %v", err)
	}

	if _, err := json.Marshal(zero); err == nil {
		t.Error("a zero Type must not serialize into an IR document")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	type column struct {
		Name string `json:"name"`
		Type Type   `json:"type"`
	}

	for _, s := range canonical {
		t.Run(s, func(t *testing.T) {
			parsed, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q): %v", s, err)
			}

			encoded, err := json.Marshal(column{Name: "c", Type: parsed})
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}

			want := `{"name":"c","type":"` + s + `"}`
			if string(encoded) != want {
				t.Errorf("JSON = %s, want %s", encoded, want)
			}

			var back column
			if err := json.Unmarshal(encoded, &back); err != nil {
				t.Fatalf("unmarshaling: %v", err)
			}

			if !back.Type.Equal(parsed) {
				t.Errorf("JSON round trip changed the type: %s -> %s", parsed, back.Type)
			}
		})
	}
}

func TestYAMLRoundTrip(t *testing.T) {
	for _, s := range canonical {
		t.Run(s, func(t *testing.T) {
			parsed, err := Parse(s)
			if err != nil {
				t.Fatalf("Parse(%q): %v", s, err)
			}

			encoded, err := yaml.Marshal(map[string]Type{"type": parsed})
			if err != nil {
				t.Fatalf("marshaling: %v", err)
			}

			var back map[string]Type
			if err := yaml.Unmarshal(encoded, &back); err != nil {
				t.Fatalf("unmarshaling %s: %v", encoded, err)
			}

			if !back["type"].Equal(parsed) {
				t.Errorf("YAML round trip changed the type: %s -> %s", parsed, back["type"])
			}
		})
	}
}

func TestUnmarshalRejectsMalformedText(t *testing.T) {
	var target Type

	if err := json.Unmarshal([]byte(`"decimal(10)"`), &target); err == nil {
		t.Fatal("unmarshaling a malformed type must fail")
	}

	if !target.Zero() {
		t.Errorf("a failed unmarshal must leave the target zero, got %#v", target)
	}
}
