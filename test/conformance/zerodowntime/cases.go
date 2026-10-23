package zerodowntime

import (
	"fmt"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// SeededRows is how many rows the loaded table holds: at it, measured, a rewrite takes several budgets on every engine.
const SeededRows = 1_000_000

// SideRows is how many rows a case's own table holds, where what is judged does not grow with the table.
const SideRows = 50_000

// names are the tables one run of the suite works with.
type names struct {
	// main is the loaded table, seeded once and left as it was after every case.
	main string
	// side is a case's own table, created by its setup, seeded, and dropped afterwards.
	side string
	// other is a second table a case creates or points at.
	other string
}

// types are the logical types the cases' columns are declared as.
type types struct {
	int32, int64, label, pad schema.Type
}

// readTypes parses the logical types every case needs.
func readTypes() (types, error) {
	var (
		ty    types
		err   error
		spell = map[*schema.Type]string{&ty.int32: "int32", &ty.int64: "int64", &ty.label: "string(40)",
			&ty.pad: "string(255)"}
	)

	for target, spelled := range spell {
		if *target, err = schema.Parse(spelled); err != nil {
			return types{}, fmt.Errorf("parsing %s: %w", spelled, err)
		}
	}

	return ty, nil
}

// scenario is one case: what it sets up, what it changes under load, and what undoes the change.
type scenario struct {
	// name names the case in every report.
	name string
	// side reports a case whose load works its own table rather than the main one.
	side bool
	// keyless reports a side table created without a primary key.
	keyless bool
	// online reports a change declared through a shadow table, which runs up alone.
	online bool
	// unattended reports a shadow-table change nobody confirms, which has to cut over by itself under the load.
	unattended bool
	// rewrittenOnly reports a case run only where the session performs its change as a safer sequence.
	rewrittenOnly bool
	// setup are the operations the case needs in place first, which are not measured.
	setup func(n names, ty types) ir.Operations
	// undo is the setup's authored down, nil where the engine derives it.
	undo func(n names, ty types) ir.Operations
	// change is what is measured, up and then down.
	change func(n names, ty types, l Load, driverName string) ir.Operations
	// authored reports a down the case writes itself, which down then returns.
	authored bool
	// down is that authored down, empty for a change nothing can invert.
	down func(n names, l Load, driverName string) ir.Operations
}

// loaded returns the table a case's load works.
func (s scenario) loaded(n names) string {
	if s.side {
		return n.side
	}

	return n.main
}

// tableOf is the shape both the main table and a side table take.
func tableOf(table string, ty types, keyed bool) ir.CreateTable {
	created := ir.CreateTable{Table: table, Columns: []ir.Column{
		{Name: "id", Type: ty.int64},
		{Name: "n", Type: ty.int32},
		{Name: "label", Type: ty.label},
		{Name: "pad", Type: ty.pad},
		{Name: "extra", Type: ty.int32, Nullable: true},
	}}

	if keyed {
		created.PrimaryKey = []string{"id"}
	}

	return created
}

// one wraps a single operation as a case's change.
func one(op ir.Operation) ir.Operations {
	return ir.Operations{op}
}

// pointerTo returns a pointer to a copy of v, for the optional fields an operation's payload holds.
func pointerTo[T any](v T) *T {
	return &v
}

// scenarios are every case the suite runs, in the order it runs them.
func scenarios() []scenario {
	return append(schemaScenarios(), dataScenarios()...)
}

// schemaScenarios change the schema, each undone by the engine's own derived inverse.
func schemaScenarios() []scenario {
	return []scenario{
		{name: "create_table", change: func(n names, ty types, _ Load, _ string) ir.Operations {
			return one(ir.CreateTable{Table: n.other, Columns: []ir.Column{{Name: "id", Type: ty.int64},
				{Name: "v", Type: ty.int32, Nullable: true}}, PrimaryKey: []string{"id"}})
		}},
		{name: "rename_table", side: true, change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.RenameTable{From: n.side, To: n.other})
		}},
		{name: "add_column", change: func(n names, ty types, _ Load, _ string) ir.Operations {
			return one(ir.AddColumn{Table: n.main, Column: ir.Column{Name: "zd_added", Type: ty.int32, Nullable: true}})
		}},
		{name: "rename_column", change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.RenameColumn{Table: n.main, From: "extra", To: "zd_moved"})
		}},
		{name: "alter_column (retype)", change: func(n names, ty types, _ Load, _ string) ir.Operations {
			return one(ir.AlterColumn{Table: n.main, Column: "extra", Type: &ty.int64,
				From: &ir.ColumnState{Type: &ty.int32}})
		}},
		notNull("alter_column (not null)", true, false),
		notNull("alter_column (not null, rewritten, every row)", false, true),
		{name: "add_index", change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AddIndex{Table: n.main, Name: "zd_pad_label_idx", Columns: []string{"pad", "label"}})
		}},
		{name: "add_unique", change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AddUnique{Table: n.main, Name: "zd_id_label_key", Columns: []string{"id", "label"}})
		}},
		foreignKey("add_foreign_key", true, false),
		foreignKey("add_foreign_key (rewritten, every row)", false, true),
		{name: "add_check", change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AddCheck{Table: n.main, Name: "zd_id_positive", Expression: "id > 0"})
		}},
		{name: "add_primary_key", side: true, keyless: true, setup: func(n names, _ types) ir.Operations {
			return one(ir.AddIndex{Table: n.side, Name: n.side + "_id_idx", Columns: []string{"id"}})
		}, change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AddPrimaryKey{Table: n.side, Name: n.side + "_pkey", Columns: []string{"id"}})
		}},
		{name: "create_view", change: func(n names, _ types, l Load, _ string) ir.Operations {
			return one(ir.CreateView{View: n.other, Query: l.View(n.main)})
		}},
		{name: "raw", authored: true, change: func(_ names, _ types, l Load, driverName string) ir.Operations {
			return one(ir.Raw{Driver: driverName, SQL: l.Raw()})
		}, down: func(_ names, l Load, driverName string) ir.Operations {
			return one(ir.Raw{Driver: driverName, SQL: l.Raw()})
		}},
		{name: "shadow table (alter_column)", side: true, online: true, change: shadowedRetype},
		{name: "shadow table (alter_column), unattended", side: true, online: true, unattended: true,
			change: shadowedRetype},
	}
}

// shadowedRetype widens the side table's extra column, which the shadow cases run through a copy.
func shadowedRetype(n names, ty types, _ Load, _ string) ir.Operations {
	return one(ir.AlterColumn{Table: n.side, Column: "extra", Type: &ty.int64, From: &ir.ColumnState{Type: &ty.int32}})
}

// notNull is the case tightening a backfilled column: on a side table everywhere, and on the loaded one where it is rewritten.
func notNull(name string, side, rewrittenOnly bool) scenario {
	table := func(n names) string {
		if side {
			return n.side
		}

		return n.main
	}

	return scenario{name: name, side: side, rewrittenOnly: rewrittenOnly,
		setup: func(n names, ty types) ir.Operations {
			return ir.Operations{
				ir.AddColumn{Table: table(n), Column: ir.Column{Name: "zd_nn", Type: ty.int32, Nullable: true}},
				ir.DataBackfill{Table: table(n), Column: "zd_nn", Type: ty.int32, Value: ir.Literal("0")},
			}
		}, undo: func(n names, ty types) ir.Operations {
			return one(ir.DropColumn{Table: table(n), Column: "zd_nn",
				From: &ir.Column{Name: "zd_nn", Type: ty.int32, Nullable: true}})
		}, change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AlterColumn{Table: table(n), Column: "zd_nn", Nullable: pointerTo(false),
				From: &ir.ColumnState{Nullable: pointerTo(true)}})
		}}
}

// foreignKey is the case adding a foreign key: on a side table everywhere, and on the loaded one where it is rewritten.
func foreignKey(name string, side, rewrittenOnly bool) scenario {
	table := func(n names) string {
		if side {
			return n.side
		}

		return n.main
	}

	return scenario{name: name, side: side, rewrittenOnly: rewrittenOnly,
		setup: func(n names, ty types) ir.Operations {
			return ir.Operations{
				ir.CreateTable{Table: n.other, Columns: []ir.Column{{Name: "id", Type: ty.int64}}, PrimaryKey: []string{"id"}},
				ir.AddColumn{Table: table(n), Column: ir.Column{Name: "zd_ref", Type: ty.int64, Nullable: true}},
			}
		}, change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.AddForeignKey{Table: table(n), Name: "zd_ref_fk", Columns: []string{"zd_ref"},
				ReferencesTable: n.other, ReferencesColumns: []string{"id"}})
		}}
}

// dataScenarios write rows, each as a data-only migration run in chunks, with an authored empty down.
func dataScenarios() []scenario {
	none := func(names, Load, string) ir.Operations { return nil }

	return []scenario{
		{name: "data_copy", side: true, authored: true, down: none, setup: func(n names, ty types) ir.Operations {
			return one(ir.CreateTable{Table: n.other, Columns: []ir.Column{{Name: "id", Type: ty.int64},
				{Name: "n", Type: ty.int32}}, PrimaryKey: []string{"id"}})
		}, change: func(n names, _ types, _ Load, _ string) ir.Operations {
			return one(ir.DataCopy{From: n.side, To: n.other, Columns: []ir.CopyColumn{{From: "id", To: "id"},
				{From: "n", To: "n"}}})
		}},
		{name: "data_backfill", side: true, authored: true, down: none,
			change: func(n names, ty types, _ Load, _ string) ir.Operations {
				return one(ir.DataBackfill{Table: n.side, Column: "extra", Type: ty.int32, Value: ir.Literal("1")})
			}},
		{name: "data_transform", side: true, authored: true, down: none,
			change: func(n names, ty types, _ Load, _ string) ir.Operations {
				return one(ir.DataTransform{Table: n.side, Column: "label", Type: ty.label, From: "label",
					Function: ir.TransformUpper})
			}},
	}
}
