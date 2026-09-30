package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

func TestOnlyTheFourTypesMySQLAcceptsACharacterSetOnCarryOne(t *testing.T) {
	for spelling, want := range map[string]bool{
		"string(10)":    true,
		"text":          true,
		"uuid":          true,
		"enum(a,b)":     true,
		"json":          false,
		"binary":        false,
		"int32":         false,
		"int64":         false,
		"int16":         false,
		"bool":          false,
		"float32":       false,
		"float64":       false,
		"decimal(10,2)": false,
		"timestamp":     false,
		"timestamptz":   false,
		"date":          false,
		"time":          false,
		"array(int32)":  false,
	} {
		if got := collated(mustType(t, spelling)); got != want {
			t.Errorf("collated(%s) is %v, want %v", spelling, got, want)
		}
	}

	t.Log("Measured rather than reasoned: varchar, longtext, char and enum accept CHARACTER SET and")
	t.Log("json, longblob and every numeric and temporal type answer error 1064 for the same clause,")
	t.Log("on both servers. So this is a list of what MySQL accepts and not a guess at what is")
	t.Log("textual -- MariaDB's json is itself a longtext and still refuses the clause.")
}

func TestBothCharacterSetSwitchesNameEveryKindRatherThanDefaulting(t *testing.T) {
	for _, spelling := range []string{
		"string(1)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
		"decimal(1,0)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
		"enum(a)", "array(int32)",
	} {
		typ := mustType(t, spelling)

		collated(typ)
		indexable(typ)
	}

	t.Log("Neither switch carries a default, which is what makes this mechanical rather than a claim:")
	t.Log(".golangci.yml sets default-signifies-exhaustive, so a switch without one must name every")
	t.Log("schema.Kind or exhaustive fails the build. Removing schema.KindJSON from collated's list")
	t.Log("reports \"missing cases in switch of type schema.Kind\" -- checked. A nineteenth logical")
	t.Log("type therefore cannot quietly inherit an answer about whether it carries a character set,")
	t.Log("which for indexable is the unsafe direction: defaulting to indexable would send MySQL a key")
	t.Log("it refuses rather than refusing it here.")
}

func TestACollatedColumnStatesTheCharacterSetAndAnUncollatedOneStatesNothing(t *testing.T) {
	collatedColumn := columnDefinition(ir.Column{Name: "e", Type: mustType(t, "string(64)")})
	if !strings.Contains(collatedColumn, "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin") {
		t.Errorf("a varchar column declares no character set: %s", collatedColumn)
	}

	plain := columnDefinition(ir.Column{Name: "n", Type: mustType(t, "int32")})
	if strings.Contains(plain, "CHARACTER SET") {
		t.Errorf("an int column declares a character set MySQL refuses: %s", plain)
	}

	if !strings.HasSuffix(tableOptions(), "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin") {
		t.Errorf("a table declares %q", tableOptions())
	}

	t.Log("Both halves are needed and the column one is load-bearing: a column migr adds to a table")
	t.Log("migr did not create inherits that table's character set otherwise, which changes what the")
	t.Log("column can hold, how wide it is in bytes, and whether it fits in a key.")
}

func TestTheCollationIsPinnedSoOneMigrationMeansOneThingOnEveryEngine(t *testing.T) {
	if collation != "utf8mb4_bin" {
		t.Fatalf("the collation is %q", collation)
	}

	t.Log("utf8mb4_bin exists on MySQL 8.0.46 and MariaDB 11.4.13; their own defaults do not agree")
	t.Log("with each other -- utf8mb4_0900_ai_ci and utf8mb4_uca1400_ai_ci, measured -- and both are")
	t.Log("case-insensitive. Under either default, UNIQUE(email) refuses 'Bob@x.com' beside")
	t.Log("'bob@x.com' where PostgreSQL and SQLite accept both, so the same migration would declare a")
	t.Log("different constraint on each engine. The cost is stated in doc.go: equality is")
	t.Log("case-sensitive and ORDER BY is code-point order, where a MySQL operator may expect neither.")
}

func TestAStringWiderThanOneMySQLColumnHoldsIsRefusedWithNoDatabase(t *testing.T) {
	_, err := compile(ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "bio", Type: mustType(t, "string(20000)")},
	}})

	if !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
	}

	if !strings.Contains(err.Error(), "16383") || !strings.Contains(err.Error(), "1074") {
		t.Errorf("the refusal does not name the limit or the error: %v", err)
	}

	if _, err := compile(ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "bio", Type: mustType(t, "string(16383)")},
	}}); err != nil {
		t.Errorf("the widest varchar MySQL holds was refused: %v", err)
	}

	t.Log("The server states the number itself: error 1074 reads \"Column length too big for column")
	t.Log("'c' (max = 16383)\" on both, and varchar(16383) is accepted where varchar(20000) is not.")
	t.Log("It is 65535/4 because the character set is pinned rather than inherited, which is what")
	t.Log("makes this decidable here at all.")
}

func TestAPrimaryKeyMySQLWillNotBuildIsRefusedWhereTheTypesAreInTheOperation(t *testing.T) {
	for name, c := range map[string]struct {
		op   ir.CreateTable
		says string
	}{
		"a text key column": {
			op: ir.CreateTable{Table: "posts", Columns: []ir.Column{
				{Name: "body", Type: mustType(t, "text")},
			}, PrimaryKey: []string{"body"}},
			says: "1170",
		},
		"a binary key column": {
			op: ir.CreateTable{Table: "blobs", Columns: []ir.Column{
				{Name: "bytes", Type: mustType(t, "binary")},
			}, PrimaryKey: []string{"bytes"}},
			says: "1170",
		},
		"a key wider than InnoDB indexes": {
			op: ir.CreateTable{Table: "wide", Columns: []ir.Column{
				{Name: "a", Type: mustType(t, "string(500)")},
				{Name: "b", Type: mustType(t, "string(500)")},
			}, PrimaryKey: []string{"a", "b"}},
			says: "1071",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(c.op)
			if !errors.Is(err, driver.ErrUnsupported) {
				t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not name error %s: %v", c.says, err)
			}
		})
	}

	t.Log("create_table is the one operation carrying its columns' types, so the same two limits are")
	t.Log("decidable with no database. add_index, add_unique and add_primary_key name columns only,")
	t.Log("and the server's own refusal is what an operator sees for those.")
	t.Log("")
	t.Log("A primary key over a longtext is error 1170 on MariaDB as well, measured -- where a plain")
	t.Log("index over one is accepted there. So this refusal is not MySQL's alone.")
}

func TestAKeyJustInsideTheLimitIsBuilt(t *testing.T) {
	stmts, err := compile(ir.CreateTable{Table: "wide", Columns: []ir.Column{
		{Name: "a", Type: mustType(t, "string(768)")},
	}, PrimaryKey: []string{"a"}})
	if err != nil {
		t.Fatalf("a 3072-byte key was refused: %v", err)
	}

	if len(stmts) != 1 {
		t.Fatalf("compiled %d statements", len(stmts))
	}

	if _, err := compile(ir.CreateTable{Table: "wide", Columns: []ir.Column{
		{Name: "a", Type: mustType(t, "string(769)")},
	}, PrimaryKey: []string{"a"}}); err == nil {
		t.Error("a 3076-byte key was accepted, and InnoDB indexes 3072")
	}

	t.Log("768 characters is exactly 3072 bytes under utf8mb4, so the boundary is tested on both")
	t.Log("sides rather than only on the side that fails.")
}

func TestSetDefaultIsRefusedWithTheMeasurementRatherThanTheGenericReason(t *testing.T) {
	for name, op := range map[string]ir.Operation{
		"on delete": ir.AddForeignKey{Table: "child", Name: "fk", Columns: []string{"pid"},
			ReferencesTable: "parent", ReferencesColumns: []string{"id"},
			OnDelete: ir.ActionSetDefault},
		"on update": ir.AddForeignKey{Table: "child", Name: "fk", Columns: []string{"pid"},
			ReferencesTable: "parent", ReferencesColumns: []string{"id"},
			OnUpdate: ir.ActionSetDefault},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(op)
			if !errors.Is(err, driver.ErrUnsupported) {
				t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
			}

			for _, phrase := range []string{"1451", "SHOW CREATE TABLE", "MariaDB drops it silently",
				"PostgreSQL sets the column to its default"} {
				if !strings.Contains(err.Error(), phrase) {
					t.Errorf("the refusal does not mention %q: %v", phrase, err)
				}
			}
		})
	}

	t.Log("The message is the whole value of this refusal, so it is asserted rather than only the")
	t.Log("sentinel. Falling through to \"this driver has no MySQL spelling for it\" would still refuse")
	t.Log("and would tell an operator nothing: the clause is valid SQL on both servers, and what is")
	t.Log("wrong is that neither performs it while one records it anyway.")
}

func TestAnActionThisDriverDoesKnowIsNotCaughtByThatRefusal(t *testing.T) {
	for _, action := range []ir.ReferentialAction{
		ir.ActionNoAction, ir.ActionRestrict, ir.ActionCascade, ir.ActionSetNull, "",
	} {
		if problem := checkAction("on_delete", action); problem != "" {
			t.Errorf("%q is refused: %s", action, problem)
		}
	}

	if problem := checkAction("on_delete", "invented"); problem == "" {
		t.Error("an action nothing declares was accepted")
	}
}

func TestAUUIDKeyIsMeasuredRatherThanCountedAsNothing(t *testing.T) {
	if got := keyBytes(mustType(t, "uuid")); got != uuidCharacters*bytesPerCharacter {
		t.Errorf("a uuid contributes %d bytes to a key, want %d", got,
			uuidCharacters*bytesPerCharacter)
	}

	t.Log("A uuid is a char(36) and carries a character set, so it costs key bytes like a varchar.")
	t.Log("Counting it as zero would let a wide composite key through to the server.")
}
