package schema

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// mustParse returns a logical type by its canonical spelling, failing the test rather than the call.
func mustParse(t *testing.T, spelling string) Type {
	t.Helper()

	parsed, err := Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	return parsed
}

// mustExact returns the exact column type a driver would record for a distinct native spelling.
func mustExact(t *testing.T, spelling, native string) ColumnType {
	t.Helper()

	built, err := ExactType(mustParse(t, spelling), native)
	if err != nil {
		t.Fatalf("ExactType(%q, %q): %v", spelling, native, err)
	}

	return built
}

// mustAmbiguous returns the inexact column type a driver would record for a shared native spelling.
func mustAmbiguous(t *testing.T, spelling, native string) ColumnType {
	t.Helper()

	built, err := AmbiguousType(mustParse(t, spelling), native)
	if err != nil {
		t.Fatalf("AmbiguousType(%q, %q): %v", spelling, native, err)
	}

	return built
}

// mustUnknown returns the column type a driver would record for a spelling it cannot map.
func mustUnknown(t *testing.T, native string) ColumnType {
	t.Helper()

	built, err := UnknownType(native)
	if err != nil {
		t.Fatalf("UnknownType(%q): %v", native, err)
	}

	return built
}

func TestTheThreeCertaintiesAreTheThreeHonestAnswers(t *testing.T) {
	exact := mustExact(t, "int32", "integer")
	ambiguous := mustAmbiguous(t, "timestamp", "datetime(6)")
	unknown := mustUnknown(t, "tsvector")

	for _, c := range []struct {
		name      string
		built     ColumnType
		certainty Certainty
		exact     bool
		logical   string
		rendered  string
	}{
		{"exact", exact, CertaintyExact, true, "int32", "int32"},
		{"ambiguous", ambiguous, CertaintyAmbiguous, false, "timestamp", "timestamp? (datetime(6))"},
		{"unknown", unknown, CertaintyUnknown, false, "<invalid>", "unknown (tsvector)"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.built.Certainty() != c.certainty {
				t.Errorf("Certainty() = %q, want %q", c.built.Certainty(), c.certainty)
			}

			if c.built.Exact() != c.exact {
				t.Errorf("Exact() = %v, want %v", c.built.Exact(), c.exact)
			}

			logical, reported := c.built.Logical()
			if reported != c.exact {
				t.Errorf("Logical() reported exactness %v, want %v", reported, c.exact)
			}

			if logical.String() != c.logical {
				t.Errorf("Logical() = %s, want %s", logical, c.logical)
			}

			if c.built.String() != c.rendered {
				t.Errorf("String() = %q, want %q", c.built.String(), c.rendered)
			}
		})
	}
}

func TestEveryColumnTypeRecordsTheEnginesOwnSpelling(t *testing.T) {
	for _, c := range []struct {
		name   string
		built  func() (ColumnType, error)
		native string
	}{
		{"exact", func() (ColumnType, error) {
			return ExactType(mustParse(t, "string(255)"), "character varying(255)")
		}, "character varying(255)"},
		{"ambiguous", func() (ColumnType, error) {
			return AmbiguousType(mustParse(t, "int64"), "INTEGER")
		}, "INTEGER"},
		{"unknown", func() (ColumnType, error) { return UnknownType("set('a','b')") },
			"set('a','b')"},
	} {
		t.Run(c.name, func(t *testing.T) {
			built, err := c.built()
			if err != nil {
				t.Fatalf("building it: %v", err)
			}

			if built.Native() != c.native {
				t.Errorf("Native() = %q, want %q", built.Native(), c.native)
			}
		})
	}
}

func TestAColumnTypeNoConstructorCouldHaveProducedIsRefused(t *testing.T) {
	for _, c := range []struct {
		name  string
		built func() (ColumnType, error)
		want  string
	}{
		{"an exact type with no logical type", func() (ColumnType, error) {
			return ExactType(Type{}, "integer")
		}, "carries no logical type"},
		{"an ambiguous type with no logical type", func() (ColumnType, error) {
			return AmbiguousType(Type{}, "datetime(6)")
		}, "carries no logical type"},
		{"an exact type with no native spelling", func() (ColumnType, error) {
			return ExactType(mustParse(t, "int32"), "  ")
		}, "names no native spelling"},
		{"an unknown type with no native spelling", func() (ColumnType, error) {
			return UnknownType("")
		}, "names no native spelling"},
	} {
		t.Run(c.name, func(t *testing.T) {
			built, err := c.built()
			if err == nil {
				t.Fatalf("building it succeeded, producing %s", built)
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}

func TestAZeroColumnTypeIsNotAColumnType(t *testing.T) {
	var zero ColumnType

	if !zero.Zero() {
		t.Error("a zero ColumnType does not report itself zero")
	}

	if err := zero.Validate(); !errors.Is(err, ErrNoColumnType) {
		t.Errorf("Validate() = %v, want ErrNoColumnType", err)
	}

	if zero.String() != invalidType {
		t.Errorf("String() = %q, want %q", zero.String(), invalidType)
	}

	if _, err := json.Marshal(zero); err == nil {
		t.Error("a zero ColumnType encoded rather than refusing")
	}
}

func TestAnUnknownTypeMayNotCarryALogicalTypeAnyway(t *testing.T) {
	smuggled := ColumnType{logical: mustParse(t, "int32"), native: "tsvector",
		certainty: CertaintyUnknown}

	err := smuggled.Validate()
	if err == nil {
		t.Fatal("an unknown type carrying a logical type validated")
	}

	if !strings.Contains(err.Error(), "a claim it cannot make") {
		t.Errorf("error %q does not say the claim is the problem", err)
	}
}

func TestAnUnrecognizedCertaintyIsRefusedRatherThanCompared(t *testing.T) {
	invented := ColumnType{logical: mustParse(t, "int32"), native: "integer",
		certainty: Certainty("probably")}

	err := invented.Validate()
	if err == nil {
		t.Fatal("an invented certainty validated")
	}

	for _, phrase := range []string{"probably", string(CertaintyExact), string(CertaintyAmbiguous),
		string(CertaintyUnknown)} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("error %q does not mention %q", err, phrase)
		}
	}

	if invented.String() != invalidType {
		t.Errorf("String() = %q, want %q", invented.String(), invalidType)
	}
}

func TestTwoExactTypesCompareOnTheLogicalTypeAloneAcrossEngines(t *testing.T) {
	postgres := mustExact(t, "int32", "integer")
	mysql := mustExact(t, "int32", "int")
	mariadb := mustExact(t, "int32", "int(11)")

	for _, other := range []ColumnType{mysql, mariadb} {
		if !postgres.Equal(other) {
			t.Errorf("%s does not equal %s, and both are exactly int32",
				postgres.Native(), other.Native())
		}
	}

	if postgres.Equal(mustExact(t, "int64", "bigint")) {
		t.Error("int32 equals int64, which no engine means")
	}
}

func TestAGuessNeverEqualsAFactEvenWhereTheLogicalTypeAgrees(t *testing.T) {
	fact := mustExact(t, "timestamp", "timestamp without time zone")
	guess := mustAmbiguous(t, "timestamp", "datetime(6)")

	if fact.Equal(guess) || guess.Equal(fact) {
		t.Error("an exact timestamp equals an ambiguous one, so a guess reads as a fact")
	}

	if !guess.Equal(mustAmbiguous(t, "timestamp", "datetime(6)")) {
		t.Error("two identical guesses from one engine do not agree")
	}

	if guess.Equal(mustAmbiguous(t, "timestamp", "DATETIME(6)")) {
		t.Error("two guesses agree across differing native spellings, which says nothing")
	}

	resolved := mustExact(t, "timestamp", "datetime(6)")
	if resolved.Equal(guess) || guess.Equal(resolved) {
		t.Error("one native spelling compares equal across two certainties, so a driver that " +
			"learned to resolve it would report no change at all")
	}
}

func TestAnUnknownTypeComparesByTheSpellingAndNothingElse(t *testing.T) {
	unknown := mustUnknown(t, "tsvector")

	if !unknown.Equal(mustUnknown(t, "tsvector")) {
		t.Error("two unknown tsvectors do not agree")
	}

	if unknown.Equal(mustUnknown(t, "inet")) {
		t.Error("an unknown tsvector equals an unknown inet")
	}

	if unknown.Equal(mustAmbiguous(t, "json", "tsvector")) {
		t.Error("an unknown type equals an ambiguous one with the same spelling")
	}
}

func TestAColumnTypeRoundTripsThroughJSONInEveryState(t *testing.T) {
	for _, built := range []ColumnType{
		mustExact(t, "string(255)", "character varying(255)"),
		mustAmbiguous(t, "timestamptz", "datetime(6)"),
		mustUnknown(t, "money"),
	} {
		t.Run(built.String(), func(t *testing.T) {
			encoded, err := json.Marshal(built)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			var decoded ColumnType

			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatalf("Unmarshal(%s): %v", encoded, err)
			}

			if !decoded.Equal(built) {
				t.Errorf("decoded %s, want %s", decoded, built)
			}

			again, err := json.Marshal(decoded)
			if err != nil {
				t.Fatalf("Marshal again: %v", err)
			}

			if string(again) != string(encoded) {
				t.Errorf("re-encoded as %s, want %s", again, encoded)
			}
		})
	}
}

func TestADecodedColumnTypeIsHeldToTheSameRulesAsABuiltOne(t *testing.T) {
	for _, c := range []struct {
		name     string
		document string
		want     string
	}{
		{"an unknown member", `{"certainty":"exact","logical":"int32","native":"int","why":"no"}`,
			"why"},
		{"a certainty nothing recognizes", `{"certainty":"maybe","native":"int"}`, "maybe"},
		{"an exact type with no logical type", `{"certainty":"exact","native":"int"}`,
			"carries no logical type"},
		{"an unknown type carrying one", `{"certainty":"unknown","logical":"int32","native":"x"}`,
			"a claim it cannot make"},
		{"no native spelling", `{"certainty":"exact","logical":"int32","native":""}`,
			"names no native spelling"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var decoded ColumnType

			err := json.Unmarshal([]byte(c.document), &decoded)
			if err == nil {
				t.Fatalf("decoding %s succeeded, producing %s", c.document, decoded)
			}

			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q does not say %q", err, c.want)
			}
		})
	}
}
