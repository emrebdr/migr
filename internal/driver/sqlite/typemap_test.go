package sqlite

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// mustType parses a canonical logical type, failing the test rather than compiling a zero one.
func mustType(t *testing.T, spelling string) schema.Type {
	t.Helper()

	typ, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	return typ
}

func TestEveryLogicalTypeIsMappedOrRefused(t *testing.T) {
	native := map[schema.Kind]string{
		schema.KindString:      "varchar(255)",
		schema.KindText:        "text",
		schema.KindBool:        "boolean",
		schema.KindInt16:       "integer",
		schema.KindInt32:       "integer",
		schema.KindInt64:       "integer",
		schema.KindFloat32:     "real",
		schema.KindFloat64:     "real",
		schema.KindDecimal:     "decimal(10,2)",
		schema.KindUUID:        "uuid",
		schema.KindJSON:        "text",
		schema.KindTimestamp:   "timestamp",
		schema.KindTimestampTZ: "timestamptz",
		schema.KindDate:        "date",
		schema.KindTime:        "time",
		schema.KindBinary:      "blob",
		schema.KindEnum:        "text",
		schema.KindArray:       "",
	}

	for _, kind := range schema.Kinds() {
		t.Run(string(kind), func(t *testing.T) {
			want, listed := native[kind]
			if !listed {
				t.Fatalf("%s is a logical type this driver's table says nothing about", kind)
			}

			typ := mustType(t, sampleSpelling(kind))

			if got := nativeType(typ); got != want {
				t.Errorf("nativeType(%s) = %q, want %q", typ, got, want)
			}

			if (checkType("column", typ) == "") != (want != "") {
				t.Errorf("%s maps to %q but its refusal reads %q", kind, want,
					checkType("column", typ))
			}
		})
	}

	t.Log("This test walks schema.Kinds() rather than the driver's own table, so a nineteenth")
	t.Log("logical type fails here instead of compiling to an empty declared type.")
}

// sampleSpelling returns one canonical spelling of a kind, parameterized where the kind requires it.
func sampleSpelling(kind schema.Kind) string {
	switch kind {
	case schema.KindString:
		return "string(255)"
	case schema.KindDecimal:
		return "decimal(10,2)"
	case schema.KindEnum:
		return "enum(draft,live)"
	case schema.KindArray:
		return "array(int32)"
	default:
		return string(kind)
	}
}

func TestOneIntegerTypeCarriesAllThreeWidths(t *testing.T) {
	for _, spelling := range []string{"int16", "int32", "int64"} {
		if got := nativeType(mustType(t, spelling)); got != "integer" {
			t.Errorf("nativeType(%s) = %q, want integer", spelling, got)
		}
	}

	t.Log("SQLite has one integer type and enforces no width, so smallint and bigint would be")
	t.Log("claims it does not keep. They would also cost the rowid alias: INTEGER PRIMARY KEY is")
	t.Log("the only spelling SQLite treats as one, and a bigint primary key will hold text.")
}

func TestOneFloatTypeCarriesBothWidths(t *testing.T) {
	for _, spelling := range []string{"float32", "float64"} {
		if got := nativeType(mustType(t, spelling)); got != "real" {
			t.Errorf("nativeType(%s) = %q, want real", spelling, got)
		}
	}
}

func TestJSONIsDeclaredTextSoAffinityCannotConvertIt(t *testing.T) {
	if got := nativeType(mustType(t, "json")); got != "text" {
		t.Errorf("json maps to %q, want text", got)
		t.Log("A declared type named json carries no affinity marker, so SQLite gives it NUMERIC")
		t.Log("affinity -- and a JSON document that is a bare number would be converted on the way")
		t.Log("in. This is the one row where affinity beats preserving the logical name.")
	}
}

func TestAnEnumIsTextHeldToItsLabels(t *testing.T) {
	typ := mustType(t, "enum(draft,live,archived)")

	if problem := checkType("column \"status\"", typ); problem != "" {
		t.Fatalf("an enum column was refused: %s", problem)
	}

	definition := columnDefinition(ir.Column{Name: "status", Type: typ})

	want := `"status" text NOT NULL CHECK ("status" IN ('draft', 'live', 'archived'))`
	if definition != want {
		t.Errorf("an enum column renders as\n  %s\nwant\n  %s", definition, want)
	}

	t.Log("PostgreSQL refuses an enum because a CREATE TYPE object leaks. SQLite's")
	t.Log("check is part of the column, so nothing leaks and no IR operation needs to name it --")
	t.Log("this engine supports a type the other cannot. What is lost is declared label ordering,")
	t.Log("which docs/ir.md 5.3 says is part of the type: ORDER BY here is lexicographic.")
}

func TestAnEnumDefaultThatIsNotALabelIsRefused(t *testing.T) {
	typ := mustType(t, "enum(draft,live)")

	if problem := checkColumn(ir.Column{Name: "status", Type: typ, Default: ir.Literal("draft")}); problem != "" {
		t.Fatalf("a default that is a label was refused: %s", problem)
	}

	problem := checkColumn(ir.Column{Name: "status", Type: typ, Default: ir.Literal("nope")})
	if problem == "" {
		t.Fatal("a default that is not one of the labels was accepted")
	}

	if !strings.Contains(problem, "nope") {
		t.Errorf("the refusal does not name the default: %s", problem)
	}

	t.Log("This one is a payload refusal like PostgreSQL's and the ADD COLUMN rules are not:")
	t.Log("no table contents can make a default that is not a label correct. SQLite does refuse")
	t.Log("it -- at apply, or at the first insert that relies on the default -- and migr validate")
	t.Log("opens no database, so catching it here is a whole deploy earlier.")
}

func TestAnArrayIsRefusedBecauseSQLiteHasNoList(t *testing.T) {
	array := mustType(t, "array(string(64))")

	problem := checkType("column \"tags\"", array)
	if problem == "" {
		t.Fatal("an array column compiled")
	}

	if !strings.Contains(problem, "storage classes") {
		t.Errorf("the refusal does not say why SQLite has no array: %q", problem)
		t.Log("The type table refuses an array by having no spelling for it, and the predicate")
		t.Log("refuses it by name. Both are asserted because only the predicate's message says")
		t.Log("anything an operator can act on, and the table's half fires only if it is removed.")
	}

	if got := nativeType(array); got != "" {
		t.Errorf("nativeType(%s) = %q, want no spelling at all", array, got)
	}

	if strings.Contains(problem, "M8-") {
		t.Errorf("the refusal %q names a task, and no M8 task will fix this", problem)
		t.Log("Supporting arrays is an IR or schema question rather than a driver one, so naming")
		t.Log("a milestone task would promise work nobody has scheduled.")
	}
}

func TestAZeroTypeIsRefusedRatherThanRenderedEmpty(t *testing.T) {
	if checkType("column", schema.Type{}) == "" {
		t.Error("a zero Type compiled")
		t.Log("Compile does not validate -- the golden harness and the conformance suite do -- so")
		t.Log("a column with no type would otherwise render as a name with nothing after it.")
	}
}

func TestEveryDefaultFunctionIsSpelledOrRefused(t *testing.T) {
	spellings := map[ir.Function]string{
		ir.FuncCurrentTimestamp: "CURRENT_TIMESTAMP",
		ir.FuncCurrentDate:      "CURRENT_DATE",
		ir.FuncCurrentTime:      "CURRENT_TIME",
		ir.FuncUUID:             "",
	}

	for function, want := range spellings {
		if got := nativeFunction(function); got != want {
			t.Errorf("nativeFunction(%s) = %q, want %q", function, got, want)
		}
	}

	if checkDefault("column", &ir.Default{Function: "now()"}) == "" {
		t.Error("a default function this driver has no spelling for was accepted")
		t.Log("The set is closed, and an unrecognized one would render DEFAULT with nothing")
		t.Log("after it -- a statement that parses as something else entirely.")
	}
}

func TestTheUUIDDefaultIsRefusedBecauseSQLiteLinksNoSuchFunction(t *testing.T) {
	problem := checkDefault("column \"id\"", ir.Call(ir.FuncUUID))
	if problem == "" {
		t.Fatal("a uuid() default was accepted")
	}

	if !strings.Contains(problem, "randomblob") {
		t.Errorf("the refusal does not say what was rejected in its place: %s", problem)
		t.Log("The hex(randomblob()) idiom is the obvious workaround, so the refusal names it and")
		t.Log("says why it is not taken: it writes a generator into the schema rather than naming")
		t.Log("one, which is the clever emulation CLAUDE.md 11 says to refuse honestly instead.")
	}
}
