package mysql

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// fsp is the fractional-seconds precision every temporal type this driver emits carries.
//
// Six digits is MySQL's maximum and it is not decoration: a datetime with no fsp keeps whole seconds
// and DEFAULT CURRENT_TIMESTAMP is refused outright unless the two agree -- error 1067, measured on
// 8.0.46, where MariaDB accepts the mismatch. Naming it once means the column type and the default
// function cannot disagree.
const fsp = 6

// uuidCharacters is how wide the char a uuid is stored in is, stated once so a key can be measured.
const uuidCharacters = 36

// nativeType returns MySQL's declared type for a logical one, and nothing when it has none.
func nativeType(t schema.Type) string {
	switch t.Kind() {
	case schema.KindString:
		length, _ := t.Length()

		return fmt.Sprintf("varchar(%d)", length)
	case schema.KindDecimal:
		precision, _ := t.Precision()
		scale, _ := t.Scale()

		return fmt.Sprintf("decimal(%d,%d)", precision, scale)
	case schema.KindText:
		return "longtext"
	case schema.KindBool:
		return "tinyint(1)"
	case schema.KindInt16:
		return "smallint"
	case schema.KindInt32:
		return "int"
	case schema.KindInt64:
		return "bigint"
	case schema.KindFloat32:
		return "float"
	case schema.KindFloat64:
		return "double"
	case schema.KindUUID:
		return fmt.Sprintf("char(%d)", uuidCharacters)
	case schema.KindJSON:
		return "json"
	case schema.KindTimestamp, schema.KindTimestampTZ:
		return fmt.Sprintf("datetime(%d)", fsp)
	case schema.KindDate:
		return "date"
	case schema.KindTime:
		return fmt.Sprintf("time(%d)", fsp)
	case schema.KindBinary:
		return "longblob"
	case schema.KindEnum:
		values, _ := t.Values()

		return "enum(" + literalList(values) + ")"
	case schema.KindArray:
		return ""
	default:
		return ""
	}
}

// needsExpressionDefault reports a column type MySQL takes no bare literal default for (doc.go).
func needsExpressionDefault(t schema.Type) bool {
	switch t.Kind() {
	case schema.KindText, schema.KindBinary, schema.KindJSON:
		return true
	default:
		return false
	}
}

// nativeFunction returns MySQL's spelling of a neutral default function, and nothing when it has none.
func nativeFunction(f ir.Function) string {
	switch f {
	case ir.FuncCurrentTimestamp:
		return fmt.Sprintf("CURRENT_TIMESTAMP(%d)", fsp)
	case ir.FuncCurrentDate:
		return "(CURDATE())"
	case ir.FuncCurrentTime:
		return fmt.Sprintf("(CURTIME(%d))", fsp)
	case ir.FuncUUID:
		return "(UUID())"
	default:
		return ""
	}
}

// expressionFunction reports a default function MySQL takes only as a parenthesised expression.
func expressionFunction(f ir.Function) bool {
	switch f {
	case ir.FuncCurrentDate, ir.FuncCurrentTime, ir.FuncUUID:
		return true
	case ir.FuncCurrentTimestamp:
		return false
	default:
		return false
	}
}

// nativeAction returns MySQL's spelling of a referential action, and nothing when it has none.
func nativeAction(a ir.ReferentialAction) string {
	switch a {
	case ir.ActionNoAction:
		return "NO ACTION"
	case ir.ActionRestrict:
		return "RESTRICT"
	case ir.ActionCascade:
		return "CASCADE"
	case ir.ActionSetNull:
		return "SET NULL"
	case ir.ActionSetDefault:
		return ""
	default:
		return ""
	}
}
