package postgres

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
		schema.KindInt16:       "smallint",
		schema.KindInt32:       "integer",
		schema.KindInt64:       "bigint",
		schema.KindFloat32:     "real",
		schema.KindFloat64:     "double precision",
		schema.KindDecimal:     "numeric(10,2)",
		schema.KindUUID:        "uuid",
		schema.KindJSON:        "jsonb",
		schema.KindTimestamp:   "timestamp",
		schema.KindTimestampTZ: "timestamptz",
		schema.KindDate:        "date",
		schema.KindTime:        "time",
		schema.KindBinary:      "bytea",
		schema.KindArray:       "integer[]",
		schema.KindEnum:        "",
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
	t.Log("logical type fails here instead of compiling to an empty native type. The exhaustive")
	t.Log("linter cannot carry it: .golangci.yml sets default-signifies-exhaustive.")
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

func TestJSONMapsToTheCanonicalRepresentation(t *testing.T) {
	if got := nativeType(mustType(t, "json")); got != "jsonb" {
		t.Errorf("json maps to %q", got)
		t.Log("PostgreSQL's json type stores the source text, preserving whitespace, key order,")
		t.Log("and duplicate keys -- so two equal documents are different values and no operator")
		t.Log("indexes it. A schema type should be the canonical one.")
	}
}

func TestAnEnumIsRefusedForTheObjectNobodyWouldDrop(t *testing.T) {
	problem := checkType("column \"status\"", mustType(t, "enum(draft,live)"))

	if problem == "" {
		t.Fatal("an enum column compiled")
	}

	if !strings.Contains(problem, enumTask) {
		t.Errorf("the refusal %q names no task that will fix it", problem)
	}
}

func TestANestedArrayIsRefusedBecausePostgresWouldFlattenIt(t *testing.T) {
	flat := mustType(t, "array(int32)")
	nested := mustType(t, "array(array(int32))")

	if checkType("column", flat) != "" {
		t.Fatalf("a flat array was refused: %s", checkType("column", flat))
	}

	if checkType("column", nested) == "" {
		t.Error("a nested array was declared compilable")
		t.Log("PostgreSQL accepts integer[][] and treats it as integer[]: dimensions are not")
		t.Log("enforced. Two distinct logical types would become one native type with no error.")
	}

	if got := nativeType(nested); got != "" {
		t.Errorf("nativeType(%s) = %q, want no spelling at all", nested, got)
		t.Log("The refusal and the table both refuse nesting, and both are asserted: a mutation")
		t.Log("sweep found that dropping the table's half was invisible, because the refusal")
		t.Log("fires first. Redundancy against a silent-wrong is worth keeping and worth testing.")
	}
}

func TestAnArrayOfSomethingUnmappableIsRefusedThroughItsElement(t *testing.T) {
	if checkType("column", mustType(t, "array(enum(draft,live))")) == "" {
		t.Error("an array of enums compiled")
	}
}

func TestAZeroTypeIsRefusedRatherThanRenderedEmpty(t *testing.T) {
	if checkType("column", schema.Type{}) == "" {
		t.Error("a zero Type compiled")
		t.Log("Compile does not validate -- the golden harness and the conformance suite do -- so")
		t.Log("a column with no type would otherwise render as a name with nothing after it.")
	}
}

func TestEveryDefaultFunctionHasASpelling(t *testing.T) {
	spellings := map[ir.Function]string{
		ir.FuncCurrentTimestamp: "CURRENT_TIMESTAMP",
		ir.FuncCurrentDate:      "CURRENT_DATE",
		ir.FuncCurrentTime:      "CURRENT_TIME",
		ir.FuncUUID:             "gen_random_uuid()",
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

func TestEveryReferentialActionHasASpelling(t *testing.T) {
	spellings := map[ir.ReferentialAction]string{
		ir.ActionNoAction:   "NO ACTION",
		ir.ActionRestrict:   "RESTRICT",
		ir.ActionCascade:    "CASCADE",
		ir.ActionSetNull:    "SET NULL",
		ir.ActionSetDefault: "SET DEFAULT",
	}

	for action, want := range spellings {
		if got := nativeAction(action); got != want {
			t.Errorf("nativeAction(%s) = %q, want %q", action, got, want)
		}
	}

	if checkAction("on_delete", "") != "" {
		t.Error("an unset action was refused, where it means leave the engine's behavior alone")
	}

	if checkAction("on_delete", "explode") == "" {
		t.Error("an action this driver has no spelling for was accepted")
		t.Log("An unrecognized action renders as nothing, which omits the clause -- so the key")
		t.Log("would silently get NO ACTION instead of whatever was asked for.")
	}
}
