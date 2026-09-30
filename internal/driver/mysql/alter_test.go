package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// typePointer returns the address of a logical type, which ir.AlterColumn carries as a pointer.
func typePointer(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	typ := mustType(t, spelling)

	return &typ
}

// boolPointer returns the address of a nullability, which ir.AlterColumn carries as a pointer.
func boolPointer(v bool) *bool {
	return &v
}

func TestOnlyDroppingADefaultIsCompiledWithoutTheDatabase(t *testing.T) {
	for name, c := range map[string]struct {
		op    ir.AlterColumn
		needs bool
	}{
		"dropping a default": {
			op: ir.AlterColumn{Table: "t", Column: "c", DropDefault: true},
		},
		"setting a default": {
			op: ir.AlterColumn{Table: "t", Column: "c",
				Default: &ir.Default{Literal: literal("x")}},
			needs: true,
		},
		"changing the type": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "int64")},
			needs: true,
		},
		"changing the nullability": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Nullable: boolPointer(true)},
			needs: true,
		},
		"dropping a default beside a retype": {
			op: ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "int64"),
				DropDefault: true},
			needs: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(c.op)

			if c.needs != errors.Is(err, driver.ErrNeedsSession) {
				t.Fatalf("compile returned %v, and needing a database is %v", err, c.needs)
			}
		})
	}

	t.Log("DROP DEFAULT is the one alteration naming no value, so it is the one needing no type. It is")
	t.Log("accepted on every native type this driver emits, including a column that had no default to")
	t.Log("drop -- measured on both servers.")
	t.Log("")
	t.Log("Setting one is not, and that is the measurement that decided the split: ALTER COLUMN ... SET")
	t.Log("DEFAULT is error 1101 on a text, blob or json column on MySQL for the bare form AND the")
	t.Log("parenthesised one, and a bare function default is error 1064 there. Which form is legal is a")
	t.Log("fact about the column's type, and ir.AlterColumn names a column rather than a type.")
}

func TestTheRedefinitionReasonSaysWhatItReadsAndWhy(t *testing.T) {
	_, err := compile(ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "int64")})

	var needs *driver.NeedsSessionError
	if !errors.As(err, &needs) {
		t.Fatalf("compile returned %v", err)
	}

	for _, phrase := range []string{"MODIFY COLUMN", "1101", "NOT NULL", "DEFAULT"} {
		if !strings.Contains(needs.Reason, phrase) {
			t.Errorf("the reason does not mention %q: %s", phrase, needs.Reason)
		}
	}
}

func TestAnAlterationAskingForNothingIsRefusedRatherThanCompiledToNoStatements(t *testing.T) {
	_, err := compile(ir.AlterColumn{Table: "t", Column: "c"})
	if !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
	}

	if !strings.Contains(err.Error(), "no change") {
		t.Errorf("the refusal does not say it asks for nothing: %v", err)
	}
}

func TestAnAlterationThatBothSetsAndDropsADefaultIsRefused(t *testing.T) {
	_, err := compile(ir.AlterColumn{Table: "t", Column: "c", DropDefault: true,
		Default: &ir.Default{Literal: literal("x")}})

	if !errors.Is(err, driver.ErrUnsupported) {
		t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
	}
}

func TestARedefinitionStatesNullabilityInBothDirections(t *testing.T) {
	nullable := redefinition{table: "t", column: "c", declared: "int", nullable: true}
	if got := nullable.render(); !strings.HasSuffix(got, "`c` int NULL") {
		t.Errorf("rendered %q", got)
	}

	notNull := redefinition{table: "t", column: "c", declared: "int"}
	if got := notNull.render(); !strings.HasSuffix(got, "`c` int NOT NULL") {
		t.Errorf("rendered %q", got)
	}

	t.Log("MODIFY COLUMN is a full redefinition and omitting NOT NULL makes the column nullable, so")
	t.Log("the nullable case is spelled rather than left out: a reader sees which answer this driver")
	t.Log("decided rather than which clause it forgot.")
}

func TestARedefinitionRestatesTheCollationOnlyWhereTheColumnCarriesOne(t *testing.T) {
	with := redefinition{table: "t", column: "c", declared: "varchar(10)", collate: true}
	if !strings.Contains(with.render(), "CHARACTER SET utf8mb4 COLLATE utf8mb4_bin") {
		t.Errorf("rendered %q", with.render())
	}

	without := redefinition{table: "t", column: "c", declared: "int"}
	if strings.Contains(without.render(), "CHARACTER SET") {
		t.Errorf("rendered %q, and MySQL refuses that clause on an int", without.render())
	}

	t.Log("A redefinition that dropped the clause would send the column back to the table's default")
	t.Log("collation, which is what this driver pinned it away from.")
}

func TestRestateKeepsWhatTheOperationDoesNotName(t *testing.T) {
	found := existingColumn{declared: "varchar(255)", data: "varchar", nullable: false,
		collation: collation}

	out, err := restate(ir.AlterColumn{Table: "users", Column: "email",
		Nullable: boolPointer(true)}, found)
	if err != nil {
		t.Fatalf("restate: %v", err)
	}

	if out.declared != "varchar(255)" {
		t.Errorf("the type became %q, and the operation did not ask to change it", out.declared)
	}

	if !out.nullable {
		t.Error("the nullability the operation asked for was not applied")
	}

	if !out.collate {
		t.Error("the collation the column carries was not restated, so it would revert")
	}

	t.Log("The kept type is the server's own COLUMN_TYPE rather than anything this driver re-derives,")
	t.Log("which is what makes it faithful: MariaDB renders int(11) and MySQL int, and restating each")
	t.Log("server's own text cannot be wrong about it.")
}

func TestRestateTakesTheOperationsTypeAndItsCollationVerdictTogether(t *testing.T) {
	found := existingColumn{declared: "varchar(255)", data: "varchar", collation: collation}

	out, err := restate(ir.AlterColumn{Table: "users", Column: "email",
		Type: typePointer(t, "int64")}, found)
	if err != nil {
		t.Fatalf("restate: %v", err)
	}

	if out.declared != "bigint" {
		t.Errorf("the type became %q", out.declared)
	}

	if out.collate {
		t.Error("a bigint restated a character set, which MySQL refuses with error 1064")
	}

	t.Log("The two move together: a retype from a collated type to an uncollated one must drop the")
	t.Log("clause, and the other way round must add it. Taking the verdict from the old column would")
	t.Log("send MySQL a bigint with a CHARACTER SET.")
}

func TestADefaultTheOperationDoesNotReplaceIsRefusedRatherThanDropped(t *testing.T) {
	found := existingColumn{declared: "varchar(255)", data: "varchar", hasDefault: true,
		collation: collation}

	_, err := restate(ir.AlterColumn{Table: "users", Column: "email",
		Type: typePointer(t, "string(512)")}, found)

	if !errors.Is(err, ErrRedefine) {
		t.Fatalf("restate returned %v, want %v", err, ErrRedefine)
	}

	for _, phrase := range []string{"backslash", "NO_BACKSLASH_ESCAPES", "Set default"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("the refusal does not mention %q: %v", phrase, err)
		}
	}

	t.Log("The refusal names the fix, which is the point: adding default to the operation makes the")
	t.Log("migration state what the default should be, and that is more correct than any guess this")
	t.Log("driver could make from a field that does not round trip.")
}

func TestADefaultTheOperationReplacesIsRenderedForTheColumnsOwnType(t *testing.T) {
	for name, c := range map[string]struct {
		op    ir.AlterColumn
		found existingColumn
		want  string
	}{
		"a varchar the operation does not retype": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Default: &ir.Default{Literal: literal("x")}},
			found: existingColumn{declared: "varchar(10)", data: "varchar"},
			want:  "DEFAULT 'x'",
		},
		"a longtext the operation does not retype": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Default: &ir.Default{Literal: literal("x")}},
			found: existingColumn{declared: "longtext", data: "longtext"},
			want:  "DEFAULT ('x')",
		},
		"MariaDB's json, which is a longtext": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Default: &ir.Default{Literal: literal("{}")}},
			found: existingColumn{declared: "longtext", data: "longtext"},
			want:  "DEFAULT ('{}')",
		},
		"MySQL's json": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Default: &ir.Default{Literal: literal("{}")}},
			found: existingColumn{declared: "json", data: "json"},
			want:  "DEFAULT ('{}')",
		},
		"a blob": {
			op:    ir.AlterColumn{Table: "t", Column: "c", Default: &ir.Default{Literal: literal("z")}},
			found: existingColumn{declared: "longblob", data: "longblob"},
			want:  "DEFAULT ('z')",
		},
		"a retype away from text takes the new type's answer": {
			op: ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "string(10)"),
				Default: &ir.Default{Literal: literal("x")}},
			found: existingColumn{declared: "longtext", data: "longtext"},
			want:  "DEFAULT 'x'",
		},
		"a retype into text takes the new type's answer": {
			op: ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "text"),
				Default: &ir.Default{Literal: literal("x")}},
			found: existingColumn{declared: "varchar(10)", data: "varchar"},
			want:  "DEFAULT ('x')",
		},
	} {
		t.Run(name, func(t *testing.T) {
			out, err := restate(c.op, c.found)
			if err != nil {
				t.Fatalf("restate: %v", err)
			}

			if out.clause != c.want {
				t.Errorf("rendered %q, want %q", out.clause, c.want)
			}
		})
	}

	t.Log("The catalog's DATA_TYPE is what answers where the operation carries no type, and the")
	t.Log("operation's own type answers where it does -- because that is what the column is about to")
	t.Log("become. MariaDB's json arrives here as longtext, which the suffix match catches.")
}

func TestDroppingADefaultInsideARedefinitionRendersNoClause(t *testing.T) {
	out, err := restate(ir.AlterColumn{Table: "t", Column: "c", Type: typePointer(t, "int64"),
		DropDefault: true}, existingColumn{declared: "int", data: "int", hasDefault: true})
	if err != nil {
		t.Fatalf("restate: %v", err)
	}

	if out.clause != "" {
		t.Errorf("rendered %q, and the operation asked for the default to go", out.clause)
	}

	t.Log("A redefinition drops every attribute it does not restate, so dropping a default is what")
	t.Log("restating nothing already does -- and the existing default is then not one this has to")
	t.Log("read back, which is why this case is not refused.")
}

func TestATypeFamilyIsMatchedOnItsSuffixRatherThanItsWidth(t *testing.T) {
	for data, want := range map[string]bool{
		"tinytext":   true,
		"text":       true,
		"mediumtext": true,
		"longtext":   true,
		"tinyblob":   true,
		"blob":       true,
		"longblob":   true,
		"json":       true,
		"geometry":   true,
		"LONGTEXT":   true,
		"varchar":    false,
		"char":       false,
		"int":        false,
		"enum":       false,
		"datetime":   false,
		"decimal":    false,
	} {
		if got := expressionOnlyType(data); got != want {
			t.Errorf("expressionOnlyType(%q) is %v, want %v", data, got, want)
		}
	}
}
