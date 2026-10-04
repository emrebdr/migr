package rule

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
)

// NotNullWithoutDefault is the id of the rule that reports a NOT NULL column added with no default.
const NotNullWithoutDefault ID = "not-null-without-default"

// notNullWithoutDefault reports a column added NOT NULL with nothing to fill the rows already there.
func notNullWithoutDefault() Rule {
	return Rule{
		ID:      NotNullWithoutDefault,
		Default: SeverityWarn,
		Summary: "a NOT NULL column is added to an existing table with no default to fill its rows",
		Judge:   judgeNotNullWithoutDefault,
	}
}

// judgeNotNullWithoutDefault reports every NOT NULL column added to a table this migration found.
func judgeNotNullWithoutDefault(s Subject) []Finding {
	created := createdIn(s.Up.Operations)

	var found []Finding

	for _, op := range s.Up.Numbered(ir.Up) {
		added, adding := op.Op.(ir.AddColumn)
		if !adding || added.Column.Nullable || added.Column.Default != nil || created[added.Table] {
			continue
		}

		found = append(found, op.Finding(fmt.Sprintf("column %q is added to %q as %s NOT NULL with "+
			"no default, and migr opened no database to count that table's rows: against a table "+
			"holding any, the server refuses the statement -- give the column a default, or add it "+
			"nullable, backfill it, and tighten it in a later migration", added.Column.Name,
			added.Table, added.Column.Type)))
	}

	return found
}

// createdIn names every table one half creates, which therefore holds no rows for a column to fill.
func createdIn(ops ir.Operations) map[string]bool {
	created := make(map[string]bool)

	for _, op := range ops {
		if table, creating := op.(ir.CreateTable); creating {
			created[table.Table] = true
		}
	}

	return created
}
