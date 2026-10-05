package driver

import (
	"fmt"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// The fixture's own column names, stated once so the expectation and the operations cannot drift.
const (
	keyColumn     = "id"
	stringColumn  = "name"
	textColumn    = "note"
	boolColumn    = "live"
	floatColumn   = "rate"
	decimalColumn = "amount"
	uuidColumn    = "token"
	stampColumn   = "seen"
	dateColumn    = "day"
	timeColumn    = "clock"
	binaryColumn  = "blob"
	parentColumn  = "parent_id"
)

// The fixture's own parameters, which every engine must read back unchanged.
const (
	stringWidth      = 64
	decimalPrecision = 10
	decimalScale     = 2
	literalDefault   = "x"
	numericDefault   = "7"
)

// parent names the referenced table this fixture builds, under the prefix the caller gave.
func (s Snapshot) parent() string { return s.Prefix + "_parent" }

// child names the referencing table this fixture builds, under the prefix the caller gave.
func (s Snapshot) child() string { return s.Prefix + "_child" }

// named renders one of this fixture's constraint names under the prefix the caller gave.
func (s Snapshot) named(suffix string) string { return s.Prefix + "_" + suffix }

// What it leaves out, it leaves out for a measured reason rather than for convenience. Three of the
// engine-text elements ADR 0065 names are absent -- a check expression, a view body and an
// expression default -- because no two engines render one the same way and nothing compares them.
// Seven logical types are absent, and inexactTypes states why each one is.

// fixture is the schema every driver must read back as the same snapshot.
func (s Snapshot) fixture(t *testing.T) ir.Operations {
	return ir.Operations{
		ir.CreateTable{Table: s.parent(), Columns: []ir.Column{
			{Name: keyColumn, Type: parse(t, "int64")},
			{Name: stringColumn, Type: parse(t, fmt.Sprintf("string(%d)", stringWidth))},
		}, PrimaryKey: []string{keyColumn}},
		ir.AddUnique{Table: s.parent(), Name: s.named("uq"), Columns: []string{stringColumn}},
		ir.CreateTable{Table: s.child(), Columns: s.childColumns(t)},
		ir.AddPrimaryKey{Table: s.child(), Name: s.named("pk"), Columns: []string{keyColumn}},
		ir.AddIndex{Table: s.child(), Name: s.named("idx"),
			Columns: []string{boolColumn, dateColumn}},
		ir.AddForeignKey{Table: s.child(), Name: s.named("fk"),
			Columns:         []string{parentColumn},
			ReferencesTable: s.parent(), ReferencesColumns: []string{keyColumn},
			OnDelete: ir.ActionCascade},
	}
}

// childColumns are the fixture's columns, one per logical type every engine resolves the same way.
func (s Snapshot) childColumns(t *testing.T) []ir.Column {
	return []ir.Column{
		{Name: keyColumn, Type: parse(t, "int64")},
		{Name: stringColumn, Type: parse(t, fmt.Sprintf("string(%d)", stringWidth)),
			Nullable: true, Default: ir.Literal(literalDefault)},
		{Name: textColumn, Type: parse(t, "text"), Nullable: true},
		{Name: boolColumn, Type: parse(t, "bool"), Nullable: true},
		{Name: floatColumn, Type: parse(t, "float64"), Nullable: true},
		{Name: decimalColumn, Type: parse(t,
			fmt.Sprintf("decimal(%d,%d)", decimalPrecision, decimalScale)), Nullable: true},
		{Name: uuidColumn, Type: parse(t, "uuid"), Nullable: true},
		{Name: stampColumn, Type: parse(t, "timestamp"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentTimestamp)},
		{Name: dateColumn, Type: parse(t, "date"), Nullable: true},
		{Name: timeColumn, Type: parse(t, "time"), Nullable: true},
		{Name: binaryColumn, Type: parse(t, "binary"), Nullable: true},
		{Name: parentColumn, Type: parse(t, "int64"), Nullable: true,
			Default: ir.Literal(numericDefault)},
	}
}

// expected is the snapshot every driver must read the fixture back as, column types normalized.
func (s Snapshot) expected(t *testing.T) schema.Snapshot {
	return schema.Snapshot{Tables: []schema.Table{
		{
			Name: s.parent(),
			Columns: []schema.Column{
				{Name: keyColumn, Type: logical(t, "int64")},
				{Name: stringColumn, Type: logical(t, fmt.Sprintf("string(%d)", stringWidth))},
			},
			PrimaryKey: &schema.PrimaryKey{Columns: []string{keyColumn}},
			Uniques: []schema.Unique{
				{Name: s.named("uq"), Columns: []string{stringColumn}},
			},
		},
		{
			Name:       s.child(),
			Columns:    s.expectedColumns(t),
			PrimaryKey: &schema.PrimaryKey{Columns: []string{keyColumn}},
			Indexes: []schema.Index{
				{Name: s.named("idx"), Columns: []string{boolColumn, dateColumn}},
			},
			ForeignKeys: []schema.ForeignKey{
				{
					Name:              s.named("fk"),
					Columns:           []string{parentColumn},
					ReferencesTable:   s.parent(),
					ReferencesColumns: []string{keyColumn},
					OnDelete:          schema.ActionCascade,
					OnUpdate:          schema.ActionNoAction,
				},
			},
		},
	}}
}

// expectedColumns are the fixture's columns as every driver must read them back.
func (s Snapshot) expectedColumns(t *testing.T) []schema.Column {
	literal, numeric := literalDefault, numericDefault

	return []schema.Column{
		{Name: keyColumn, Type: logical(t, "int64")},
		{Name: stringColumn, Type: logical(t, fmt.Sprintf("string(%d)", stringWidth)),
			Nullable: true, Default: &schema.ColumnDefault{Literal: &literal}},
		{Name: textColumn, Type: logical(t, "text"), Nullable: true},
		{Name: boolColumn, Type: logical(t, "bool"), Nullable: true},
		{Name: floatColumn, Type: logical(t, "float64"), Nullable: true},
		{Name: decimalColumn, Type: logical(t,
			fmt.Sprintf("decimal(%d,%d)", decimalPrecision, decimalScale)), Nullable: true},
		{Name: uuidColumn, Type: logical(t, "uuid"), Nullable: true},
		{Name: stampColumn, Type: logical(t, "timestamp"), Nullable: true,
			Default: &schema.ColumnDefault{Function: schema.FuncCurrentTimestamp}},
		{Name: dateColumn, Type: logical(t, "date"), Nullable: true},
		{Name: timeColumn, Type: logical(t, "time"), Nullable: true},
		{Name: binaryColumn, Type: logical(t, "binary"), Nullable: true},
		{Name: parentColumn, Type: logical(t, "int64"), Nullable: true,
			Default: &schema.ColumnDefault{Literal: &numeric}},
	}
}

// inexactTypes names every logical type this fixture cannot carry, and what measurement excludes it.
//
// Each entry is a catalog that cannot answer the question, not a driver that chose not to: the
// logical type one engine reads back for that spelling is a different logical type, so no expected
// value could hold for every engine at once. Coverage is checked against this list, so a nineteenth
// logical type fails the suite until somebody decides which half it is in.
var inexactTypes = map[schema.Kind]string{
	schema.KindInt16: "SQLite declares int16, int32 and int64 all as integer, whose best answer " +
		"is int64",
	schema.KindInt32: "SQLite declares int16, int32 and int64 all as integer, whose best answer " +
		"is int64",
	schema.KindFloat32: "SQLite declares both floats as real, whose best answer is float64",
	schema.KindJSON:    "SQLite declares text, json and enum all as text, whose best answer is text",
	schema.KindTimestampTZ: "MySQL and MariaDB declare timestamp and timestamptz both as " +
		"datetime(6), whose best answer is timestamp",
	schema.KindEnum:  "PostgreSQL refuses it outright, so no fixture can carry it on every engine",
	schema.KindArray: "MySQL and SQLite refuse it outright, so no fixture can carry it on every engine",
}

// checkCoverage reports a logical type this suite neither carries nor accounts for.
func (s Snapshot) checkCoverage(t *testing.T) {
	t.Helper()

	carried := make(map[schema.Kind]bool, len(s.childColumns(t)))
	for _, column := range s.childColumns(t) {
		carried[column.Type.Kind()] = true
	}

	for _, kind := range schema.Kinds() {
		if carried[kind] {
			continue
		}

		if _, stated := inexactTypes[kind]; stated {
			continue
		}

		t.Errorf("the fixture carries no %s column and inexactTypes says nothing about it, so "+
			"nothing holds this driver to reading one back", kind)
	}
}

// parse parses one of this fixture's logical types, which are all constants in this file.
func parse(t *testing.T, spelling string) schema.Type {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("the snapshot fixture names a logical type that will not parse: %q: %v",
			spelling, err)
	}

	return parsed
}

// logical returns the normalized column type the expectation compares against.
func logical(t *testing.T, spelling string) schema.ColumnType {
	t.Helper()

	built, err := schema.ExactType(parse(t, spelling), normalized)
	if err != nil {
		t.Fatalf("the snapshot fixture names a column type that will not build: %q: %v",
			spelling, err)
	}

	return built
}
