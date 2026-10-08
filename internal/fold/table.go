package fold

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// createTable folds a new table onto the schema, with the key it states inline.
func (f *folder) createTable(at int32, op ir.CreateTable) {
	if _, held := f.held.Table(op.Table); held {
		f.refuse(at, op.Kind(), op.Table, "this schema already holds a table of that name")

		return
	}

	built, problem := table(op.Table, op.Columns, op.PrimaryKey)
	if problem != "" {
		f.refuse(at, op.Kind(), op.Table, problem)

		return
	}

	f.held.Tables = append(f.held.Tables, built)
}

// A new table states its key inline and an existing one states it as an operation (docs/ir.md §7.2),
// so the inline key has no name of its own. A snapshot records one and never compares it, no two
// engines agreeing on one (ADR 0065) -- measured, an inline key reads back as "" on SQLite,
// <table>_pkey on PostgreSQL and PRIMARY on MySQL, so there is no name to invent here.

// table renders one of an operation's table definitions as a snapshot holds it.
func table(name string, columns []ir.Column, key []string) (schema.Table, string) {
	built := schema.Table{Name: name, Columns: make([]schema.Column, 0, len(columns))}

	for _, column := range columns {
		projected, problem := projected(column)
		if problem != "" {
			return schema.Table{}, fmt.Sprintf("its column %q cannot be held: %s",
				column.Name, problem)
		}

		built.Columns = append(built.Columns, projected)
	}

	if len(key) > 0 {
		built.PrimaryKey = &schema.PrimaryKey{Columns: key}
	}

	return built, ""
}

// dropTable folds a table off the schema, and every reference another one made to it.
func (f *folder) dropTable(at int32, op ir.DropTable) {
	if _, held := f.table(at, op.Kind(), op.Table); !held {
		return
	}

	kept := make([]schema.Table, 0, len(f.held.Tables))

	for _, one := range f.held.Tables {
		if one.Name != op.Table {
			kept = append(kept, referenceless(one, op.Table))
		}
	}

	f.held.Tables = kept
}

// referenceless returns a table with every foreign key pointing at a dropped one gone.
func referenceless(table schema.Table, dropped string) schema.Table {
	var kept []schema.ForeignKey

	for _, key := range table.ForeignKeys {
		if key.ReferencesTable != dropped {
			kept = append(kept, key)
		}
	}

	table.ForeignKeys = kept

	return table
}

// renameTable folds a table's new name onto it, and onto every reference another one makes.
func (f *folder) renameTable(at int32, op ir.RenameTable) {
	if _, held := f.table(at, op.Kind(), op.From); !held {
		return
	}

	if _, taken := f.held.Table(op.To); taken {
		f.refuse(at, op.Kind(), op.To, "this schema already holds a table of that name")

		return
	}

	for i := range f.held.Tables {
		if f.held.Tables[i].Name == op.From {
			f.held.Tables[i].Name = op.To
		}

		f.held.Tables[i].ForeignKeys = mapped(f.held.Tables[i].ForeignKeys,
			func(key schema.ForeignKey) schema.ForeignKey {
				if key.ReferencesTable == op.From {
					key.ReferencesTable = op.To
				}

				return key
			})
	}
}

// createView folds a view onto the schema, replacing one of that name where it says so.
func (f *folder) createView(at int32, op ir.CreateView) {
	held, exists := f.held.View(op.View)

	if exists && !op.OrReplace {
		f.refuse(at, op.Kind(), op.View, "this schema already holds a view of that name, and the "+
			"operation does not replace one")

		return
	}

	built := schema.View{Name: op.View, Query: op.Query}

	if !exists {
		f.held.Views = append(f.held.Views, built)

		return
	}

	_ = held

	f.held.Views = mapped(f.held.Views, func(one schema.View) schema.View {
		if one.Name == op.View {
			return built
		}

		return one
	})
}

// dropView folds a view off the schema.
func (f *folder) dropView(at int32, op ir.DropView) {
	if _, held := f.held.View(op.View); !held {
		f.refuse(at, op.Kind(), op.View, "this schema holds no such view")

		return
	}

	kept := make([]schema.View, 0, len(f.held.Views))

	for _, one := range f.held.Views {
		if one.Name != op.View {
			kept = append(kept, one)
		}
	}

	f.held.Views = kept
}
