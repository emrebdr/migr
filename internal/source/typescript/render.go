package typescript

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/schema"
	"github.com/emrebdr/migr/internal/source"
)

// The same chains the Go writer renders, spelled the way this package's SDK spells them: the methods
// recase to camelCase, the type constructors hang off t rather off the package, a payload is an
// object literal rather than a struct, and the terminal call is awaited rather than returned. The
// two vocabularies need no table at all here -- this SDK types both as string unions, so a word is
// written as itself in quotes.

// A TypeScript migration is a module with a default export, so nothing here needs a unique
// identifier the way the Go writer does -- which is the module system replacing the registry
// (ADR 0042) showing up one more time.

// Render writes one migration's operations as the whole TypeScript file that carries them.
func Render(m migration.Migration, ops ir.Operations) (string, error) {
	body, err := statements(ops)
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(generated, SDK, m.Name, body), nil
}

// generated is the file a diff writes: the import, a default export, and an up and nothing else.
const generated = `import { defineMigration, t } from "%s";

/**
 * What migr diff found between the database and the migrations (%s). Read what it does before
 * running it.
 *
 * There is deliberately no down: every operation here records what it replaced, so migr derives
 * the inverse from what up emits. Writing one by hand is still allowed and is always preferred.
 */
export default defineMigration({
  async up(s) {
%s  },
});
`

// statements renders the operations as the body of an up, which is what the file is built around.
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

		statement, err := render(chain)
		if err != nil {
			return "", err
		}

		body.WriteString(statement)
	}

	return body.String(), nil
}

// A chain built and never awaited is the one failure this SDK has that Go's does not (ADR 0043), so
// every statement is awaited -- which is also what the template writes.

// render writes one chain as the awaited statement it is.
func render(chain source.Chain) (string, error) {
	links, err := written(chain)
	if err != nil {
		return "", err
	}

	return "    await s." + links + "\n      .apply();\n", nil
}

// written renders a chain's root and its links as the dotted call sequence they are.
func written(chain source.Chain) (string, error) {
	var body strings.Builder

	for i, one := range append([]source.Call{chain.Root}, chain.Links...) {
		rendered, err := call(one)
		if err != nil {
			return "", err
		}

		switch {
		case i == 0:
			body.WriteString(rendered)
		case qualifier(one.Method):
			body.WriteString("." + rendered)
		default:
			body.WriteString("\n      ." + rendered)
		}
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
		rendered, err := argument(arg, variadic(one.Method))
		if err != nil {
			return "", err
		}

		args = append(args, rendered)
	}

	return lower(one.Method) + "(" + strings.Join(args, ", ") + ")", nil
}

// variadic reports whether a call takes its column list spread rather than as an array.
func variadic(method string) bool {
	switch method {
	case "AddIndex", "AddUnique", "AddPrimaryKey", "PrimaryKey":
		return true
	default:
		return false
	}
}

// argument renders one argument of a call, in the TypeScript the SDK's own signature takes.
func argument(arg source.Arg, spread bool) (string, error) {
	switch held := arg.(type) {
	case source.Name:
		return quoted(string(held)), nil
	case source.Literal:
		return quoted(string(held)), nil
	case source.Names:
		return names(held, spread), nil
	case source.Logical:
		return source.Type(schema.Type(held), spelling)
	case source.Function:
		return quoted(string(held)), nil
	case source.Action:
		return quoted(string(held)), nil
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

// names renders a column list, spread for a variadic call and as an array literal otherwise.
func names(held source.Names, spread bool) string {
	written := make([]string, 0, len(held))
	for _, one := range held {
		written = append(written, quoted(one))
	}

	if spread {
		return strings.Join(written, ", ")
	}

	return "[" + strings.Join(written, ", ") + "]"
}

// A payload is an object literal rather than a helper call, which is what the interfaces this SDK
// declares take -- so a rendered migration imports nothing but what the template already imports.

// record renders one of the payload shapes as the object literal its interface declares.
func record(held source.Record) (string, error) {
	fields := make([]string, 0, len(held.Fields))

	for _, field := range held.Fields {
		rendered, err := argument(field.Value, false)
		if err != nil {
			return "", err
		}

		fields = append(fields, snake(field.Member)+": "+rendered)
	}

	return "{ " + strings.Join(fields, ", ") + " }", nil
}

// list renders a sequence of records as the array literal a recorded table definition holds.
func list(held source.List) (string, error) {
	if len(held) == 0 {
		return "[]", nil
	}

	written := make([]string, 0, len(held))

	for _, one := range held {
		rendered, err := record(one)
		if err != nil {
			return "", err
		}

		written = append(written, "\n        "+rendered)
	}

	return "[" + strings.Join(written, ",") + ",\n      ]", nil
}

// quoted renders a string as a TypeScript literal, which JSON's own escaping is a subset of.
func quoted(held string) string {
	encoded, err := json.Marshal(held)
	if err != nil {
		return strconv.Quote(held)
	}

	return string(encoded)
}

// A method recases to camelCase and a payload member to snake_case, which is not an inconsistency
// in this SDK: a method is TypeScript's own surface, while a payload is the wire shape typed, so
// ForeignKeyDef declares references_table exactly as docs/ir.md names it. Go's struct tags make the
// same distinction invisibly, which is why only this writer needs both.

// snake recases a neutral member name as the wire spelling this SDK's payload interfaces declare.
func snake(held string) string {
	var written strings.Builder

	for i := range len(held) {
		if isUpper(held[i]) && i > 0 && !isUpper(held[i-1]) {
			written.WriteByte('_')
		}

		written.WriteByte(toLower(held[i]))
	}

	return written.String()
}

// toLower lowers one byte, which is all these names are made of.
func toLower(c byte) byte {
	if isUpper(c) {
		return c - 'A' + 'a'
	}

	return c
}

// lower recases a neutral method or member name as this SDK's camelCase spelling of it.
func lower(held string) string {
	if held == "" {
		return held
	}

	cut := 1
	for cut < len(held) && isUpper(held[cut]) && (cut+1 == len(held) || isUpper(held[cut+1])) {
		cut++
	}

	return strings.ToLower(held[:cut]) + held[cut:]
}

// isUpper reports whether a byte is an upper-case letter, which is what an initialism is made of.
func isUpper(c byte) bool {
	return c >= 'A' && c <= 'Z'
}

// spelling is how TypeScript writes the parts of a logical type that are not the shape of it.
var spelling = source.Spelling{
	Prefix: "t.",
	Quote:  quoted,
	Names: map[schema.Kind]string{
		schema.KindString:      "string",
		schema.KindText:        "text",
		schema.KindBool:        "bool",
		schema.KindInt16:       "int16",
		schema.KindInt32:       "int32",
		schema.KindInt64:       "int64",
		schema.KindFloat32:     "float32",
		schema.KindFloat64:     "float64",
		schema.KindDecimal:     "decimal",
		schema.KindUUID:        "uuid",
		schema.KindJSON:        "json",
		schema.KindTimestamp:   "timestamp",
		schema.KindTimestampTZ: "timestamptz",
		schema.KindDate:        "date",
		schema.KindTime:        "time",
		schema.KindBinary:      "binary",
		schema.KindEnum:        "enum",
		schema.KindArray:       "array",
	},
}
