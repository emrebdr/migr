package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

func TestEnsureCreatesTheTableTheIndexItsLookupNeedsAndTheCheckpointTable(t *testing.T) {
	_, held := ensured(t)

	objects := read(t, held.session,
		`SELECT type || ' ' || name FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY name`)

	var found []string
	for _, row := range objects.Values {
		found = append(found, *row[0])
	}

	slices.Sort(found)

	want := []string{"index " + historyTable + "_version_idx", "table " + historyTable,
		"table " + historyTable + "_progress", "table " + historyTable + "_releases",
		"table " + historyTable + "_shadows"}
	if !slices.Equal(found, want) {
		t.Errorf("Ensure left %v, want %v", found, want)
	}
}

func TestEnsureIsIdempotent(t *testing.T) {
	_, held := ensured(t)

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("the second Ensure: %v", err)
	}

	if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
		t.Fatalf("Record after two Ensures: %v", err)
	}
}

func TestEnsureFindsATableWhoseNameFoldsToAnotherCase(t *testing.T) {
	c := open(t, filepath.Join(t.TempDir(), "app.db"), driver.Options{HistoryTable: "MigrRuns"})

	held, ok := c.History().(sqliteHistory)
	if !ok {
		t.Fatalf("History returned %T", c.History())
	}

	exec(t, c, `CREATE TABLE "migrruns" (a integer)`)

	err := held.Ensure(t.Context())
	if !errors.Is(err, ErrHistoryShape) {
		t.Fatalf("Ensure returned %v, want %v", err, ErrHistoryShape)
	}

	t.Log("SQLite folds ASCII case in an identifier and compares sqlite_schema.name as BINARY, so a")
	t.Log("lookup without COLLATE NOCASE reports absent, Ensure runs CREATE TABLE, and SQLite refuses")
	t.Log("it as already existing -- forever, on every run. Measured, and PostgreSQL's quote_ident")
	t.Log("finding in a second dialect. Here the folded table is found and judged on its columns.")
}

func TestAHistoryNameHeldBySomethingOtherThanATableIsRefused(t *testing.T) {
	for name, setup := range map[string]string{
		"a view":    `CREATE VIEW ` + quote(historyTable) + ` AS SELECT 1 AS batch`,
		"a trigger": ``,
	} {
		t.Run(name, func(t *testing.T) {
			c, held := store(t)

			if setup == "" {
				exec(t, c, `CREATE TABLE other (a integer)`)
				exec(t, c, `CREATE TRIGGER `+quote(historyTable)+
					` AFTER INSERT ON other BEGIN SELECT 1; END`)
			} else {
				exec(t, c, setup)
			}

			err := held.Ensure(t.Context())
			if !errors.Is(err, ErrHistoryNotATable) {
				t.Fatalf("Ensure returned %v, want %v", err, ErrHistoryNotATable)
			}
		})
	}
}

func TestAReadOverAHistoryNameHeldByAViewIsRefusedRatherThanAnswered(t *testing.T) {
	c, held := store(t)

	exec(t, c, `CREATE VIEW `+quote(historyTable)+` AS SELECT 1 AS batch`)

	if _, err := held.List(t.Context()); !errors.Is(err, ErrHistoryNotATable) {
		t.Errorf("List over a view returned %v, want %v", err, ErrHistoryNotATable)
	}

	t.Log("A view is selectable, so a read that only checked for presence would answer from one.")
}

func TestATableMissingAColumnThisBuildWritesIsNamedRatherThanWrittenTo(t *testing.T) {
	c, held := store(t)

	exec(t, c, `CREATE TABLE `+quote(historyTable)+` (batch integer, ordinal integer)`)

	err := held.Ensure(t.Context())
	if !errors.Is(err, ErrHistoryShape) {
		t.Fatalf("Ensure returned %v, want %v", err, ErrHistoryShape)
	}

	for _, column := range []string{"version", "checksum", "applied_at", "ir_version"} {
		if !strings.Contains(err.Error(), column) {
			t.Errorf("the refusal does not name the missing column %q: %v", column, err)
		}
	}
}

func TestAnExtraColumnIsFineAndAGeneratedOneIsNot(t *testing.T) {
	c, held := store(t)

	exec(t, c, `CREATE TABLE `+quote(historyTable)+` (`+
		strings.Join(declaredColumnsWithTypes(t), ", ")+`, note text)`)

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure refused a table with an extra column: %v", err)
	}

	generated, heldGenerated := store(t)

	exec(t, generated, `CREATE TABLE `+quote(historyTable)+` (`+
		strings.Join(declaredColumnsWithTypes(t)[:14], ", ")+
		`, ir_version integer GENERATED ALWAYS AS (1))`)

	err := heldGenerated.Ensure(t.Context())
	if !errors.Is(err, ErrHistoryShape) {
		t.Fatalf("Ensure accepted a generated column: %v", err)
	}

	if !strings.Contains(err.Error(), "ir_version") {
		t.Errorf("the refusal does not name the generated column: %v", err)
	}

	t.Log("pragma_table_info omits a generated column and pragma_table_xinfo includes it -- measured,")
	t.Log("along with SQLite refusing an INSERT into one. So table_info is the right pragma: a")
	t.Log("generated column counts as absent, where xinfo would call a table that fails every write")
	t.Log("the right shape.")
}

// declaredColumnsWithTypes renders the history columns as a column list, for a hand-built table.
func declaredColumnsWithTypes(t *testing.T) []string {
	t.Helper()

	names := declaredColumns(historyTable)

	declared := make([]string, len(names))
	for i, name := range names {
		declared[i] = quote(name) + " text"
	}

	return declared
}

func TestTheBoundColumnOrderIsTheOneTheNeutralSchemaDeclares(t *testing.T) {
	declared := declaredColumns(historyTable)

	if !slices.Equal(historyColumns, declared) {
		t.Errorf("this store binds\n%v\nand the neutral schema declares\n%v", historyColumns, declared)
		t.Log("The insert and the scan share one ordered list, so a transposition here writes every")
		t.Log("run's fields into the wrong columns and reads them back consistently wrong.")
	}
}

func TestTheHistorySchemaCompilesThroughThisDriverAndNothingElse(t *testing.T) {
	_, held := store(t)

	stmts, err := held.compileSchema(history.Schema(held.table))
	if err != nil {
		t.Fatalf("compileSchema: %v", err)
	}

	if len(stmts) != len(history.Schema(historyTable)) {
		t.Errorf("the schema compiles to %d statements, want %d", len(stmts),
			len(history.Schema(historyTable)))
	}

	for _, stmt := range stmts {
		if !strings.Contains(stmt.SQL, quote(historyTable)) {
			t.Errorf("a compiled statement does not name the history table: %s", stmt.SQL)
		}
	}
}

func TestTheHistoryTableIsCreatedAsOneUnitRatherThanStatementByStatement(t *testing.T) {
	c, held := store(t)

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		return held.Ensure(ctx)
	}); err != nil {
		t.Fatalf("Ensure inside a caller's transaction: %v", err)
	}

	if err := held.Record(t.Context(), run(t, 1, 1)); err != nil {
		t.Fatalf("Record after it: %v", err)
	}

	t.Log("Ensure opens a transaction only when the session has none, as PostgreSQL's does: ADR 0017")
	t.Log("makes a second one a refusal, and Ensure is called from paths that may already be inside")
	t.Log("one.")
}

func TestAnEnsureThatFailsPartWayLeavesNoTableBehind(t *testing.T) {
	c, held := store(t)

	exec(t, c, `CREATE TABLE decoy (a integer)`)
	exec(t, c, `CREATE INDEX `+quote(historyTable+"_version_idx")+` ON decoy (a)`)

	if err := held.Ensure(t.Context()); err == nil {
		t.Fatal("Ensure succeeded with its index name already taken")
	}

	kind, found, err := held.kind(t.Context())
	if err != nil {
		t.Fatalf("looking for the table afterwards: %v", err)
	}

	if found {
		t.Errorf("a failed Ensure left a %s behind, so the schema statements did not run as one unit",
			kind)
		t.Log("The CREATE TABLE succeeds and the CREATE INDEX fails on the name this decoy holds. A")
		t.Log("table left behind here is worse than the failure: the next run finds it, reads it as")
		t.Log("the history, and meets a table with no index on version.")
	}
}

func TestOnlyACreateTableCarriesTheDeclaredColumns(t *testing.T) {
	if declaredColumns("nothing_declares_this") == nil {
		t.Fatal("the neutral schema declares no columns at all")
	}

	var tables int

	for _, op := range history.Schema(historyTable) {
		if _, ok := op.(ir.CreateTable); ok {
			tables++
		}
	}

	if tables != 1 {
		t.Errorf("the neutral schema creates %d tables, and declaredColumns reads the first", tables)
	}
}
