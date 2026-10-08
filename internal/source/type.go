package source

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/emrebdr/migr/internal/schema"
)

// Spelling is how one language writes the parts of a logical type that are not the shape of it.
type Spelling struct {
	// Prefix opens every type constructor: "migr." for Go, "t." for TypeScript.
	Prefix string
	// Names the constructor each logical kind is called, which is the one table that differs.
	Names map[schema.Kind]string
	// Quote renders a string literal the way the language writes one.
	Quote func(string) string
}

// Type renders one logical type as the constructor call a language's schema builder takes.
func Type(held schema.Type, spelling Spelling) (string, error) {
	name, known := spelling.Names[held.Kind()]
	if !known {
		return "", fmt.Errorf("nothing here writes the logical type %s", held.Kind())
	}

	call := spelling.Prefix + name

	switch held.Kind() {
	case schema.KindString:
		return call + "(" + parameter(held.Length()) + ")", nil
	case schema.KindDecimal:
		return call + "(" + parameter(held.Precision()) + ", " + parameter(held.Scale()) + ")", nil
	case schema.KindEnum:
		return enum(call, held, spelling)
	case schema.KindArray:
		return array(call, held, spelling)
	case schema.KindText, schema.KindBool, schema.KindInt16, schema.KindInt32, schema.KindInt64,
		schema.KindFloat32, schema.KindFloat64, schema.KindUUID, schema.KindJSON,
		schema.KindTimestamp, schema.KindTimestampTZ, schema.KindDate, schema.KindTime,
		schema.KindBinary:
		return call + "()", nil
	default:
		return "", fmt.Errorf("nothing here writes the logical type %s", held.Kind())
	}
}

// Four of the eighteen kinds take parameters and the rest stand alone, which is the whole of the
// shape a language has to know -- so a third language supplies eighteen names, a prefix and a quoter
// rather than a renderer. Coverage is derived from schema.Kinds(), so a nineteenth logical type
// fails a writer's own suite until it is named.

// parameter renders a type's integer parameter, which every parameterised kind has.
func parameter(value int, _ bool) string {
	return strconv.Itoa(value)
}

// enum renders an enumeration, whose values are string literals in the order it declares them.
func enum(call string, held schema.Type, spelling Spelling) (string, error) {
	values, _ := held.Values()

	quoted := make([]string, 0, len(values))
	for _, one := range values {
		quoted = append(quoted, spelling.Quote(one))
	}

	return call + "(" + strings.Join(quoted, ", ") + ")", nil
}

// array renders an array, whose element is a logical type of its own.
func array(call string, held schema.Type, spelling Spelling) (string, error) {
	elem, _ := held.Elem()

	written, err := Type(elem, spelling)
	if err != nil {
		return "", err
	}

	return call + "(" + written + ")", nil
}
