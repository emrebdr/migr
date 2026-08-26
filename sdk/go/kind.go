package migr

import "slices"

// Kind names one operation in the v1 IR vocabulary, spelled as docs/ir.md spells it.
type Kind string

// The v1 operation set, in the order docs/ir.md §7.1 lists it.
const (
	KindCreateTable    Kind = "create_table"
	KindDropTable      Kind = "drop_table"
	KindRenameTable    Kind = "rename_table"
	KindAddColumn      Kind = "add_column"
	KindDropColumn     Kind = "drop_column"
	KindRenameColumn   Kind = "rename_column"
	KindAlterColumn    Kind = "alter_column"
	KindAddIndex       Kind = "add_index"
	KindDropIndex      Kind = "drop_index"
	KindAddForeignKey  Kind = "add_foreign_key"
	KindDropForeignKey Kind = "drop_foreign_key"
	KindAddUnique      Kind = "add_unique"
	KindDropUnique     Kind = "drop_unique"
	KindAddCheck       Kind = "add_check"
	KindDropCheck      Kind = "drop_check"
	KindAddPrimaryKey  Kind = "add_primary_key"
	KindDropPrimaryKey Kind = "drop_primary_key"
	KindCreateView     Kind = "create_view"
	KindDropView       Kind = "drop_view"
	KindRaw            Kind = "raw"
)

// kinds lists every operation in the order docs/ir.md §7.1 declares them.
var kinds = []Kind{
	KindCreateTable, KindDropTable, KindRenameTable,
	KindAddColumn, KindDropColumn, KindRenameColumn, KindAlterColumn,
	KindAddIndex, KindDropIndex,
	KindAddForeignKey, KindDropForeignKey,
	KindAddUnique, KindDropUnique,
	KindAddCheck, KindDropCheck,
	KindAddPrimaryKey, KindDropPrimaryKey,
	KindCreateView, KindDropView,
	KindRaw,
}

// Kinds returns every operation kind, in declaration order.
func Kinds() []Kind {
	return slices.Clone(kinds)
}

// Valid reports whether a kind is part of the v1 operation set.
func (k Kind) Valid() bool {
	return slices.Contains(kinds, k)
}

// String returns the kind's wire name.
func (k Kind) String() string {
	return string(k)
}
