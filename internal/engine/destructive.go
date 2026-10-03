package engine

import (
	"fmt"
	"slices"
	"strings"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// Allowance is the destruction a run may perform, and nothing it does not name may happen.
type Allowance struct {
	// Kinds are the operation kinds this project permits, which migr.yaml holds and a review sees.
	Kinds []ir.Kind
	// All reports whether this one run permits every kind, which is what --allow-destructive means.
	All bool
}

// Permits reports whether this allowance covers an operation, which a harmless one needs no part of.
func (a Allowance) Permits(op ir.Operation) bool {
	if !op.Destructive() {
		return true
	}

	return a.All || slices.Contains(a.Kinds, op.Kind())
}

// Destruction is one operation that destroys something the run it belongs to may not destroy.
type Destruction struct {
	// Operation is the operation's place in the half it was emitted for, counting from one.
	Operation int32
	// Kind is the operation at fault, which is also what an allowance names to permit it.
	Kind ir.Kind
	// Subject is what it destroys, named the way the operation names it.
	Subject string
	// Detail states what would be lost, in terms the migration's author can act on.
	Detail string
}

// Describe renders one finding as a line, naming the operation, its kind, and what it destroys.
func (d Destruction) Describe() string {
	return fmt.Sprintf("operation %d (%s) %s", d.Operation, d.Kind, d.Detail)
}

// Destructive returns every operation in a list that destroys something this allowance leaves out.
func Destructive(ops ir.Operations, allowed Allowance) []Destruction {
	var (
		found []Destruction
		index int32
	)

	for _, op := range ops {
		index++

		if allowed.Permits(op) {
			continue
		}

		found = append(found, Destruction{
			Operation: index,
			Kind:      op.Kind(),
			Subject:   Subject(op),
			Detail:    detailOf(op),
		})
	}

	return found
}

// Subject names the object one operation acts on, and is total so a new kind cannot be forgotten.
func Subject(op ir.Operation) string {
	switch o := op.(type) {
	case ir.CreateTable:
		return o.Table
	case ir.DropTable:
		return o.Table
	case ir.RenameTable:
		return o.From
	case ir.AddColumn:
		return qualified(o.Table, o.Column.Name)
	case ir.DropColumn:
		return qualified(o.Table, o.Column)
	case ir.RenameColumn:
		return qualified(o.Table, o.From)
	case ir.AlterColumn:
		return qualified(o.Table, o.Column)
	case ir.AddIndex:
		return o.Name
	case ir.DropIndex:
		return o.Name
	case ir.AddForeignKey:
		return o.Name
	case ir.DropForeignKey:
		return o.Name
	case ir.AddUnique:
		return o.Name
	case ir.DropUnique:
		return o.Name
	case ir.AddCheck:
		return o.Name
	case ir.DropCheck:
		return o.Name
	case ir.AddPrimaryKey:
		return o.Table
	case ir.DropPrimaryKey:
		return o.Table
	case ir.CreateView:
		return o.View
	case ir.DropView:
		return o.View
	case ir.Raw:
		return firstLine(o.SQL)
	}

	return ""
}

// detailOf says what one destructive operation would lose, which for raw is the thing nothing knows.
func detailOf(op ir.Operation) string {
	switch o := op.(type) {
	case ir.DropTable:
		return fmt.Sprintf("drops the table %q and every row in it", o.Table)
	case ir.DropColumn:
		return fmt.Sprintf("drops the column %q and every value in it", qualified(o.Table, o.Column))
	case ir.DropView:
		return fmt.Sprintf("drops the view %q", o.View)
	case ir.AlterColumn:
		return describeRetype(o)
	case ir.Raw:
		return fmt.Sprintf("is engine SQL, and migr cannot read what it does: %s", firstLine(o.SQL))
	}

	return fmt.Sprintf("destroys something, and nothing here can say what: %s", Subject(op))
}

// describeRetype says why a column change counts, which is the type it is becoming and nothing else.
func describeRetype(op ir.AlterColumn) string {
	if op.Type == nil {
		return fmt.Sprintf("changes the column %q", qualified(op.Table, op.Column))
	}

	return fmt.Sprintf("retypes the column %q to %s, and this operation does not carry what it is "+
		"now, so whether that narrows is unknowable here", qualified(op.Table, op.Column), *op.Type)
}

// qualified names a column the way an operator reads one, since a column name alone names many.
func qualified(table, column string) string {
	return table + "." + column
}

// firstLine renders as much of a statement as a message can carry, since one may be a whole script.
func firstLine(sql string) string {
	trimmed := strings.TrimSpace(sql)

	if cut := strings.IndexByte(trimmed, '\n'); cut >= 0 {
		return strings.TrimSpace(trimmed[:cut]) + " ..."
	}

	return trimmed
}

// permit refuses a migration that destroys what this run was not allowed to destroy, applying only.
func (e *executor) permit(m migration.Migration, ops ir.Operations) error {
	if e.mode != ModeApply {
		return nil
	}

	found := Destructive(ops, e.opts.Allow)
	if len(found) == 0 {
		return nil
	}

	return &DestructiveError{
		Migration: m,
		Version:   m.Version,
		Found:     found,
		Allowed:   e.opts.Allow.Kinds,
	}
}
