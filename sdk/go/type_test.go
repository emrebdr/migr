package migr

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestEveryLogicalTypeHasOneSpellingAndItSurvivesAParse(t *testing.T) {
	for _, built := range []Type{
		Text(), Bool(), Int16(), Int32(), Int64(), Float32(), Float64(),
		UUID(), JSON(), Timestamp(), TimestampTZ(), Date(), Time(), Binary(),
		String(255), Decimal(10, 2), Enum("draft", "live"), Array(Int32()),
		Array(String(255)), Array(Enum("draft", "live")), Array(Array(Int32())),
	} {
		if err := built.Err(); err != nil {
			t.Fatalf("%s: %v", built, err)
		}

		parsed := ParseType(built.String())
		if err := parsed.Err(); err != nil {
			t.Errorf("%s does not parse back: %v", built, err)

			continue
		}

		if parsed.String() != built.String() {
			t.Errorf("%s parsed back as %s", built, parsed)
		}
	}
}

func TestParsingNormalizesWhitespaceAndPrintingNeverKeepsIt(t *testing.T) {
	for _, spelled := range []string{"decimal(10, 2)", " decimal( 10 , 2 ) ", "array( int32 )"} {
		parsed := ParseType(spelled)
		if err := parsed.Err(); err != nil {
			t.Fatalf("%q: %v", spelled, err)
		}

		if strings.ContainsAny(parsed.String(), " \t") {
			t.Errorf("%q printed as %q, which keeps the input spelling", spelled, parsed)
		}
	}

	t.Log("docs/ir.md §5.3: parsing normalizes, printing always emits the tight form. Two SDKs")
	t.Log("emitting different bytes for one type would break byte-identical output.")
}

func TestATypeSpelledInAnyOtherWayIsRefused(t *testing.T) {
	for _, spelled := range []string{
		"", "  ", "DECIMAL(10,2)", "Text", "varchar(255)", "string", "string()",
		"string(a)", "string(255", "int32)", "decimal(10)", "decimal(10,2,3)",
		"array()", "array(int32,int64)", "text(1)", "enum()",
	} {
		if err := ParseType(spelled).Err(); err == nil {
			t.Errorf("%q was accepted as a logical type", spelled)
		}
	}
}

func TestArrayNestingIsBoundedSoAPipeCannotRecurseThisAway(t *testing.T) {
	deep := strings.Repeat("array(", maxArrayDepth+1) + "int32" +
		strings.Repeat(")", maxArrayDepth+1)

	if err := ParseType(deep).Err(); err == nil {
		t.Errorf("a type nested %d deep was accepted", maxArrayDepth+1)
	}

	built := Int32()
	for range maxArrayDepth + 1 {
		built = Array(built)
	}

	if err := built.Err(); err == nil {
		t.Error("a constructor built a type nested past the limit")
	}
}

func TestADeeplyNestedTypeIsRefusedWithoutReadingAllOfIt(t *testing.T) {
	const levels = 200_000

	hostile := strings.Repeat("array(", levels) + "int32" + strings.Repeat(")", levels)

	if err := ParseType(hostile).Err(); err == nil {
		t.Errorf("a type nested %d deep was accepted", levels)
	}

	t.Log("docs/ir.md §5.4: nesting is bounded so that corrupt input arriving over a pipe")
	t.Log("produces an error instead of a stack overflow. The bound has to be inside the parser's")
	t.Log("own recursion -- catching it on the way back out of Array means the parser has already")
	t.Log("descended, and rescanned the remaining input, once per level. This returns in")
	t.Log("microseconds with the bound, and does not return at all without it.")
}

func TestEnumOrderIsPartOfTheType(t *testing.T) {
	if Enum("draft", "live").Equal(Enum("live", "draft")) {
		t.Error("two enums differing only in order are one type")
	}

	t.Log("docs/ir.md §5.2: PostgreSQL compares enum labels by declaration order, so the order")
	t.Log("is the type rather than a rendering of it.")
}

func TestAConstructorCarriesTheReasonItCouldNotBuildAType(t *testing.T) {
	for _, unbuildable := range []struct {
		built Type
		says  string
	}{
		{String(0), "at least 1"},
		{Decimal(0, 0), "at least 1"},
		{Decimal(2, -1), "must not be negative"},
		{Decimal(2, 3), "exceeds precision"},
		{Enum(), "at least one value"},
		{Enum("a b"), "contains"},
		{Enum("a", "a"), "more than once"},
		{Array(Type{}), "array element"},
		{Scalar(TypeString), "takes parameters"},
		{Scalar("varchar"), "unknown type"},
	} {
		err := unbuildable.built.Err()
		if err == nil {
			t.Errorf("%s was built", unbuildable.built)

			continue
		}

		if !strings.Contains(err.Error(), unbuildable.says) {
			t.Errorf("the reason does not say %q: %v", unbuildable.says, err)
		}

		if _, err := unbuildable.built.MarshalText(); err == nil {
			t.Errorf("%s crossed the wire anyway", unbuildable.built)
		}
	}
}

func TestATypeNobodyGaveIsNotTheSameAsOneThatCouldNotBeBuilt(t *testing.T) {
	if !(Type{}).Zero() {
		t.Error("a type nobody gave does not report itself as one")
	}

	if String(0).Zero() {
		t.Error("a type that could not be built reports itself as one nobody gave")
	}

	if err := (Type{}).Err(); !errors.Is(err, ErrInvalidType) {
		t.Errorf("a type nobody gave reports %v, want %v", err, ErrInvalidType)
	}
}

func TestNoJSONNumberAppearsInsideAnOperation(t *testing.T) {
	document, err := documentOf(CreateTable{
		Table: "measurements",
		Columns: []Column{
			{Name: "label", Type: String(255)},
			{Name: "amount", Type: Decimal(10, 2)},
			{Name: "tags", Type: Array(Enum("draft", "live"))},
		},
	})
	if err != nil {
		t.Fatalf("encoding: %v", err)
	}

	dec := json.NewDecoder(bytes.NewReader(document))
	dec.UseNumber()

	var decoded any
	if err := dec.Decode(&decoded); err != nil {
		t.Fatalf("decoding: %v", err)
	}

	if found := numbersIn(decoded); found != "" {
		t.Errorf("the document carries the JSON number %s: %s", found, document)
	}

	t.Log("docs/ir.md §3.5: a column's length and a decimal's precision travel inside the type")
	t.Log("string, because Go and JavaScript do not agree on every number rendering and one")
	t.Log("disagreement would break byte-identical output across SDKs.")
}

// numbersIn returns the first JSON number anywhere in a decoded document, and empty for none.
func numbersIn(decoded any) string {
	switch v := decoded.(type) {
	case json.Number:
		return v.String()
	case []any:
		for _, item := range v {
			if found := numbersIn(item); found != "" {
				return found
			}
		}
	case map[string]any:
		for _, item := range v {
			if found := numbersIn(item); found != "" {
				return found
			}
		}
	}

	return ""
}
