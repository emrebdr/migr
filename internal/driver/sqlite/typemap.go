package sqlite

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// nativeType returns SQLite's declared type for a logical one, and nothing when it has none.
func nativeType(t schema.Type) string {
	switch t.Kind() {
	case schema.KindString:
		length, _ := t.Length()

		return fmt.Sprintf("varchar(%d)", length)
	case schema.KindDecimal:
		precision, _ := t.Precision()
		scale, _ := t.Scale()

		return fmt.Sprintf("decimal(%d,%d)", precision, scale)
	case schema.KindText, schema.KindJSON, schema.KindEnum:
		return "text"
	case schema.KindBool:
		return "boolean"
	case schema.KindInt16, schema.KindInt32, schema.KindInt64:
		return "integer"
	case schema.KindFloat32, schema.KindFloat64:
		return "real"
	case schema.KindUUID:
		return "uuid"
	case schema.KindTimestamp:
		return "timestamp"
	case schema.KindTimestampTZ:
		return "timestamptz"
	case schema.KindDate:
		return "date"
	case schema.KindTime:
		return "time"
	case schema.KindBinary:
		return "blob"
	case schema.KindArray:
		return ""
	default:
		return ""
	}
}

// nativeAction returns SQLite's spelling of a referential action, and nothing when unset.
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
		return "SET DEFAULT"
	default:
		return ""
	}
}

// nativeFunction returns SQLite's spelling of a neutral default function.
func nativeFunction(f ir.Function) string {
	switch f {
	case ir.FuncCurrentTimestamp:
		return "CURRENT_TIMESTAMP"
	case ir.FuncCurrentDate:
		return "CURRENT_DATE"
	case ir.FuncCurrentTime:
		return "CURRENT_TIME"
	case ir.FuncUUID:
		return ""
	default:
		return ""
	}
}
