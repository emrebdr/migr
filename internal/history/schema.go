package history

import (
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// versionIndex is the suffix the lookup index carries, so its name follows the table's.
const versionIndex = "_version_idx"

// Schema returns the operations that create the history table under a given name.
func Schema(table string) ir.Operations {
	return ir.Operations{
		ir.CreateTable{
			Table:      table,
			Columns:    columns(),
			PrimaryKey: []string{"batch", "ordinal"},
		},
		ir.AddIndex{
			Table:   table,
			Name:    table + versionIndex,
			Columns: []string{"version"},
		},
	}
}

// columns returns one column per Record field, in the order the record declares them.
func columns() []ir.Column {
	return []ir.Column{
		{Name: "batch", Type: typeOf("int64")},
		{Name: "ordinal", Type: typeOf("int32")},
		{Name: "version", Type: typeOf("string(64)")},
		{Name: "name", Type: typeOf("string(255)")},
		{Name: "direction", Type: typeOf("string(8)")},
		{Name: "outcome", Type: typeOf("string(16)")},
		{Name: "checksum", Type: typeOf("string(255)")},
		{Name: "applied_at", Type: typeOf("timestamptz")},
		{Name: "duration_ms", Type: typeOf("int64")},
		{Name: "last_statement", Type: typeOf("int32")},
		{Name: "error", Type: typeOf("text"), Nullable: true},
		{Name: "driver", Type: typeOf("string(64)")},
		{Name: "runtime", Type: typeOf("string(32)")},
		{Name: "engine_version", Type: typeOf("string(64)")},
		{Name: "ir_version", Type: typeOf("int32")},
	}
}

// typeOf returns a logical type by its canonical spelling, or the zero Type if it is misspelled.
func typeOf(spelling string) schema.Type {
	typ, _ := schema.Parse(spelling)

	return typ
}
