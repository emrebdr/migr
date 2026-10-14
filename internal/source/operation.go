package source

import "github.com/emrebdr/migr/internal/ir"

// chainOf returns the calls one operation is written as, or why no source expresses it.
func chainOf(op ir.Operation) (Chain, error) {
	switch o := op.(type) {
	case ir.CreateTable:
		return createTable(o), nil
	case ir.DropTable:
		return dropTable(o), nil
	case ir.RenameTable:
		return table(o.From, call("RenameTo", Name(o.To))), nil
	case ir.AddColumn:
		return addColumn(o), nil
	case ir.DropColumn:
		return dropColumn(o), nil
	case ir.RenameColumn:
		return table(o.Table, call("RenameColumn", Name(o.From), Name(o.To))), nil
	case ir.AlterColumn:
		return alterColumn(o), nil
	case ir.AddIndex:
		return table(o.Table, call("AddIndex", Name(o.Name), Names(o.Columns))), nil
	case ir.DropIndex:
		return dropped(o.Table, "DropIndex", o.Name, ShapeColumnsDef, columnsDef(o.From)), nil
	case ir.AddUnique:
		return table(o.Table, call("AddUnique", Name(o.Name), Names(o.Columns))), nil
	case ir.DropUnique:
		return dropped(o.Table, "DropUnique", o.Name, ShapeColumnsDef, columnsDef(o.From)), nil
	case ir.AddCheck:
		return table(o.Table, call("AddCheck", Name(o.Name), Literal(o.Expression))), nil
	case ir.DropCheck:
		return dropped(o.Table, "DropCheck", o.Name, ShapeCheckDef, checkDef(o.From)), nil
	case ir.AddPrimaryKey:
		return table(o.Table, call("AddPrimaryKey", Name(o.Name), Names(o.Columns))), nil
	case ir.DropPrimaryKey:
		return dropped(o.Table, "DropPrimaryKey", o.Name, ShapeColumnsDef,
			columnsDef(o.From)), nil
	case ir.AddForeignKey:
		return addForeignKey(o), nil
	case ir.DropForeignKey:
		return dropped(o.Table, "DropForeignKey", o.Name, ShapeForeignKeyDef,
			foreignKeyDef(o.From)), nil
	case ir.CreateView:
		return createView(o), nil
	case ir.DropView:
		return dropView(o), nil
	case ir.Raw:
		return Chain{}, ErrNotRenderable
	case ir.DataCopy, ir.DataBackfill, ir.DataTransform:
		return Chain{}, ErrNotRenderable
	default:
		return Chain{}, ErrNotRenderable
	}
}

// createTable writes a new table, whose key is stated inline rather than as an operation.
func createTable(op ir.CreateTable) Chain {
	links := make([]Call, 0, len(op.Columns)+2)

	for _, column := range op.Columns {
		links = append(links, columnLinks(column)...)
	}

	if len(op.PrimaryKey) > 0 {
		links = append(links, call("PrimaryKey", Names(op.PrimaryKey)))
	}

	return Chain{Root: Call{Method: "CreateTable", Args: []Arg{Name(op.Table)}}, Links: links}
}

// A column is NOT NULL unless the chain says otherwise and a default is a link of its own, which is
// docs/ir.md §7.2's two conventions written the way an author writes them. So a nullable column
// renders Null and a NOT NULL one renders nothing, which is what keeps a generated migration
// readable as the migration a person would have written.

// columnLinks writes one column of a new table, with the qualifiers it needs and no others.
func columnLinks(column ir.Column) []Call {
	links := []Call{call("Column", Name(column.Name), Logical(column.Type))}

	if column.Nullable {
		links = append(links, call("Null"))
	}

	if column.Default != nil {
		links = append(links, call("Default", defaultArg(column.Default)))
	}

	return links
}

// dropTable writes a table's removal, recording the definition its own inverse rebuilds it from.
func dropTable(op ir.DropTable) Chain {
	return table(op.Table, recorded([]Call{call("Drop")},
		ShapeTableDef, tableDefFields(op.From), op.From != nil)...)
}

// addColumn writes one column onto an existing table, with the qualifiers it needs.
func addColumn(op ir.AddColumn) Chain {
	links := []Call{call("AddColumn", Name(op.Column.Name), Logical(op.Column.Type))}

	if op.Column.Nullable {
		links = append(links, call("Null"))
	}

	if op.Column.Default != nil {
		links = append(links, call("Default", defaultArg(op.Column.Default)))
	}

	return table(op.Table, links...)
}

// dropColumn writes one column off a table, recording what it was.
func dropColumn(op ir.DropColumn) Chain {
	links := recorded([]Call{call("DropColumn", Name(op.Column))},
		ShapeColumn, columnFields(*nonNilColumn(op.From)), op.From != nil)

	return table(op.Table, links...)
}

// nonNilColumn returns a recorded column, and an empty one where nothing was recorded.
func nonNilColumn(held *ir.Column) *ir.Column {
	if held == nil {
		return &ir.Column{}
	}

	return held
}

// An alteration states each prior attribute as a call of its own -- WasType, WasNullable,
// WasDefault, WasNoDefault -- rather than as one recorded payload, because that is the shape both
// SDKs give it: the state is a delta and an author sets the halves they are changing.

// alterColumn writes what changes about one column, and records what each of those was.
func alterColumn(op ir.AlterColumn) Chain {
	links := []Call{call("AlterColumn", Name(op.Column))}

	if op.Type != nil {
		links = append(links, call("Type", Logical(*op.Type)))
	}

	if op.Nullable != nil {
		links = append(links, nullability(*op.Nullable))
	}

	if op.Default != nil {
		links = append(links, call("Default", defaultArg(op.Default)))
	}

	if op.DropDefault {
		links = append(links, call("DropDefault"))
	}

	return table(op.Table, append(links, priorState(op.From)...)...)
}

// priorState writes what an alteration recorded about the column before it, one call per attribute.
func priorState(held *ir.ColumnState) []Call {
	if held == nil || held.Empty() {
		return nil
	}

	var links []Call

	if held.Type != nil {
		links = append(links, call("WasType", Logical(*held.Type)))
	}

	if held.Nullable != nil {
		links = append(links, call("WasNullable", Flag(*held.Nullable)))
	}

	if held.Default != nil {
		links = append(links, call("WasDefault", defaultArg(held.Default)))
	}

	if held.NoDefault {
		links = append(links, call("WasNoDefault"))
	}

	return links
}

// nullability names the call that states what a column is to accept, there being one call per answer.
func nullability(nullable bool) Call {
	if nullable {
		return call("Null")
	}

	return call("NotNull")
}

// addForeignKey writes a reference, with the two referential actions it states.
func addForeignKey(op ir.AddForeignKey) Chain {
	links := []Call{call("AddForeignKey", Name(op.Name), Names(op.Columns),
		Name(op.ReferencesTable), Names(op.ReferencesColumns))}

	if op.OnDelete != "" {
		links = append(links, call("OnDelete", Action(op.OnDelete)))
	}

	if op.OnUpdate != "" {
		links = append(links, call("OnUpdate", Action(op.OnUpdate)))
	}

	return table(op.Table, links...)
}

// dropped writes an operation that removes a named constraint, recording what it was.
func dropped(name, method, constraint string, shape Shape, fields []Field) Chain {
	return table(name, recorded([]Call{call(method, Name(constraint))},
		shape, fields, fields != nil)...)
}

// A view records its prior body as a bare query rather than as a payload, both SDKs spelling that
// Was(query) -- the one recorded state that is a string and not a struct.

// createView writes a view, saying whether it replaces one and recording the body it replaced.
func createView(op ir.CreateView) Chain {
	method := "Create"
	if op.OrReplace {
		method = "Replace"
	}

	return view(op.View, append([]Call{call(method, Literal(op.Query))},
		priorQuery(op.From)...)...)
}

// dropView writes a view's removal, recording the query its own inverse rebuilds it from.
func dropView(op ir.DropView) Chain {
	return view(op.View, append([]Call{call("Drop")}, priorQuery(op.From)...)...)
}

// view starts a chain from a view, which is the one object that is not a table.
func view(name string, links ...Call) Chain {
	return Chain{Root: Call{Method: "View", Args: []Arg{Name(name)}}, Links: links}
}

// priorQuery writes a recorded view body, which is a query rather than a payload.
func priorQuery(held *ir.ViewDef) []Call {
	if held == nil {
		return nil
	}

	return []Call{call("Was", Literal(held.Query))}
}

// defaultArg writes a default as the one of the two things it is: a literal, or a neutral function.
func defaultArg(held *ir.Default) Arg {
	if held.Literal != nil {
		return Record{Shape: ShapeDefault,
			Fields: []Field{{Member: "Literal", Value: Literal(*held.Literal)}}}
	}

	return Record{Shape: ShapeDefault,
		Fields: []Field{{Member: "Function", Value: Function(held.Function)}}}
}

// columnFields writes one column as the fields a recorded column holds.
func columnFields(column ir.Column) []Field {
	fields := []Field{
		{Member: "Name", Value: Name(column.Name)},
		{Member: "Type", Value: Logical(column.Type)},
	}

	if column.Nullable {
		fields = append(fields, Field{Member: "Nullable", Value: Flag(true)})
	}

	if column.Default != nil {
		fields = append(fields, Field{Member: "Default", Value: defaultArg(column.Default)})
	}

	return fields
}

// tableDefFields writes a recorded table definition as its fields, which is its columns and its key.
func tableDefFields(held *ir.TableDef) []Field {
	if held == nil {
		return nil
	}

	columns := make(List, 0, len(held.Columns))
	for _, column := range held.Columns {
		columns = append(columns, Record{Shape: ShapeColumn, Fields: columnFields(column)})
	}

	fields := []Field{{Member: "Columns", Value: columns}}

	if len(held.PrimaryKey) > 0 {
		fields = append(fields, Field{Member: "PrimaryKey", Value: Names(held.PrimaryKey)})
	}

	return fields
}

// columnsDef writes a recorded column list as its one field.
func columnsDef(held *ir.ColumnsDef) []Field {
	if held == nil {
		return nil
	}

	return []Field{{Member: "Columns", Value: Names(held.Columns)}}
}

// checkDef writes a recorded check expression as its one field.
func checkDef(held *ir.CheckDef) []Field {
	if held == nil {
		return nil
	}

	return []Field{{Member: "Expression", Value: Literal(held.Expression)}}
}

// foreignKeyDef writes a recorded foreign key as its fields, both referential actions included.
func foreignKeyDef(held *ir.ForeignKeyDef) []Field {
	if held == nil {
		return nil
	}

	fields := []Field{
		{Member: "Columns", Value: Names(held.Columns)},
		{Member: "ReferencesTable", Value: Name(held.ReferencesTable)},
		{Member: "ReferencesColumns", Value: Names(held.ReferencesColumns)},
	}

	if held.OnDelete != "" {
		fields = append(fields, Field{Member: "OnDelete", Value: Action(held.OnDelete)})
	}

	if held.OnUpdate != "" {
		fields = append(fields, Field{Member: "OnUpdate", Value: Action(held.OnUpdate)})
	}

	return fields
}

// viewDefFields is unused by the chain and kept out: both SDKs record a view's prior body as a bare
// query, which priorQuery writes.
