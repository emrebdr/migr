package postgres

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
)

// historyDDL is what migr's own history table is, in the dialect it is actually created in.
//
// The neutral schema is M2-T5's and the compiler is M3-T2's, so nothing here chooses anything --
// but the product of the two is the one thing an operator inspects and no golden file records.
// test/golden holds one file per IR operation kind, resolved from ir.Kinds(), so a sixteenth file
// for this would be a case the harness never asked for. Stating it here puts a change to migr's
// own table in a diff instead of leaving it to a live server to reveal.
const historyDDL = `CREATE TABLE "migr_migrations" (
    "batch" bigint NOT NULL,
    "ordinal" integer NOT NULL,
    "version" varchar(64) NOT NULL,
    "name" varchar(255) NOT NULL,
    "direction" varchar(8) NOT NULL,
    "outcome" varchar(16) NOT NULL,
    "checksum" varchar(255) NOT NULL,
    "applied_at" timestamptz NOT NULL,
    "duration_ms" bigint NOT NULL,
    "last_statement" integer NOT NULL,
    "error" text,
    "driver" varchar(64) NOT NULL,
    "runtime" varchar(32) NOT NULL,
    "engine_version" varchar(64) NOT NULL,
    "ir_version" integer NOT NULL,
    PRIMARY KEY ("batch", "ordinal")
);
CREATE INDEX "migr_migrations_version_idx" ON "migr_migrations" ("version");`

func TestTheHistoryTableIsCreatedAsThisDialectSpellsIt(t *testing.T) {
	store := pgHistory{table: driver.DefaultHistoryTable}

	stmts, err := store.compileSchema(history.Schema(store.table))
	if err != nil {
		t.Fatalf("compiling the history schema: %v", err)
	}

	rendered := make([]string, len(stmts))
	for i, stmt := range stmts {
		rendered[i] = stmt.SQL

		if stmt.NoTransaction {
			t.Errorf("statement %d opts out of the transaction Ensure runs it in", i+1)
		}
	}

	if got := strings.Join(rendered, "\n"); got != historyDDL {
		t.Errorf("migr's own history table is created as\n%s\n\nwant\n%s", got, historyDDL)
		t.Log("The column list is M2-T5's and the spellings are M3-T2's, so a change here means")
		t.Log("one of those moved -- read the diff before accepting it. checksum is varchar(255)")
		t.Log("rather than the 135 characters migr1 needs, so a scheme bump never has to migrate")
		t.Log("migr's own history table.")
	}
}

func TestTheHistorySchemaCompilesForEveryOperationItDeclares(t *testing.T) {
	ops := history.Schema(driver.DefaultHistoryTable)

	if len(ops) == 0 {
		t.Fatal("the neutral history schema emits no operations")
	}

	for _, op := range ops {
		t.Run(string(op.Kind()), func(t *testing.T) {
			if err := op.Validate(); err != nil {
				t.Fatalf("the schema emits an operation that is not well formed: %v", err)
			}

			stmts, err := compile(op)
			if err != nil {
				t.Fatalf("this driver cannot compile the history schema: %v", err)
			}

			if len(stmts) == 0 {
				t.Fatal("compiled to no statements at all")
			}
		})
	}
}
