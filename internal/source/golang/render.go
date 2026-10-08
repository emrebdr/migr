package golang

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source"
)

// The Go and TypeScript writers differ only in how something is written down -- the method names
// recase, the type constructors carry a prefix rather than a receiver, a record is a struct literal
// rather than an object, and the terminal call returns rather than awaits. The chain itself is
// internal/source's, so adding a third language is a writer and not a second renderer.

// A generated file declares a type, and every Go migration in a project shares one package -- so the
// identifier carries the version discovery proved unique rather than the name, which two generated
// migrations default to the same value of. The doc comment says to rename it, as the template's does.

// Render writes one migration's operations as the whole Go file that carries them.
func Render(m migration.Migration, ops ir.Operations) (string, error) {
	body, err := statements(ops)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(generated, SDK, identifier(m), body), nil
}

// generated is the file a diff writes: the preamble, a named migration, and an Up and nothing else.
const generated = `package migrations

import (
	"context"

	migr "%[1]s"
)

func init() { migr.Register(%[2]s{}) }

// %[2]s is what migr diff found between the database and the migrations.
//
// Rename it to something the next reader will recognize, and read what it does before you run it.
type %[2]s struct{}

// Up closes the gap migr diff found, in the order the operations have to run in.
//
// There is deliberately no Down: every operation here records what it replaced, so migr derives
// the inverse from what Up emits. Writing one by hand is still allowed and is always preferred.
func (m %[2]s) Up(ctx context.Context, s *migr.Schema) error {
%[3]s}
`

// identifier names the type a generated file declares, from the version discovery proved unique.
func identifier(m migration.Migration) string {
	return "Migration" + m.Version
}

// statements renders the operations as the body of an Up, which is what the file is built around.
func statements(ops ir.Operations) (string, error) {
	chains, err := source.Chains(ops)
	if err != nil {
		return "", err
	}

	var body strings.Builder

	for i, chain := range chains {
		if i > 0 {
			body.WriteString("\n")
		}

		statement, err := render(chain, i == len(chains)-1)
		if err != nil {
			return "", err
		}

		body.WriteString(statement)
	}

	return body.String(), nil
}

// A chain that is never applied emits nothing at all, and migr fails such a migration rather than
// reporting a change it never made (ADR 0038, A). So every statement but the last checks its error
// and returns it, and the last one returns the apply itself -- which is how an author writes it.

// render writes one chain, returning its result where it is the last of them.
func render(chain source.Chain, last bool) (string, error) {
	links, err := written(chain)
	if err != nil {
		return "", err
	}

	applied := "s." + links + ".\n\t\tApply(ctx)"

	if last {
		return "\treturn " + applied + "\n", nil
	}

	return "\tif err := " + applied + "; err != nil {\n\t\treturn err\n\t}\n", nil
}

// A qualifier stays on the line of the call it qualifies, because that is where an author puts it:
// the SDK's own template writes Column("nickname", migr.Text()).Null() on one line, and a chain
// that broke every link would read as a list of unrelated calls rather than as one column.

// written renders a chain's root and its links as the dotted call sequence they are.
func written(chain source.Chain) (string, error) {
	var body strings.Builder

	for i, one := range append([]source.Call{chain.Root}, chain.Links...) {
		written, err := call(one)
		if err != nil {
			return "", err
		}

		if i > 0 && !qualifier(one.Method) {
			body.WriteString(".\n\t\t")
		} else if i > 0 {
			body.WriteString(".")
		}

		body.WriteString(written)
	}

	return body.String(), nil
}

// qualifier reports a call that says more about the one before it rather than doing its own thing.
func qualifier(method string) bool {
	switch method {
	case "Null", "NotNull", "Default", "DropDefault", "Type":
		return true
	default:
		return false
	}
}

// call renders one method call, recased and with its arguments written out.
func call(one source.Call) (string, error) {
	args := make([]string, 0, len(one.Args))

	for _, arg := range one.Args {
		written, err := argument(arg, variadic(one.Method))
		if err != nil {
			return "", err
		}

		args = append(args, written)
	}

	return one.Method + "(" + strings.Join(args, ", ") + ")", nil
}

// variadic reports whether a call takes its column list spread rather than as a slice.
func variadic(method string) bool {
	switch method {
	case "AddIndex", "AddUnique", "AddPrimaryKey", "PrimaryKey":
		return true
	default:
		return false
	}
}

// argument renders one argument of a call, in the Go the SDK's own signature takes.
func argument(arg source.Arg, spread bool) (string, error) {
	switch held := arg.(type) {
	case source.Name:
		return quoted(string(held)), nil
	case source.Literal:
		return quoted(string(held)), nil
	case source.Names:
		return names(held, spread), nil
	case source.Logical:
		return logical(held)
	case source.Function:
		return named(functions, string(held), "default function")
	case source.Action:
		return named(actions, string(held), "referential action")
	case source.Flag:
		return strconv.FormatBool(bool(held)), nil
	case source.Record:
		return record(held)
	case source.List:
		return list(held)
	default:
		return "", fmt.Errorf("nothing here writes a %T", arg)
	}
}

// names renders a column list, spread for a variadic call and as a slice literal otherwise.
func names(held source.Names, spread bool) string {
	quoted := make([]string, 0, len(held))
	for _, one := range held {
		quoted = append(quoted, strconv.Quote(one))
	}

	if spread {
		return strings.Join(quoted, ", ")
	}

	return "[]string{" + strings.Join(quoted, ", ") + "}"
}

// record renders one of the payload shapes as the struct literal the SDK declares.
func record(held source.Record) (string, error) {
	name, known := structs[held.Shape]
	if !known {
		return "", fmt.Errorf("nothing here writes a %s payload", held.Shape)
	}

	if held.Shape == source.ShapeDefault {
		return defaultCall(held)
	}

	fields := make([]string, 0, len(held.Fields))

	for _, field := range held.Fields {
		written, err := argument(field.Value, false)
		if err != nil {
			return "", err
		}

		fields = append(fields, field.Member+": "+written)
	}

	return "migr." + name + "{" + strings.Join(fields, ", ") + "}", nil
}

// A default is a constructor call rather than a struct, since migr.Literal distinguishes an empty
// string from no default at all and a bare struct literal cannot.

// defaultCall renders a default as the constructor the SDK gives it.
func defaultCall(held source.Record) (string, error) {
	field := held.Fields[0]

	written, err := argument(field.Value, false)
	if err != nil {
		return "", err
	}

	if field.Member == "Function" {
		return "migr.Call(" + written + ")", nil
	}

	return "migr.Literal(" + written + ")", nil
}

// structs names the SDK type each payload shape is written as.
var structs = map[source.Shape]string{
	source.ShapeColumn:        "Column",
	source.ShapeTableDef:      "TableDef",
	source.ShapeColumnsDef:    "ColumnsDef",
	source.ShapeCheckDef:      "CheckDef",
	source.ShapeForeignKeyDef: "ForeignKeyDef",
	source.ShapeDefault:       "Default",
}

// list renders a sequence of records as the slice literal a recorded table definition holds.
func list(held source.List) (string, error) {
	if len(held) == 0 {
		return "nil", nil
	}

	written := make([]string, 0, len(held))

	for _, one := range held {
		rendered, err := record(one)
		if err != nil {
			return "", err
		}

		written = append(written, strings.TrimPrefix(rendered, "migr."))
	}

	return "[]migr." + structs[held[0].Shape] + "{\n\t\t\t{" +
		strings.Join(trimmed(written), "},\n\t\t\t{") + "},\n\t\t}", nil
}

// trimmed strips each rendered record down to its fields, the slice literal naming the type once.
func trimmed(written []string) []string {
	fields := make([]string, 0, len(written))

	for _, one := range written {
		cut := one[strings.Index(one, "{")+1 : len(one)-1]
		fields = append(fields, cut)
	}

	return fields
}

// quoted renders a string as a Go literal, which is what every identifier and every body is.
func quoted(held string) string {
	return strconv.Quote(held)
}

// A vocabulary word is looked up rather than recased, because the SDK names uuid FuncUUID and no
// casing rule produces that. A test holds both tables to the vocabularies they spell, so a fifth
// default function fails that test rather than rendering an identifier the SDK does not declare.

// named returns the identifier the SDK declares one vocabulary word under.
func named(held map[string]string, word, vocabulary string) (string, error) {
	identifier, known := held[word]
	if !known {
		return "", fmt.Errorf("nothing here writes the %s %q", vocabulary, word)
	}

	return "migr." + identifier, nil
}

// functions names the identifier the SDK declares each neutral default function under.
var functions = map[string]string{
	string(schema.FuncCurrentTimestamp): "FuncCurrentTimestamp",
	string(schema.FuncCurrentDate):      "FuncCurrentDate",
	string(schema.FuncCurrentTime):      "FuncCurrentTime",
	string(schema.FuncUUID):             "FuncUUID",
}

// actions names the identifier the SDK declares each referential action under.
var actions = map[string]string{
	string(schema.ActionNoAction):   "ActionNoAction",
	string(schema.ActionRestrict):   "ActionRestrict",
	string(schema.ActionCascade):    "ActionCascade",
	string(schema.ActionSetNull):    "ActionSetNull",
	string(schema.ActionSetDefault): "ActionSetDefault",
}

// logical renders one logical type as the SDK constructor that builds it.
func logical(held source.Logical) (string, error) {
	return source.Type(schema.Type(held), spelling)
}

// spelling is how Go writes the parts of a logical type that are not the shape of it.
var spelling = source.Spelling{
	Prefix: "migr.",
	Quote:  quoted,
	Names: map[schema.Kind]string{
		schema.KindString:      "String",
		schema.KindText:        "Text",
		schema.KindBool:        "Bool",
		schema.KindInt16:       "Int16",
		schema.KindInt32:       "Int32",
		schema.KindInt64:       "Int64",
		schema.KindFloat32:     "Float32",
		schema.KindFloat64:     "Float64",
		schema.KindDecimal:     "Decimal",
		schema.KindUUID:        "UUID",
		schema.KindJSON:        "JSON",
		schema.KindTimestamp:   "Timestamp",
		schema.KindTimestampTZ: "TimestampTZ",
		schema.KindDate:        "Date",
		schema.KindTime:        "Time",
		schema.KindBinary:      "Binary",
		schema.KindEnum:        "Enum",
		schema.KindArray:       "Array",
	},
}
