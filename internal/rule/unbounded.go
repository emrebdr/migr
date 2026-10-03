package rule

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// UnboundedString is the id of the rule that reports a column declared with no length at all.
const UnboundedString ID = "unbounded-string"

// unboundedString reports a text column, which is the logical type that declares no length.
func unboundedString() Rule {
	return Rule{
		ID:      UnboundedString,
		Default: SeverityWarn,
		Summary: "a column is declared text, which is unbounded, rather than string(n)",
		Judge:   judgeUnboundedString,
	}
}

// judgeUnboundedString reports every unbounded column a migration adds, in the order it adds them.
func judgeUnboundedString(s Subject) []Finding {
	var found []Finding

	for _, op := range s.Up.Numbered(ir.Up) {
		for _, column := range declaredIn(op.Op) {
			if column.Type.Kind() != schema.KindText {
				continue
			}

			found = append(found, op.Finding(fmt.Sprintf("column %q is %s, which declares no "+
				"length: use string(n) unless the value really is unbounded", column.Name,
				column.Type)))
		}
	}

	return found
}

// declaredIn returns the columns one operation gives a type to, which is none for most of them.
func declaredIn(op ir.Operation) []ir.Column {
	switch typed := op.(type) {
	case ir.CreateTable:
		return typed.Columns
	case ir.AddColumn:
		return []ir.Column{typed.Column}
	case ir.AlterColumn:
		return retyped(typed)
	default:
		return nil
	}
}

// retyped returns the column an alter gives a new type to, and none for an alter that changes else.
func retyped(op ir.AlterColumn) []ir.Column {
	if op.Type == nil {
		return nil
	}

	return []ir.Column{{Name: op.Column, Type: *op.Type}}
}
