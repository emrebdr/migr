package postgres

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// nativeType returns PostgreSQL's spelling of a logical type, and nothing when it has none.
func nativeType(t schema.Type) string {
	switch t.Kind() {
	case schema.KindString:
		length, _ := t.Length()

		return fmt.Sprintf("varchar(%d)", length)
	case schema.KindDecimal:
		precision, _ := t.Precision()
		scale, _ := t.Scale()

		return fmt.Sprintf("numeric(%d,%d)", precision, scale)
	case schema.KindArray:
		return nativeArray(t)
	case schema.KindText:
		return "text"
	case schema.KindBool:
		return "boolean"
	case schema.KindInt16:
		return "smallint"
	case schema.KindInt32:
		return "integer"
	case schema.KindInt64:
		return "bigint"
	case schema.KindFloat32:
		return "real"
	case schema.KindFloat64:
		return "double precision"
	case schema.KindUUID:
		return "uuid"
	case schema.KindJSON:
		return "jsonb"
	case schema.KindTimestamp:
		return "timestamp"
	case schema.KindTimestampTZ:
		return "timestamptz"
	case schema.KindDate:
		return "date"
	case schema.KindTime:
		return "time"
	case schema.KindBinary:
		return "bytea"
	case schema.KindEnum:
		return ""
	default:
		return ""
	}
}

// nativeArray returns the array spelling, and nothing for an element PostgreSQL cannot carry.
func nativeArray(t schema.Type) string {
	elem, declared := t.Elem()
	if !declared || elem.Kind() == schema.KindArray {
		return ""
	}

	native := nativeType(elem)
	if native == "" {
		return ""
	}

	return native + "[]"
}

// nativeFunction returns PostgreSQL's spelling of a neutral default function.
func nativeFunction(f ir.Function) string {
	switch f {
	case ir.FuncCurrentTimestamp:
		return "CURRENT_TIMESTAMP"
	case ir.FuncCurrentDate:
		return "CURRENT_DATE"
	case ir.FuncCurrentTime:
		return "CURRENT_TIME"
	case ir.FuncUUID:
		return "gen_random_uuid()"
	default:
		return ""
	}
}

// nativeAction returns PostgreSQL's spelling of a referential action, and nothing when unset.
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
