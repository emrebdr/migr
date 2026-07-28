package history

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// tableColumns is the history table restated independently, so a change has to be made twice.
var tableColumns = []string{
	"batch", "ordinal", "version", "name", "direction", "outcome", "checksum",
	"applied_at", "duration_ms", "last_statement", "error", "driver", "runtime",
	"engine_version", "ir_version",
}

// createdBy returns the create_table operation a schema begins with.
func createdBy(t *testing.T, ops ir.Operations) ir.CreateTable {
	t.Helper()

	for _, op := range ops {
		if created, ok := op.(ir.CreateTable); ok {
			return created
		}
	}

	t.Fatal("the schema creates no table")

	return ir.CreateTable{}
}

func TestEveryOperationTheSchemaEmitsIsWellFormed(t *testing.T) {
	ops := Schema("migr_migrations")

	if len(ops) == 0 {
		t.Fatal("the schema emits no operations")
	}

	for _, op := range ops {
		if err := op.Validate(); err != nil {
			t.Errorf("the %s operation is not well formed: %v", op.Kind(), err)
			t.Log("Every schema.Parse call here returns an error this package discards, because")
			t.Log("the spellings are compile-time constants and panic is banned. A misspelling")
			t.Log("leaves a zero schema.Type, which Column.Validate rejects -- this test is what")
			t.Log("makes that a build failure instead of a table with an untyped column.")
		}
	}
}

func TestTheSchemaUsesTheTableItWasGiven(t *testing.T) {
	ops := Schema("audit_log")

	for _, op := range ops {
		if !strings.Contains(mustMarshal(t, op), "audit_log") {
			t.Errorf("the %s operation ignores the configured table name", op.Kind())
			t.Log("history.table is configurable and M12-T1 gives seeds a second table of the")
			t.Log("same shape, so a hard-coded name would put seed runs in the schema history.")
		}
	}

	if name := indexName(t, ops); !strings.HasPrefix(name, "audit_log") {
		t.Errorf("the index is named %q, which does not follow its table", name)
	}
}

func TestTheSchemaHasAColumnForEveryRecordField(t *testing.T) {
	created := createdBy(t, Schema("migr_migrations"))

	if len(created.Columns) != reflect.TypeOf(Record{}).NumField() {
		t.Errorf("the table has %d columns and Record has %d fields",
			len(created.Columns), reflect.TypeOf(Record{}).NumField())
		t.Log("A field with no column is silently dropped on write and silently zero on read.")
		t.Log("Nothing else keeps the struct and the table in step, so adding one to either")
		t.Log("has to fail here until it is added to both.")
	}

	for i, column := range created.Columns {
		if i < len(tableColumns) && column.Name != tableColumns[i] {
			t.Errorf("column %d is %q, want %q", i, column.Name, tableColumns[i])
		}
	}
}

func TestTheSchemaIsDeterministic(t *testing.T) {
	first, again := mustMarshalAll(t, Schema("migr_migrations")), mustMarshalAll(t, Schema("migr_migrations"))

	if !bytes.Equal(first, again) {
		t.Error("two calls produced different operations")
		t.Log("Invariant #8: anything producing IR is deterministic. A map ranged here would")
		t.Log("emit columns in a different order per run, and the history table would be")
		t.Log("created differently depending on which process got there first.")
	}
}

func TestTheSchemaNamesNoNativeType(t *testing.T) {
	for _, column := range createdBy(t, Schema("migr_migrations")).Columns {
		if column.Type.Zero() {
			t.Errorf("column %q has no type", column.Name)

			continue
		}

		if _, err := schema.Parse(column.Type.String()); err != nil {
			t.Errorf("column %q is typed %q, which is not a logical type: %v",
				column.Name, column.Type, err)
			t.Log("A native spelling here would make the history table PostgreSQL's shape, and")
			t.Log("SQLite and MySQL would each need their own -- which is the database")
			t.Log("independence this table exists inside of.")
		}
	}
}

// mustMarshal renders one operation as IR, failing the test if it will not encode.
func mustMarshal(t *testing.T, op ir.Operation) string {
	t.Helper()

	encoded, err := ir.Marshal(op)
	if err != nil {
		t.Fatalf("ir.Marshal: %v", err)
	}

	return string(encoded)
}

// mustMarshalAll renders a whole schema as IR, which is how two of them are compared.
func mustMarshalAll(t *testing.T, ops ir.Operations) []byte {
	t.Helper()

	encoded, err := ir.Marshal(ops)
	if err != nil {
		t.Fatalf("ir.Marshal: %v", err)
	}

	return encoded
}

// indexName returns the name of the lookup index a schema adds.
func indexName(t *testing.T, ops ir.Operations) string {
	t.Helper()

	for _, op := range ops {
		if index, ok := op.(ir.AddIndex); ok {
			return index.Name
		}
	}

	t.Fatal("the schema adds no index, so every status lookup is a full scan")

	return ""
}
