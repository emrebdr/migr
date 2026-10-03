package ir

import (
	"slices"
	"strings"
)

// Kind names one operation in the v1 IR vocabulary.
type Kind string

// The v1 operation set. Adding one is the five-part change in CLAUDE.md invariant #4.
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

// kinds lists every operation in the order CLAUDE.md §4.1 declares them.
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

// destructiveKinds lists the operations some payload of which can lose data, in declaration order.
//
// A kind rather than a verdict, because alter_column is destructive only when it retypes and this
// is the set an operator may permit by name. Destructive() on each operation is still what decides
// one, and a test holds this list to that method over the whole fixture set -- so a twenty-first
// destructive operation fails rather than being quietly unlistable.
var destructiveKinds = []Kind{
	KindDropTable, KindDropColumn, KindAlterColumn, KindDropView, KindRaw,
}

// DestructiveKinds returns every operation kind that can lose data, in declaration order, as a copy.
func DestructiveKinds() []Kind {
	return slices.Clone(destructiveKinds)
}

// CanDestroy reports whether some payload of this kind loses data, which is not whether one does.
func (k Kind) CanDestroy() bool {
	return slices.Contains(destructiveKinds, k)
}

// Valid reports whether a kind is part of the v1 operation set.
func (k Kind) Valid() bool {
	return slices.Contains(kinds, k)
}

// String returns the kind's wire name.
func (k Kind) String() string {
	return string(k)
}

// joinKinds renders the whole operation set for an error message.
func joinKinds() string {
	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}

	return strings.Join(names, ", ")
}
