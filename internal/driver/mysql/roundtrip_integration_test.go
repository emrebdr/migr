//go:build integration

package mysql

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// fixture is the objects one round trip owns, named from the running test so no two cases collide.
type fixture struct {
	// prefix is what every name begins with, and what the digest filters this database's objects by.
	prefix string
	// users is the referenced side of the foreign key.
	users string
	// orders is the table the fixture alters, constrains, indexes, renames, and finally drops.
	orders string
	// archive is where rename_table sends orders, so the rename has somewhere to go.
	archive string
	// view is the view the fixture creates, replaces, and drops.
	view string
}

// newFixture names what this test will create and removes whatever a previous run left behind.
func newFixture(t *testing.T, c *mysqlConn) fixture {
	t.Helper()

	prefix := uniqueName(t)
	f := fixture{
		prefix:  prefix,
		users:   prefix + "_u",
		orders:  prefix + "_o",
		archive: prefix + "_a",
		view:    prefix + "_v",
	}

	dropLater(t, c, f.owned()...)
	drop(t.Context(), c, f.owned()...)

	return f
}

// owned lists this fixture's objects dependents first, since users cannot be dropped before orders.
func (f fixture) owned() []string {
	return []string{f.view, f.orders, f.archive, f.users}
}

// named renders one of this fixture's constraint or index names, which MySQL keeps 64 characters of.
func (f fixture) named(suffix string) string {
	return f.prefix + "_" + suffix
}

// M11-T6 moved this off SHOW CREATE and onto the snapshot, and the sorting it used to do is why.
// A reversed drop_column appends the column, and no v1 operation can put it back where it was --
// MySQL's AFTER having no field in the IR -- so a comparison of the server's own rendering had to
// sort its lines to survive a round trip that was in fact correct. A snapshot holds what a table has
// and never the order it has it in, so Canonical does that properly and the workaround is gone.
//
// What survives from the old shape is the prefix scope, because one engine gives every test in this
// package the same database: the snapshot reads the whole of it, and what this fixture owns is the
// part of it this comparison is about. The history table is excluded by Introspect itself (ADR 0069)
// rather than by the filter, which matters because eachServer names it after the running test and
// the prefix would otherwise match it.

// describeSchema renders every object a fixture owns, as the neutral snapshot every driver answers with.
func describeSchema(t *testing.T, c *mysqlConn, f fixture) string {
	t.Helper()

	held, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("introspecting the database: %v", err)
	}

	return schema.Render(f.scoped(c, *held))
}

// scoped keeps the part of one database's schema this fixture owns, the rest belonging to other tests.
func (f fixture) scoped(c *mysqlConn, held schema.Snapshot) schema.Snapshot {
	stored := storedName(f.prefix, c.folding)

	var kept schema.Snapshot

	for _, table := range held.Tables {
		if strings.HasPrefix(table.Name, stored) {
			kept.Tables = append(kept.Tables, table)
		}
	}

	for _, view := range held.Views {
		if strings.HasPrefix(view.Name, stored) {
			kept.Views = append(kept.Views, view)
		}
	}

	return kept
}

// addedColumn is the column add_column adds and alter_column then widens.
const addedColumn = "note"

// renamedColumn is what rename_column calls it, chosen because it is a reserved word.
const renamedColumn = "order"

// checkExpression is the condition add_check applies to a column everyMappableColumn declares.
const checkExpression = "total >= 0"

// droppedColumnName is the column drop_column removes, and all three of its properties matter.
//
// It is nullable and it carries a default, so a reverse that restores only a name and a type does not
// pass. Its default is a literal rather than uuid(): MySQL refuses to add a column defaulting to a
// per-row function while it writes a binary log (error 1674, and log_bin is on by default in 8.0),
// which is a refusal a reversed drop must not run into. And it is third of twenty-five rather than
// last, so it comes back at a position it did not hold before -- which is what holds the digest to
// comparing schemas rather than column order.
const droppedColumnName = "total"

// buildScript creates everything the fixture holds, in an order MySQL will accept.
func buildScript(t *testing.T, f fixture) ir.Operations {
	t.Helper()

	return append(ir.Operations{
		ir.CreateTable{Table: f.users, Columns: usersColumns(t), PrimaryKey: []string{"id"}},
		ir.CreateTable{Table: f.orders, Columns: everyMappableColumn(t)},
		ir.AddColumn{Table: f.orders, Column: addedNote(t)},
		ir.AlterColumn{
			Table:    f.orders,
			Column:   addedColumn,
			Type:     typePointerLive(t, "string(128)"),
			Nullable: boolPointerLive(false),
			Default:  ir.Literal("still fine"),
			From: &ir.ColumnState{
				Type:     typePointerLive(t, "string(64)"),
				Nullable: boolPointerLive(true),
				Default:  ir.Literal("it's fine"),
			},
		},
		ir.AlterColumn{
			Table:       f.orders,
			Column:      addedColumn,
			DropDefault: true,
			From:        &ir.ColumnState{Default: ir.Literal("still fine")},
		},
		ir.RenameColumn{Table: f.orders, From: addedColumn, To: renamedColumn},
		ir.AddPrimaryKey{Table: f.orders, Name: f.named("pk"), Columns: []string{"id"}},
		ir.AddUnique{Table: f.orders, Name: f.named("uq"), Columns: []string{"id"}},
		ir.AddCheck{Table: f.orders, Name: f.named("ck"), Expression: checkExpression},
		ir.AddIndex{Table: f.orders, Name: f.named("ix"), Columns: []string{renamedColumn}},
	}, keyedScript(t, f)...)
}

// keyIndexSuffix names the index the foreign key's own column carries before the key is declared.
//
// It is not decoration. MySQL requires an index on a referencing column and makes one named after the
// constraint when it finds none -- measured on both servers -- and DROP FOREIGN KEY then leaves that
// index behind, because the IR has no field recording that it was ever created. Given an index of its
// own the server reuses it and drops nothing, which is the only way add_foreign_key round-trips at
// all. TestAReversedDropRestoresOnlyWhatItRecorded holds the other case.
const keyIndexSuffix = "fx"

// keyedScript indexes the foreign key's own column before declaring the key, and then the view.
func keyedScript(t *testing.T, f fixture) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.AddIndex{Table: f.orders, Name: f.named(keyIndexSuffix), Columns: []string{"user_id"}},
		ir.AddForeignKey{
			Table:             f.orders,
			Name:              f.named("fk"),
			Columns:           []string{"user_id"},
			ReferencesTable:   f.users,
			ReferencesColumns: []string{"id"},
			OnDelete:          ir.ActionCascade,
			OnUpdate:          ir.ActionRestrict,
		},
		ir.CreateView{View: f.view, Query: f.terminatedViewQuery()},
		ir.CreateView{
			View:      f.view,
			Query:     f.secondViewQuery(),
			OrReplace: true,
			From:      &ir.ViewDef{Query: f.firstViewQuery()},
		},
	}
}

// firstViewQuery is what the view selects when it is created.
func (f fixture) firstViewQuery() string {
	return "SELECT id FROM " + quote(f.orders)
}

// terminatedViewQuery is the same query with the semicolon a migration author may have written.
func (f fixture) terminatedViewQuery() string {
	return f.firstViewQuery() + ";"
}

// secondViewQuery is what create_view narrows it to with or_replace set.
func (f fixture) secondViewQuery() string {
	return f.firstViewQuery() + " WHERE id > 0"
}

// teardownScript removes everything buildScript made, dependents first, each recording what it drops.
func teardownScript(t *testing.T, f fixture) ir.Operations {
	t.Helper()

	return append(ir.Operations{
		ir.DropView{View: f.view, From: &ir.ViewDef{Query: f.secondViewQuery()}},
		ir.DropForeignKey{
			Table: f.orders,
			Name:  f.named("fk"),
			From: &ir.ForeignKeyDef{
				Columns:           []string{"user_id"},
				ReferencesTable:   f.users,
				ReferencesColumns: []string{"id"},
				OnDelete:          ir.ActionCascade,
				OnUpdate:          ir.ActionRestrict,
			},
		},
		ir.DropIndex{
			Table: f.orders,
			Name:  f.named(keyIndexSuffix),
			From:  &ir.ColumnsDef{Columns: []string{"user_id"}},
		},
		ir.DropIndex{
			Table: f.orders,
			Name:  f.named("ix"),
			From:  &ir.ColumnsDef{Columns: []string{renamedColumn}},
		},
		ir.DropCheck{
			Table: f.orders,
			Name:  f.named("ck"),
			From:  &ir.CheckDef{Expression: checkExpression},
		},
		ir.DropUnique{
			Table: f.orders,
			Name:  f.named("uq"),
			From:  &ir.ColumnsDef{Columns: []string{"id"}},
		},
		ir.DropPrimaryKey{
			Table: f.orders,
			Name:  f.named("pk"),
			From:  &ir.ColumnsDef{Columns: []string{"id"}},
		},
	}, droppedScript(t, f)...)
}

// droppedScript takes the table apart once nothing is attached to it any more.
func droppedScript(t *testing.T, f fixture) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.DropColumn{
			Table:  f.orders,
			Column: droppedColumnName,
			From:   pointerToColumn(droppedColumn(t)),
		},
		ir.RenameTable{From: f.orders, To: f.archive},
		ir.DropTable{Table: f.archive, From: &ir.TableDef{Columns: archiveColumns(t)}},
		ir.DropTable{
			Table: f.users,
			From:  &ir.TableDef{Columns: usersColumns(t), PrimaryKey: []string{"id"}},
		},
	}
}

// script is the whole fixture: every operation but raw, which has no inverse to walk.
func script(t *testing.T, f fixture) ir.Operations {
	t.Helper()

	return append(buildScript(t, f), teardownScript(t, f)...)
}

// usersColumns is the referenced table, kept small so the foreign key is the point of it.
func usersColumns(t *testing.T) []ir.Column {
	t.Helper()

	return []ir.Column{{Name: "id", Type: liveType(t, "uuid")}}
}

// addedNote is the column as add_column adds it, before alter_column widens and pins it.
func addedNote(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{
		Name:     addedColumn,
		Type:     liveType(t, "string(64)"),
		Nullable: true,
		Default:  ir.Literal("it's fine"),
	}
}

// widenedNote is that column as the archive table holds it: widened, no longer nullable, renamed.
func widenedNote(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{Name: renamedColumn, Type: liveType(t, "string(128)")}
}

// droppedColumn is what drop_column removes, recorded exactly as the table holds it.
func droppedColumn(t *testing.T) ir.Column {
	t.Helper()

	return ir.Column{
		Name:     droppedColumnName,
		Type:     liveType(t, "int32"),
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

// mappableSpellings is every logical type this driver has a MySQL spelling for, enum included.
//
// Seventeen of the eighteen, where PostgreSQL's fixture omits enum -- MySQL's is native and that
// one's is not. array is what is left out, and it is left out because this driver refuses it at
// compile with no database open: a JSON column would hold a list but would be a different type from
// the one the migration named.
var mappableSpellings = []string{
	"string(255)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
	"decimal(10,2)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
	"enum(red,green)",
}

// labelDefault is the one default here that MySQL takes only as a parenthesised expression.
//
// A text, blob or json column refuses a bare literal default with error 1101, so this driver
// parenthesises one (ADR 0058) -- which no other column in the fixture reaches, every other text
// column being left without a default. Found by the mutation sweep: dropping the parentheses passed
// everything until a column needed them.
const labelDefault = "unset"

// everyMappableColumn returns one column per logical type this driver claims to have a spelling for.
func everyMappableColumn(t *testing.T) []ir.Column {
	t.Helper()

	columns := []ir.Column{
		{Name: "id", Type: liveType(t, "int64")},
		{Name: "user_id", Type: liveType(t, "uuid"), Nullable: true},
		droppedColumn(t),
		{Name: "key", Type: liveType(t, "uuid"), Default: ir.Call(ir.FuncUUID)},
		{Name: "seen", Type: liveType(t, "timestamptz"), Default: ir.Call(ir.FuncCurrentTimestamp)},
		{Name: "day", Type: liveType(t, "date"), Default: ir.Call(ir.FuncCurrentDate)},
		{Name: "hour", Type: liveType(t, "time"), Default: ir.Call(ir.FuncCurrentTime)},
		{Name: "label", Type: liveType(t, "text"), Nullable: true, Default: ir.Literal(labelDefault)},
	}

	for i, spelling := range mappableSpellings {
		columns = append(columns, ir.Column{
			Name:     fmt.Sprintf("mapped_%02d", i),
			Type:     liveType(t, spelling),
			Nullable: true,
		})
	}

	return columns
}

// pointerToColumn takes a column's address, which a from payload wants and a composite literal cannot.
func pointerToColumn(c ir.Column) *ir.Column {
	return &c
}

func TestASnapshotDescribesTheFixtureAndNotMigrsOwnBookkeeping(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		t.Cleanup(func() {
			drop(context.WithoutCancel(t.Context()), c, c.table)
		})

		if err := c.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		if described := describeSchema(t, c, f); described != "" {
			t.Errorf("the history table is in the snapshot:\n%s", described)
			t.Log("eachServer names it after the running test, which is the fixture's prefix")
			t.Log("exactly -- so the prefix scope matches it and only Introspect's own exclusion")
			t.Log("keeps it out. A comparison carrying migr's bookkeeping describes something other")
			t.Log("than the schema the migration changed, and every migr diff over this database")
			t.Log("would propose dropping the migration history (ADR 0069).")
		}
	})
}

func TestTheScopeKeepsThisFixtureAndLeavesEveryOtherTestsObjects(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		applyOps(t, c, buildScript(t, f))

		held, err := c.Introspect(t.Context())
		if err != nil {
			t.Fatalf("Introspect: %v", err)
		}

		scoped := f.scoped(c, *held)
		if len(scoped.Tables)+len(scoped.Views) == 0 {
			t.Fatalf("the scope kept nothing of a fixture that was just built:\n%s",
				schema.Render(*held))
		}

		for _, table := range scoped.Tables {
			if !slices.Contains(f.owned(), table.Name) {
				t.Errorf("the scope kept %q, which this fixture does not own", table.Name)
			}
		}

		for _, view := range scoped.Views {
			if !slices.Contains(f.owned(), view.Name) {
				t.Errorf("the scope kept the view %q, which this fixture does not own", view.Name)
			}
		}

		t.Log("One engine gives every test in this package the same database, so the snapshot reads")
		t.Log("objects this round trip never built. Scoping is what keeps the comparison about this")
		t.Log("fixture -- and a scope that kept nothing would make every round trip here pass by")
		t.Log("comparing two empty schemas.")
	})
}

func TestEveryOperationRestoresTheSchemaItChanged(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		ops := script(t, f)
		checkCoverage(t, ops)

		for i, op := range ops {
			name := fmt.Sprintf("%02d_%s", i+1, op.Kind())
			if !t.Run(name, func(t *testing.T) { roundTrip(t, c, f, op) }) {
				return
			}
		}

		t.Log("Each operation is reversed in the state the fixture has reached, so none is asked to")
		t.Log("run where it would be illegal, and a failure names one operation rather than a schema.")
		t.Log("alter_column is the one this driver compiles against the database, so MySQL is the")
		t.Log("first engine whose emulated reverse has run at all -- PostgreSQL emulates nothing.")
	})
}

// roundTrip applies one operation, reverses it, and holds the schema to what it was before.
func roundTrip(t *testing.T, c *mysqlConn, f fixture, op ir.Operation) {
	t.Helper()

	before := describeSchema(t, c, f)

	applyOps(t, c, ir.Operations{op})

	after := describeSchema(t, c, f)
	if after == before {
		t.Log("An operation the digest cannot see round-trips for free: apply nothing, reverse")
		t.Log("nothing, compare equal. Either the fixture is not exercising this operation or")
		t.Log("describeSchema needs the probe that would show it.")
		t.Fatalf("%s changed nothing the schema digest can see", op.Kind())
	}

	applyOps(t, c, ir.Operations{reverse(t, op)})

	if restored := describeSchema(t, c, f); restored != before {
		t.Fatalf("reversing %s did not restore the schema it changed:\n%s",
			op.Kind(), schema.Difference(before, restored))
	}

	applyOps(t, c, ir.Operations{op})

	if again := describeSchema(t, c, f); again != after {
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

	checkRawIsTheOnlyOmission(t, kindsOf(ops))
}

// uncovered returns the operations the IR declares that a fixture leaves untested, raw excepted.
func uncovered(ops ir.Operations) []ir.Kind {
	covered := kindsOf(ops)

	var missing []ir.Kind

	for _, kind := range ir.Kinds() {
		if covered[kind] || kind == ir.KindRaw {
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

// checkRawIsTheOnlyOmission ties the fixture's one gap to a fact rather than to a preference.
func checkRawIsTheOnlyOmission(t *testing.T, covered map[ir.Kind]bool) {
	t.Helper()

	if covered[ir.KindRaw] {
		t.Error("the fixture holds a raw operation, which the framework cannot invert")
	}

	if _, err := (ir.Raw{Driver: Name, SQL: "SELECT 1"}).Reverse(); !errors.Is(err, ir.ErrIrreversible) {
		t.Errorf("ir.Raw.Reverse() returned %v, want ir.ErrIrreversible", err)
		t.Log("raw is left out of this fixture only because it has no inverse to walk. If that")
		t.Log("stops being true it belongs here, like every other operation in the set.")
	}
}

func TestTheCoverageCheckWouldNoticeAMissingOperation(t *testing.T) {
	missing := uncovered(ir.Operations{})

	if want := len(ir.Kinds()) - 1; len(missing) != want {
		t.Errorf("an empty fixture leaves %d operations untested, want %d", len(missing), want)
		t.Log("The check reads ir.Kinds() rather than what the fixture offers, so a twenty-first")
		t.Log("operation fails it on the day it is added. Read the other way round it can never")
		t.Log("report anything, and would pass a fixture holding one operation.")
	}

	if slices.Contains(missing, ir.KindRaw) {
		t.Error("raw is reported as untested, though it is deliberately left out")
	}
}

func TestABuildRollsBackToTheSchemaItFound(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		empty := describeSchema(t, c, f)
		build := buildScript(t, f)

		applyOps(t, c, build)

		if built := describeSchema(t, c, f); built == empty {
			t.Fatal("the build left the schema exactly as it found it")
		}

		applyOps(t, c, reverseAll(t, build))

		if restored := describeSchema(t, c, f); restored != empty {
			t.Fatalf("rolling the build back left the schema changed:\n%s",
				schema.Difference(empty, restored))
		}
	})

	t.Log("Fourteen operations undone newest first, which is where the order reversal is")
	t.Log("load-bearing: undone forwards, the first statement would drop a table a foreign key")
	t.Log("still references. Nothing here shares a transaction -- this engine's DDL commits")
	t.Log("itself, which is the premise the whole of M9 is built on.")
}

func TestATeardownIsRebuiltFromWhatItRecorded(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		applyOps(t, c, buildScript(t, f))
		built := describeSchema(t, c, f)

		teardown := teardownScript(t, f)
		applyOps(t, c, teardown)

		emptied := describeSchema(t, c, f)
		if emptied == built {
			t.Fatal("the teardown removed nothing")
		}

		applyOps(t, c, reverseAll(t, teardown))

		if rebuilt := describeSchema(t, c, f); rebuilt != built {
			t.Fatalf("the teardown's reverse did not rebuild what it removed:\n%s",
				schema.Difference(built, rebuilt))
		}
	})

	t.Log("Ten of these eleven operations carry a from payload, and this is what it is for: the")
	t.Log("whole structure is reconstructed from recorded prior state and from nothing else.")
	t.Log("ADR 0008 calls that payload trust rather than proof; against a live server it is")
	t.Log("measured, which is the strongest check available before introspection lands.")
}

func TestOnlyAnAuthoredUndoTakesARawOperationBack(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		applyOps(t, c, ir.Operations{ir.CreateTable{
			Table: f.users,
			Columns: []ir.Column{
				{Name: "id", Type: liveType(t, "uuid")},
				{Name: "body", Type: liveType(t, "text"), Nullable: true},
			},
			PrimaryKey: []string{"id"},
		}})

		before := describeSchema(t, c, f)
		raw := ir.Raw{Driver: Name, SQL: f.prefixIndexSQL()}

		applyOps(t, c, ir.Operations{raw})

		if after := describeSchema(t, c, f); after == before {
			t.Fatal("the raw statement changed nothing the schema digest can see")
		}

		checkNothingIsDerived(t, f, raw)

		applyOps(t, c, ir.Operations{ir.Raw{Driver: Name, SQL: f.dropPrefixIndexSQL()}})

		if restored := describeSchema(t, c, f); restored != before {
			t.Fatalf("the authored undo did not take the raw statement back:\n%s",
				schema.Difference(before, restored))
		}
	})

	t.Log("A prefix length is this engine's partial index: no v1 operation carries one, which is")
	t.Log("the same refusal add_index over a longtext column runs into (ADR 0058). So the one")
	t.Log("statement raw exists for is a statement an operator would really reach for.")
}

// prefixIndexSQL creates an index no v1 operation can express, which is what raw is for.
func (f fixture) prefixIndexSQL() string {
	return fmt.Sprintf("CREATE INDEX %s ON %s (%s(10))", quote(f.named("pi")), quote(f.users),
		quote("body"))
}

// dropPrefixIndexSQL is the undo a migration author has to write, because nothing derives it.
func (f fixture) dropPrefixIndexSQL() string {
	return fmt.Sprintf("DROP INDEX %s ON %s", quote(f.named("pi")), quote(f.users))
}

// checkNothingIsDerived holds ReverseAll to failing whole rather than to inverting what it can.
func checkNothingIsDerived(t *testing.T, f fixture, raw ir.Raw) {
	t.Helper()

	reversible := ir.AddColumn{Table: f.users, Column: ir.Column{
		Name: "later", Type: liveType(t, "int32"), Nullable: true,
	}}

	undo, err := ir.ReverseAll(ir.Operations{reversible, raw})
	if !errors.Is(err, ir.ErrIrreversible) {
		t.Fatalf("ReverseAll over a raw operation returned %v, want ir.ErrIrreversible", err)
	}

	if undo != nil {
		t.Errorf("ReverseAll returned %d operations alongside its refusal", len(undo))
		t.Log("The list holds one operation that inverts cleanly and one that cannot, and a")
		t.Log("rollback that ran the first half would leave a migration half undone with nothing")
		t.Log("recording it. Failing whole is what lets the executor halt before doing anything.")
	}
}

func TestAReversedDropRestoresOnlyWhatItRecorded(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		f := newFixture(t, c)

		checkTheForeignKeysOwnIndexIsNotRecorded(t, c, f)
		checkTheImpliedNotNullIsNotRestored(t, c, f)
	})

	t.Log("Neither of these is a compiler bug, and neither is fixable with a wider from payload:")
	t.Log("one operation records a key and MySQL creates more than a key, the other inverts by")
	t.Log("name and a name says nothing about what the constraint implied. PostgreSQL has the")
	t.Log("second of these exactly and the first in a different shape -- a reversed drop_column")
	t.Log("not restoring an index. The second closed with M11-T2: engine.Unrestorable refuses an")
	t.Log("add_primary_key over a nullable column before it runs. The first did not, and that is")
	t.Log("deliberate -- the index is one this server adds for a key over an unindexed column, so")
	t.Log("it is residue a server creates rather than a definition the operation set cannot record,")
	t.Log("and CLAUDE.md §11 puts an engine difference in the capability system. It stays pinned.")
}

// checkTheForeignKeysOwnIndexIsNotRecorded shows a dropped foreign key leaving the index MySQL made.
func checkTheForeignKeysOwnIndexIsNotRecorded(t *testing.T, c *mysqlConn, f fixture) {
	t.Helper()

	applyOps(t, c, ir.Operations{
		ir.CreateTable{Table: f.users, Columns: usersColumns(t), PrimaryKey: []string{"id"}},
		ir.CreateTable{Table: f.orders, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
			{Name: "user_id", Type: liveType(t, "uuid"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
	})

	before := describeSchema(t, c, f)
	add := ir.AddForeignKey{
		Table:             f.orders,
		Name:              f.named("fk"),
		Columns:           []string{"user_id"},
		ReferencesTable:   f.users,
		ReferencesColumns: []string{"id"},
	}

	applyOps(t, c, ir.Operations{add})
	applyOps(t, c, ir.Operations{reverse(t, add)})

	restored := describeSchema(t, c, f)
	if restored == before {
		t.Error("reversing an add_foreign_key over an unindexed column restored the schema")
		t.Log("If this server has started dropping the index it created for a foreign key, the")
		t.Log("gap is closed and the fixture no longer has to index user_id before keying it.")

		return
	}

	orders := scopedTable(t, c, f, f.orders)

	if len(orders.ForeignKeys) != 0 {
		t.Errorf("the key itself is still there, so this reads as something else:\n%s",
			schema.Difference(before, restored))
	}

	if !holdsIndex(orders, f.named("fk")) {
		t.Errorf("what is left is not the key's own index:\n%s", schema.Difference(before, restored))
	}

	applyOps(t, c, ir.Operations{
		ir.DropTable{Table: f.orders},
		ir.DropTable{Table: f.users},
	})
}

// checkTheImpliedNotNullIsNotRestored shows a reversed add_primary_key leaving a column not null.
func checkTheImpliedNotNullIsNotRestored(t *testing.T, c *mysqlConn, f fixture) {
	t.Helper()

	applyOps(t, c, ir.Operations{ir.CreateTable{
		Table:   f.archive,
		Columns: []ir.Column{{Name: "id", Type: liveType(t, "int64"), Nullable: true}},
	}})

	before := describeSchema(t, c, f)
	add := ir.AddPrimaryKey{Table: f.archive, Name: f.named("pk"), Columns: []string{"id"}}

	applyOps(t, c, ir.Operations{add})
	applyOps(t, c, ir.Operations{reverse(t, add)})

	restored := describeSchema(t, c, f)
	if restored == before {
		t.Error("reversing an add_primary_key restored the column's nullability")
		t.Log("This server used to keep the NOT NULL that ADD PRIMARY KEY implied. If it has")
		t.Log("stopped, add_primary_key round-trips on any column and this case can go.")

		return
	}

	column, there := scopedTable(t, c, f, f.archive).Column("id")
	if !there {
		t.Fatalf("the column the key was over is gone:\n%s", restored)
	}

	if column.Nullable {
		t.Errorf("the column is not left NOT NULL, so this reads as something else:\n%s",
			schema.Difference(before, restored))
	}
}

// scopedTable reads one of this fixture's tables back as the snapshot holds it.
func scopedTable(t *testing.T, c *mysqlConn, f fixture, name string) schema.Table {
	t.Helper()

	held, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("introspecting the database: %v", err)
	}

	table, there := f.scoped(c, *held).Table(name)
	if !there {
		t.Fatalf("the snapshot does not hold %q:\n%s", name, schema.Render(*held))
	}

	return table
}

// holdsIndex reports whether a table carries an index of one name, whatever that index covers.
func holdsIndex(table schema.Table, name string) bool {
	return slices.ContainsFunc(table.Indexes, func(held schema.Index) bool {
		return held.Name == name
	})
}

// applyOps runs a fixture's operations the way a planner would, in order and one statement at a time.
func applyOps(t *testing.T, c *mysqlConn, ops ir.Operations) {
	t.Helper()

	for _, op := range ops {
		if err := op.Validate(); err != nil {
			t.Fatalf("%s: the fixture is not a well-formed operation: %v", op.Kind(), err)
		}

		for i, stmt := range statementsFor(t, c, op) {
			if err := c.Exec(t.Context(), stmt); err != nil {
				t.Fatalf("%s: statement %d of it was refused:\n%s\n%v", op.Kind(), i+1, stmt.SQL, err)
			}
		}
	}
}

// statementsFor compiles one operation, asking the session for the ones compiled against the database.
func statementsFor(t *testing.T, c *mysqlConn, op ir.Operation) []driver.Statement {
	t.Helper()

	stmts, err := compile(op)
	if err == nil {
		return stmts
	}

	if !errors.Is(err, driver.ErrNeedsSession) {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	expanded, err := c.Expand(t.Context(), op)
	if err != nil {
		t.Fatalf("expanding %s: %v", op.Kind(), err)
	}

	return expanded
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
