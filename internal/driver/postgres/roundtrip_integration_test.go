//go:build integration

package postgres

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// usersTable is the referenced side of the fixture's foreign key.
const usersTable = "migr_probe_users"

// ordersTable is the table the fixture alters, constrains, indexes, renames, and finally drops.
const ordersTable = "migr_probe_orders"

// archiveTable is where rename_table sends ordersTable, so the rename has somewhere to go.
const archiveTable = "migr_probe_archive"

// probeView is the view the fixture creates, replaces, and drops.
const probeView = "migr_probe_view"

// addedColumn is the column add_column adds and alter_column then widens.
const addedColumn = "note"

// renamedColumn is what rename_column calls it, chosen because it is a reserved word.
const renamedColumn = "order"

// firstViewQuery is what the view selects when it is created.
const firstViewQuery = "SELECT id FROM migr_probe_orders"

// terminatedViewQuery is the same query with the semicolon a migration author may have written.
const terminatedViewQuery = firstViewQuery + ";"

// secondViewQuery is what create_view narrows it to with or_replace set.
const secondViewQuery = "SELECT id FROM migr_probe_orders WHERE id > 0"

// checkExpression is the condition add_check applies to a column everyMappableColumn declares.
const checkExpression = "total >= 0"

// buildScript creates everything the fixture holds, in an order PostgreSQL will accept.
func buildScript(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table:      usersTable,
			Columns:    usersColumns(t),
			PrimaryKey: []string{"id"},
		},
		ir.CreateTable{Table: ordersTable, Columns: everyMappableColumn(t)},
		ir.AddColumn{Table: ordersTable, Column: addedNote(t)},
		ir.AlterColumn{
			Table:    ordersTable,
			Column:   addedColumn,
			Type:     probePointer(probeType(t, "string(128)")),
			Nullable: probePointer(false),
			Default:  ir.Literal("still fine"),
			From: &ir.ColumnState{
				Type:     probePointer(probeType(t, "string(64)")),
				Nullable: probePointer(true),
				Default:  ir.Literal("it's fine"),
			},
		},
		ir.AlterColumn{
			Table:       ordersTable,
			Column:      addedColumn,
			DropDefault: true,
			From:        &ir.ColumnState{Default: ir.Literal("still fine")},
		},
		ir.RenameColumn{Table: ordersTable, From: addedColumn, To: renamedColumn},
		ir.AddPrimaryKey{Table: ordersTable, Name: "pk_probe_orders", Columns: []string{"id"}},
		ir.AddUnique{Table: ordersTable, Name: "uq_probe_orders", Columns: []string{"id"}},
		ir.AddCheck{Table: ordersTable, Name: "ck_probe_orders", Expression: checkExpression},
		ir.AddIndex{Table: ordersTable, Name: "idx_probe_orders", Columns: []string{renamedColumn}},
		ir.AddForeignKey{
			Table:             ordersTable,
			Name:              "fk_probe_orders",
			Columns:           []string{"user_id"},
			ReferencesTable:   usersTable,
			ReferencesColumns: []string{"id"},
			OnDelete:          ir.ActionCascade,
			OnUpdate:          ir.ActionRestrict,
		},
		ir.CreateView{View: probeView, Query: terminatedViewQuery},
		ir.CreateView{
			View:      probeView,
			Query:     secondViewQuery,
			OrReplace: true,
			From:      &ir.ViewDef{Query: firstViewQuery},
		},
	}
}

// teardownScript removes everything buildScript made, dependents first, each recording what it drops.
func teardownScript(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.DropView{View: probeView, From: &ir.ViewDef{Query: secondViewQuery}},
		ir.DropForeignKey{
			Table: ordersTable,
			Name:  "fk_probe_orders",
			From: &ir.ForeignKeyDef{
				Columns:           []string{"user_id"},
				ReferencesTable:   usersTable,
				ReferencesColumns: []string{"id"},
				OnDelete:          ir.ActionCascade,
				OnUpdate:          ir.ActionRestrict,
			},
		},
		ir.DropIndex{
			Table: ordersTable,
			Name:  "idx_probe_orders",
			From:  &ir.ColumnsDef{Columns: []string{renamedColumn}},
		},
		ir.DropCheck{
			Table: ordersTable,
			Name:  "ck_probe_orders",
			From:  &ir.CheckDef{Expression: checkExpression},
		},
		ir.DropUnique{
			Table: ordersTable,
			Name:  "uq_probe_orders",
			From:  &ir.ColumnsDef{Columns: []string{"id"}},
		},
		ir.DropPrimaryKey{
			Table: ordersTable,
			Name:  "pk_probe_orders",
			From:  &ir.ColumnsDef{Columns: []string{"id"}},
		},
		ir.DropColumn{
			Table:  ordersTable,
			Column: droppedColumnName,
			From:   probePointer(droppedColumn(t)),
		},
		ir.RenameTable{From: ordersTable, To: archiveTable},
		ir.DropTable{
			Table: archiveTable,
			From:  &ir.TableDef{Columns: archiveColumns(t)},
		},
		ir.DropTable{
			Table: usersTable,
			From:  &ir.TableDef{Columns: usersColumns(t), PrimaryKey: []string{"id"}},
		},
	}
}

// script is the whole fixture: every operation but raw, which has no inverse to walk.
func script(t *testing.T) ir.Operations {
	t.Helper()

	return append(buildScript(t), teardownScript(t)...)
}

// usersColumns is the referenced table, kept small so the foreign key is the point of it.
func usersColumns(t *testing.T) []ir.Column {
	t.Helper()

	return []ir.Column{{Name: "id", Type: probeType(t, "uuid")}}
}

// addedNote is the column as add_column adds it, before alter_column widens and pins it.
func addedNote(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{
		Name:     addedColumn,
		Type:     probeType(t, "string(64)"),
		Nullable: true,
		Default:  ir.Literal("it's fine"),
	}
}

// widenedNote is that column as the archive table holds it: widened, no longer nullable, renamed.
func widenedNote(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{Name: renamedColumn, Type: probeType(t, "string(128)")}
}

// droppedColumnName is the column drop_column removes, and all three of its properties matter.
//
// It is nullable and it carries a default, so a reverse that restores only a name and a type does
// not pass. And it is third of twenty-five rather than last, so it comes back at a position it did
// not hold before -- which is what holds the digest to comparing schemas rather than column order,
// the one difference PostgreSQL cannot undo and the comparison therefore has to ignore.
const droppedColumnName = "total"

// droppedColumn is what drop_column removes, recorded exactly as the table holds it.
func droppedColumn(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{
		Name:     droppedColumnName,
		Type:     probeType(t, "int32"),
		Nullable: true,
		Default:  ir.Literal("0"),
	}
}

// archiveColumns is what the table holds when drop_table records it: one column gone, one renamed in.
func archiveColumns(t *testing.T) []ir.Column {
	t.Helper()

	var kept []ir.Column

	for _, column := range everyMappableColumn(t) {
		if column.Name != droppedColumnName {
			kept = append(kept, column)
		}
	}

	return append(kept, widenedNote(t))
}

// everyMappableColumn returns one column per logical type this driver claims to have a spelling for.
func everyMappableColumn(t *testing.T) []ir.Column {
	t.Helper()

	spellings := []string{
		"string(255)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
		"decimal(10,2)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
		"array(int32)", "array(string(16))",
	}

	columns := []ir.Column{
		{Name: "id", Type: probeType(t, "int64")},
		{Name: "user_id", Type: probeType(t, "uuid"), Nullable: true},
		droppedColumn(t),
		{Name: "key", Type: probeType(t, "uuid"), Default: ir.Call(ir.FuncUUID)},
		{Name: "seen", Type: probeType(t, "timestamptz"), Default: ir.Call(ir.FuncCurrentTimestamp)},
		{Name: "day", Type: probeType(t, "date"), Default: ir.Call(ir.FuncCurrentDate)},
		{Name: "hour", Type: probeType(t, "time"), Default: ir.Call(ir.FuncCurrentTime)},
	}

	for i, spelling := range spellings {
		columns = append(columns, ir.Column{
			Name:     fmt.Sprintf("mapped_%02d", i),
			Type:     probeType(t, spelling),
			Nullable: true,
		})
	}

	return columns
}

func TestEveryOperationRestoresTheSchemaItChanged(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			ops := script(t)
			checkCoverage(t, ops)

			for i, op := range ops {
				name := fmt.Sprintf("%02d_%s", i+1, op.Kind())
				if !t.Run(name, func(t *testing.T) { roundTrip(t, session, target, op) }) {
					return
				}
			}
		})
	}

	t.Log("Each operation is reversed in the state the fixture has reached, so none is asked to")
	t.Log("run where it would be illegal, and a failure names one operation rather than a schema.")
}

// roundTrip applies one operation, reverses it, and holds the schema to what it was before.
func roundTrip(t *testing.T, session *pgConn, target string, op ir.Operation) {
	t.Helper()

	before := describeSchema(t, session)

	apply(t, session, ir.Operations{op})

	after := describeSchema(t, session)
	if after == before {
		t.Log("An operation the digest cannot see round-trips for free: apply nothing, reverse")
		t.Log("nothing, compare equal. Either the fixture is not exercising this operation or")
		t.Log("describeSchema needs the probe that would show it.")
		t.Fatalf("%s changed nothing the schema digest can see", op.Kind())
	}

	inverse, err := op.Reverse()
	if err != nil {
		t.Fatalf("%s: Reverse: %v", op.Kind(), err)
	}

	apply(t, session, ir.Operations{inverse})

	if restored := describeSchema(t, session); restored != before {
		t.Fatalf("reversing %s did not restore the schema it changed:\n%s",
			op.Kind(), schema.Difference(before, restored))
	}

	apply(t, session, ir.Operations{op})

	if again := describeSchema(t, session); again != after {
		t.Fatalf("re-applying %s after its reverse reached a different schema:\n%s",
			op.Kind(), schema.Difference(after, again))
	}
}

// checkCoverage holds the fixture to the operation set the IR declares, not to the one it offers.
func checkCoverage(t *testing.T, ops ir.Operations) {
	t.Helper()

	for _, kind := range uncovered(ops) {
		t.Errorf("nothing in the fixture is a %s, so nothing proves it rolls back", kind)
	}

	checkEveryOmissionHasNoInverse(t, kindsOf(ops))
}

// uncovered returns the operations the IR declares that a fixture leaves untested, the inverseless excepted.
func uncovered(ops ir.Operations) []ir.Kind {
	covered := kindsOf(ops)

	var missing []ir.Kind

	for _, kind := range ir.Kinds() {
		if covered[kind] || inverseless(kind) {
			continue
		}

		missing = append(missing, kind)
	}

	return missing
}

// kindsOf reports which operations a fixture holds, whatever order it holds them in.
func kindsOf(ops ir.Operations) map[ir.Kind]bool {
	covered := make(map[ir.Kind]bool, len(ops))
	for _, op := range ops {
		covered[op.Kind()] = true
	}

	return covered
}

func TestTheCoverageCheckWouldNoticeAMissingOperation(t *testing.T) {
	missing := uncovered(ir.Operations{})

	if want := len(ir.Kinds()) - 1 - len(ir.DataKinds()); len(missing) != want {
		t.Errorf("an empty fixture leaves %d operations untested, want %d", len(missing), want)
		t.Log("The check reads ir.Kinds() rather than what the fixture offers, so a twenty-first")
		t.Log("operation fails it on the day it is added. Read the other way round it can never")
		t.Log("report anything, and would pass a fixture holding one operation.")
	}

	if slices.Contains(missing, ir.KindRaw) {
		t.Error("raw is reported as untested, though it is deliberately left out")
	}
}

// inverseless names the operations no fixture here can walk: raw, and the data operations.
func inverseless(kind ir.Kind) bool {
	return kind == ir.KindRaw || slices.Contains(ir.DataKinds(), kind)
}

// checkEveryOmissionHasNoInverse ties the fixture's gaps to a fact rather than to a preference.
func checkEveryOmissionHasNoInverse(t *testing.T, covered map[ir.Kind]bool) {
	t.Helper()

	for _, op := range drivertest.Operations(Name) {
		if !inverseless(op.Kind()) {
			continue
		}

		if covered[op.Kind()] {
			t.Errorf("the fixture holds a %s, which the framework cannot invert", op.Kind())
		}

		if _, err := op.Reverse(); !errors.Is(err, ir.ErrIrreversible) {
			t.Errorf("%s.Reverse() returned %v, want ir.ErrIrreversible", op.Kind(), err)
			t.Log("It is left out of this fixture only because it has no inverse to walk. If that")
			t.Log("stops being true it belongs here, like every other operation in the set.")
		}
	}
}

func TestABuildRollsBackToTheSchemaItFound(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			empty := describeSchema(t, session)
			build := buildScript(t)

			apply(t, session, build)

			if built := describeSchema(t, session); built == empty {
				t.Fatal("the build left the schema exactly as it found it")
			}

			apply(t, session, reverseAll(t, build))

			if restored := describeSchema(t, session); restored != empty {
				t.Fatalf("rolling the build back left the schema changed:\n%s",
					schema.Difference(empty, restored))
			}
		})
	}

	t.Log("Both runs are one Apply call, so thirteen operations share one transaction -- this")
	t.Log("driver marks nothing NoTransaction, which is what makes that a single group.")
	t.Log("It is also where the order reversal is load-bearing: undone forwards, the first")
	t.Log("statement would drop a table a foreign key still references.")
}

func TestATeardownIsRebuiltFromWhatItRecorded(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			apply(t, session, buildScript(t))
			built := describeSchema(t, session)

			teardown := teardownScript(t)
			apply(t, session, teardown)

			emptied := describeSchema(t, session)
			if emptied == built {
				t.Fatal("the teardown removed nothing")
			}

			apply(t, session, reverseAll(t, teardown))

			if rebuilt := describeSchema(t, session); rebuilt != built {
				t.Fatalf("the teardown's reverse did not rebuild what it removed:\n%s",
					schema.Difference(built, rebuilt))
			}
		})
	}

	t.Log("Eight of these ten operations carry a from payload, and this is what it is for: the")
	t.Log("whole structure is reconstructed from recorded prior state and from nothing else.")
	t.Log("ADR 0008 calls that payload trust rather than proof; against a live server it is")
	t.Log("measured, which is the strongest check available before introspection lands.")
}

// partialIndexSQL creates an index no v1 operation can express, which is what raw is for.
const partialIndexSQL = `CREATE INDEX "idx_probe_active" ON "migr_probe_users" ("id") WHERE "active"`

// dropPartialIndexSQL is the undo a migration author has to write, because nothing derives it.
const dropPartialIndexSQL = `DROP INDEX "idx_probe_active"`

func TestOnlyAnAuthoredUndoTakesARawOperationBack(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			apply(t, session, ir.Operations{ir.CreateTable{
				Table: usersTable,
				Columns: []ir.Column{
					{Name: "id", Type: probeType(t, "uuid")},
					{Name: "active", Type: probeType(t, "bool"), Nullable: true},
				},
				PrimaryKey: []string{"id"},
			}})

			before := describeSchema(t, session)
			raw := ir.Raw{Driver: Name, SQL: partialIndexSQL}

			apply(t, session, ir.Operations{raw})

			if after := describeSchema(t, session); after == before {
				t.Fatal("the raw statement changed nothing the schema digest can see")
			}

			checkNothingIsDerived(t, raw)

			apply(t, session, ir.Operations{ir.Raw{Driver: Name, SQL: dropPartialIndexSQL}})

			if restored := describeSchema(t, session); restored != before {
				t.Fatalf("the authored undo did not take the raw statement back:\n%s",
					schema.Difference(before, restored))
			}
		})
	}
}

// checkNothingIsDerived holds ReverseAll to failing whole rather than to inverting what it can.
func checkNothingIsDerived(t *testing.T, raw ir.Raw) {
	t.Helper()

	reversible := ir.AddColumn{Table: usersTable, Column: ir.Column{
		Name: "later", Type: probeType(t, "int32"), Nullable: true,
	}}

	undo, err := ir.ReverseAll(ir.Operations{reversible, raw})
	if !errors.Is(err, ir.ErrIrreversible) {
		t.Fatalf("ReverseAll over a raw operation returned %v, want ir.ErrIrreversible", err)
	}

	if undo != nil {
		t.Errorf("ReverseAll returned %d operations alongside its refusal", len(undo))
		t.Log("The list holds one operation that inverts cleanly and one that cannot, and a")
		t.Log("rollback that ran the first half would leave a migration half undone with nothing")
		t.Log("recording it. Failing whole is what lets M4-T6 halt before doing anything.")
	}
}

func TestAReversedDropRestoresOnlyWhatItRecorded(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			target := scratchSchema(t, dsn)
			session := sessionIn(t, dsn, target)

			checkTheIndexIsNotRecorded(t, session, target)
			checkTheImpliedNotNullIsNotRestored(t, session, target)
		})
	}

	t.Log("Neither of these is a compiler bug, and neither is fixable with a wider from payload:")
	t.Log("one operation records a column and PostgreSQL destroys more than a column, the other")
	t.Log("inverts by name and a name says nothing about what the constraint implied. Both still")
	t.Log("hold, because both are facts about this server rather than about migr -- and both are")
	t.Log("why engine.Unrestorable refuses either operation before it runs rather than at rollback")
	t.Log("time, when the index is already gone and the nullability was never recorded (M11-T2).")
	t.Log("The refusal itself is asserted in internal/engine and in cmd/migr, which may name a")
	t.Log("driver; this package cannot, since internal/engine imports it.")
}

// checkTheIndexIsNotRecorded shows a reversed drop_column bringing back the column and not its index.
func checkTheIndexIsNotRecorded(t *testing.T, session *pgConn, target string) {
	t.Helper()

	tagged := ir.Column{Name: "tag", Type: probeType(t, "string(64)"), Nullable: true}

	apply(t, session, ir.Operations{
		ir.CreateTable{Table: "migr_gap_indexed", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")}, tagged,
		}},
		ir.AddIndex{Table: "migr_gap_indexed", Name: "idx_gap_tag", Columns: []string{"tag"}},
	})

	before := describeSchema(t, session)
	drop := ir.DropColumn{Table: "migr_gap_indexed", Column: "tag", From: &tagged}

	apply(t, session, ir.Operations{drop})
	apply(t, session, ir.Operations{reverse(t, drop)})

	restored := describeSchema(t, session)
	if restored == before {
		t.Error("reversing a drop_column restored the index on the dropped column")
		t.Log("If PostgreSQL has started recording that dependency, this gap is closed and the")
		t.Log("fixture no longer has to drop an index before the column it covers.")

		return
	}

	if !strings.Contains(restored, "migr_gap_indexed.tag ") {
		t.Errorf("the column itself did not come back:\n%s", schema.Difference(before, restored))
	}

	if strings.Contains(restored, "idx_gap_tag") {
		t.Errorf("the index is present after all:\n%s", schema.Difference(before, restored))
	}
}

// checkTheImpliedNotNullIsNotRestored shows a reversed add_primary_key leaving a column not null.
func checkTheImpliedNotNullIsNotRestored(t *testing.T, session *pgConn, target string) {
	t.Helper()

	apply(t, session, ir.Operations{ir.CreateTable{
		Table:   "migr_gap_nullable",
		Columns: []ir.Column{{Name: "id", Type: probeType(t, "int64"), Nullable: true}},
	}})

	before := describeSchema(t, session)
	add := ir.AddPrimaryKey{Table: "migr_gap_nullable", Name: "pk_gap", Columns: []string{"id"}}

	apply(t, session, ir.Operations{add})
	apply(t, session, ir.Operations{reverse(t, add)})

	restored := describeSchema(t, session)
	if restored == before {
		t.Error("reversing an add_primary_key restored the column's nullability")
		t.Log("PostgreSQL used to keep the NOT NULL that ADD PRIMARY KEY implied. If it has")
		t.Log("stopped, add_primary_key round-trips on any column and this case can go.")

		return
	}

	if !strings.Contains(restored, "migr_gap_nullable.id int64 not null") {
		t.Errorf("the column is not left NOT NULL, so this reads as something else:\n%s",
			schema.Difference(before, restored))
	}
}

// apply compiles operations and runs them the way the executor will, as one transaction.
func apply(t *testing.T, session *pgConn, ops ir.Operations) {
	t.Helper()

	if err := applyOps(t, session, ops); err != nil {
		t.Fatalf("applying %s: %v", describeKinds(ops), err)
	}
}

// reverse inverts one operation, failing the test rather than the migration it stands for.
func reverse(t *testing.T, op ir.Operation) ir.Operation {
	t.Helper()

	inverse, err := op.Reverse()
	if err != nil {
		t.Fatalf("%s: Reverse: %v", op.Kind(), err)
	}

	return inverse
}

// reverseAll inverts a whole migration, in the order a rollback would run it.
func reverseAll(t *testing.T, ops ir.Operations) ir.Operations {
	t.Helper()

	reversed, err := ir.ReverseAll(ops)
	if err != nil {
		t.Fatalf("ReverseAll: %v", err)
	}

	return reversed
}

// describeKinds names the operations a failed run was carrying, since statements say less.
func describeKinds(ops ir.Operations) string {
	kinds := make([]string, len(ops))
	for i, op := range ops {
		kinds[i] = op.Kind().String()
	}

	return strings.Join(kinds, ", ")
}
