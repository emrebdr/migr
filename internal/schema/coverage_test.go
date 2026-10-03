package schema_test

import (
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// This file is the external test package on purpose: it is the one test that reads ir.Kinds(), and
// internal/ir imports internal/schema, so an in-package test importing it would be an import cycle.

// covered is what one operation does to a snapshot, declared for every kind in the v1 set.
type covered struct {
	// element is the thing in a snapshot the operation creates, changes or removes.
	element schema.Element
	// note says how the model holds it, for a reader deciding whether the mapping is still true.
	note string
	// before is a schema the operation would be applied to, and after is the result.
	before func(t *testing.T) schema.Snapshot
	after  func(t *testing.T) schema.Snapshot
}

// coverage maps every v1 operation to what it changes in a snapshot, which ir.Kinds() holds total.
var coverage = map[ir.Kind]covered{
	ir.KindCreateTable: {schema.ElementTable, "a Table appears", nothing, plain},
	ir.KindDropTable:   {schema.ElementTable, "a Table is gone", plain, nothing},
	ir.KindRenameTable: {schema.ElementTable, "Table.Name changes", plain, renamedTable},
	ir.KindAddColumn:   {schema.ElementColumn, "a Column appears", plain, withColumn},
	ir.KindDropColumn:  {schema.ElementColumn, "a Column is gone", withColumn, plain},
	ir.KindRenameColumn: {schema.ElementColumn, "Column.Name changes",
		withColumn, renamedColumn},
	ir.KindAlterColumn: {schema.ElementColumn, "Column.Type, Nullable or Default changes",
		withColumn, retypedColumn},
	ir.KindAddIndex:  {schema.ElementIndex, "an Index appears", plain, withIndex},
	ir.KindDropIndex: {schema.ElementIndex, "an Index is gone", withIndex, plain},
	ir.KindAddForeignKey: {schema.ElementForeignKey, "a ForeignKey appears",
		plain, withForeignKey},
	ir.KindDropForeignKey: {schema.ElementForeignKey, "a ForeignKey is gone",
		withForeignKey, plain},
	ir.KindAddUnique:  {schema.ElementUnique, "a Unique appears", plain, withUnique},
	ir.KindDropUnique: {schema.ElementUnique, "a Unique is gone", withUnique, plain},
	ir.KindAddCheck:   {schema.ElementCheck, "a Check appears", plain, withCheck},
	ir.KindDropCheck:  {schema.ElementCheck, "a Check is gone", withCheck, plain},
	ir.KindAddPrimaryKey: {schema.ElementPrimaryKey, "Table.PrimaryKey appears",
		plain, withPrimaryKey},
	ir.KindDropPrimaryKey: {schema.ElementPrimaryKey, "Table.PrimaryKey is gone",
		withPrimaryKey, plain},
	ir.KindCreateView: {schema.ElementView, "a View appears, or View.Query changes",
		plain, withView},
	ir.KindDropView: {schema.ElementView, "a View is gone", withView, plain},
	ir.KindRaw: {"", "nothing: its effect is opaque to the framework, which is also why it has " +
		"no inverse", nil, nil},
}

// exactly returns the column type a driver would record for a distinct native spelling.
func exactly(t *testing.T, spelling, native string) schema.ColumnType {
	t.Helper()

	parsed, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("Parse(%q): %v", spelling, err)
	}

	built, err := schema.ExactType(parsed, native)
	if err != nil {
		t.Fatalf("ExactType(%q, %q): %v", spelling, native, err)
	}

	return built
}

// nothing returns the schema of a database holding nothing at all.
func nothing(_ *testing.T) schema.Snapshot {
	return schema.Snapshot{}
}

// plain returns the schema every pair below is a change to.
func plain(t *testing.T) schema.Snapshot {
	t.Helper()

	return schema.Snapshot{Tables: []schema.Table{{
		Name:    "users",
		Columns: []schema.Column{{Name: "id", Type: exactly(t, "int64", "bigint")}},
	}}}
}

// changed returns the plain schema with one change made to it.
func changed(t *testing.T, change func(s *schema.Snapshot)) schema.Snapshot {
	t.Helper()

	snapshot := plain(t)
	change(&snapshot)

	return snapshot
}

// renamedTable is the plain schema with its table under another name.
func renamedTable(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) { s.Tables[0].Name = "people" })
}

// withColumn is the plain schema with a second column.
func withColumn(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables[0].Columns = append(s.Tables[0].Columns, schema.Column{Name: "email",
			Type: exactly(t, "string(255)", "character varying(255)")})
	})
}

// renamedColumn is that schema with the second column under another name.
func renamedColumn(t *testing.T) schema.Snapshot {
	snapshot := withColumn(t)
	snapshot.Tables[0].Columns[1].Name = "mail"

	return snapshot
}

// retypedColumn is that schema with the second column changed rather than renamed.
func retypedColumn(t *testing.T) schema.Snapshot {
	snapshot := withColumn(t)
	snapshot.Tables[0].Columns[1].Type = exactly(t, "text", "text")
	snapshot.Tables[0].Columns[1].Nullable = true

	return snapshot
}

// withIndex is the plain schema with an index over its one column.
func withIndex(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables[0].Indexes = []schema.Index{{Name: "idx_users_id", Columns: []string{"id"}}}
	})
}

// withUnique is the plain schema with a unique constraint over its one column.
func withUnique(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables[0].Uniques = []schema.Unique{{Name: "uq_users_id", Columns: []string{"id"}}}
	})
}

// withCheck is the plain schema with a check constraint on it.
func withCheck(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables[0].Checks = []schema.Check{{Name: "ck_users_id", Expression: "id > 0"}}
	})
}

// withPrimaryKey is the plain schema with a primary key over its one column.
func withPrimaryKey(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables[0].PrimaryKey = &schema.PrimaryKey{Name: "users_pkey", Columns: []string{"id"}}
	})
}

// withForeignKey is the plain schema with a second table referencing the first.
func withForeignKey(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Tables = append(s.Tables, schema.Table{
			Name:    "orders",
			Columns: []schema.Column{{Name: "user_id", Type: exactly(t, "int64", "bigint")}},
			ForeignKeys: []schema.ForeignKey{{Name: "fk_orders_user",
				Columns: []string{"user_id"}, ReferencesTable: "users",
				ReferencesColumns: []string{"id"}}},
		})
	})
}

// withView is the plain schema with a view over its table.
func withView(t *testing.T) schema.Snapshot {
	return changed(t, func(s *schema.Snapshot) {
		s.Views = []schema.View{{Name: "active_users", Query: "SELECT id FROM users"}}
	})
}

func TestEveryOperationHasADeclaredEffectOnASnapshot(t *testing.T) {
	for _, kind := range ir.Kinds() {
		declared, named := coverage[kind]
		if !named {
			t.Errorf("%s changes nothing this model declares, so a snapshot cannot be said to "+
				"represent what that operation does", kind)

			continue
		}

		if declared.note == "" {
			t.Errorf("%s declares no note saying how the model holds it", kind)
		}
	}

	for kind := range coverage {
		if !kind.Valid() {
			t.Errorf("the coverage map names %q, which is no operation in the v1 set", kind)
		}
	}

	if len(coverage) != len(ir.Kinds()) {
		t.Errorf("the coverage map holds %d kinds and the operation set has %d",
			len(coverage), len(ir.Kinds()))
	}
}

func TestOnlyRawChangesSomethingASnapshotCannotSee(t *testing.T) {
	for _, kind := range ir.Kinds() {
		declared := coverage[kind]

		opaque := declared.element == "" && declared.before == nil && declared.after == nil
		if opaque != (kind == ir.KindRaw) {
			t.Errorf("%s declares an opaque effect: %v, want %v", kind, opaque,
				kind == ir.KindRaw)
		}
	}
}

func TestEveryOperationsEffectIsVisibleInTheModel(t *testing.T) {
	for _, kind := range ir.Kinds() {
		declared := coverage[kind]
		if declared.before == nil {
			continue
		}

		t.Run(kind.String(), func(t *testing.T) {
			before, after := declared.before(t), declared.after(t)

			for _, s := range []schema.Snapshot{before, after} {
				if err := s.Validate(); err != nil {
					t.Fatalf("a schema this operation applies to is invalid: %v", err)
				}
			}

			if before.Equal(after) {
				t.Errorf("a snapshot before and after a %s compare equal, so nothing would "+
					"notice the operation ran (%s)", kind, declared.note)
			}
		})
	}
}

func TestEveryOperationsEffectSurvivesSerialization(t *testing.T) {
	for _, kind := range ir.Kinds() {
		declared := coverage[kind]
		if declared.after == nil {
			continue
		}

		t.Run(kind.String(), func(t *testing.T) {
			after := declared.after(t)

			encoded, err := schema.Marshal(after)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			decoded, err := schema.Unmarshal(encoded)
			if err != nil {
				t.Fatalf("Unmarshal(%s): %v", encoded, err)
			}

			if !decoded.Equal(after) {
				t.Errorf("the effect of a %s does not survive a round trip through JSON", kind)
			}

			if declared.before(t).Equal(decoded) {
				t.Errorf("a decoded %s equals the state before it ran", kind)
			}
		})
	}
}
