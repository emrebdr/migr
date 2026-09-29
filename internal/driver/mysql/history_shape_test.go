package mysql

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// historyTable is the name these tests record under, which is the contract's own default.
const historyTable = driver.DefaultHistoryTable

func TestTheBoundColumnOrderIsTheOneTheNeutralSchemaDeclares(t *testing.T) {
	declared := declaredColumns(historyTable)

	if !slices.Equal(historyColumns, declared) {
		t.Errorf("this store binds\n%v\nand the neutral schema declares\n%v", historyColumns, declared)
		t.Log("The insert and the scan share one ordered list, so a transposition here writes every")
		t.Log("run's fields into the wrong columns and reads them back consistently wrong.")
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

	if len(afterTable(historyTable)) != len(history.Schema(historyTable))-tables {
		t.Errorf("afterTable does not return everything but the table")
	}

	t.Log("complete() repeats what afterTable returns, so a second table in the neutral schema would")
	t.Log("be repeated rather than created once -- which this holds to one.")
}

func TestTheRepeatableHalfOfTheSchemaIsEverythingButTheTable(t *testing.T) {
	rest := afterTable(historyTable)

	if len(rest) == 0 {
		t.Fatal("there is nothing after the table, so a half-built installation cannot be finished")
	}

	for _, op := range rest {
		if _, isTable := op.(ir.CreateTable); isTable {
			t.Errorf("afterTable returned the table itself")
		}
	}

	t.Log("MySQL commits on every DDL statement, so a run interrupted between CREATE TABLE and")
	t.Log("CREATE INDEX leaves a table with no index on version and nothing can roll it back.")
	t.Log("Ensure repeats this half and treats an index already there as done.")
}

func TestTheMissingColumnsAreNamedInDeclarationOrder(t *testing.T) {
	present := map[string]bool{"batch": true, "ordinal": true}

	missing := missingColumns(declaredColumns(historyTable), present)

	if len(missing) != len(historyColumns)-2 {
		t.Fatalf("missingColumns found %d, want %d", len(missing), len(historyColumns)-2)
	}

	if missing[0] != "version" {
		t.Errorf("the missing columns are not in declaration order: %v", missing)
	}
}

func TestAnInstantSurvivesTheRenderingAndTheParseAtMicrosecondResolution(t *testing.T) {
	at := time.Date(2026, 9, 29, 11, 22, 33, 123456789,
		time.FixedZone("UTC+5:30", 5*3600+30*60))

	rendered := renderTime(at)
	if rendered != "2026-09-29 05:52:33.123456" {
		t.Fatalf("rendered %q", rendered)
	}

	back, err := parseTime(rendered)
	if err != nil {
		t.Fatalf("parseTime: %v", err)
	}

	if !back.Equal(at.UTC().Truncate(time.Microsecond)) {
		t.Errorf("read back %s, want %s", back, at.UTC().Truncate(time.Microsecond))
	}

	if back.Location() != time.UTC {
		t.Errorf("read back in %s, and only UTC is ever written", back.Location())
	}

	t.Log("datetime(6) keeps microseconds, so the nanoseconds go. Truncating here rather than")
	t.Log("handing the server nine digits is what makes the two engines agree: MySQL rounds the")
	t.Log("extra digits up and MariaDB truncates them, so the same instant would be two different")
	t.Log("microseconds. Both measured.")
}

func TestAStoredInstantIsFixedWidthSoItSortsLikeTheInstant(t *testing.T) {
	early := renderTime(time.Date(2026, 9, 29, 11, 22, 33, 0, time.UTC))
	late := renderTime(time.Date(2026, 9, 29, 11, 22, 33, 500000000, time.UTC))

	if len(early) != len(late) {
		t.Fatalf("%q and %q are different widths", early, late)
	}

	if early >= late {
		t.Errorf("%q does not sort before %q", early, late)
	}
}

func TestAnInstantIsRenderedInMySQLsOwnLiteralFormRatherThanRFC3339(t *testing.T) {
	rendered := renderTime(time.Date(2026, 9, 29, 11, 22, 33, 0, time.UTC))

	if strings.ContainsAny(rendered, "TZ") {
		t.Errorf("rendered %q, and an RFC3339 string is error 1292 on both servers", rendered)
	}
}

func TestTheInsertAndTheSelectNameEveryColumnOnce(t *testing.T) {
	h := mysqlHistory{table: historyTable, stored: historyTable}

	insert, _, _ := strings.Cut(h.insertStatement(), "\nVALUES")
	read, _, _ := strings.Cut(h.selectStatement(runOrder), "\nFROM")

	for _, column := range historyColumns {
		if strings.Count(insert, quote(column)) != 1 {
			t.Errorf("the insert names %q %d times", column, strings.Count(insert, quote(column)))
		}

		if strings.Count(read, quote(column)) != 1 {
			t.Errorf("the select names %q %d times", column, strings.Count(read, quote(column)))
		}
	}

	insert = h.insertStatement()

	if strings.Count(insert, "?") != len(historyColumns) {
		t.Errorf("the insert binds %d values for %d columns", strings.Count(insert, "?"),
			len(historyColumns))
	}
}

func TestTheStoredNameFollowsHowTheServerFoldsIdentifiers(t *testing.T) {
	for folding, want := range map[int]string{0: "MigrRuns", 1: "migrruns", 2: "MigrRuns"} {
		if got := storedName("MigrRuns", folding); got != want {
			t.Errorf("under lower_case_table_names=%d the catalog is asked for %q, want %q",
				folding, got, want)
		}
	}

	t.Log("Only setting 1 changes what the catalog holds: it lowercases a name on the way in, which")
	t.Log("is the Windows default and what a MySQL image does when told to. Asking for the wrong")
	t.Log("spelling means Ensure reports the table absent and tries to create it on every run,")
	t.Log("forever. SQLite's COLLATE NOCASE finding in a third dialect, from the opposite direction.")
}
