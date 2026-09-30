package mysql

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// mustType parses a logical type by its canonical spelling, failing on a spelling nothing carries.
func mustType(t *testing.T, spelling string) schema.Type {
	t.Helper()

	typ, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("parsing the logical type %q: %v", spelling, err)
	}

	return typ
}

// literal returns the address of a default value, which ir.Default carries as a pointer.
func literal(v string) *string {
	return &v
}

// compileOne compiles an operation and insists it became exactly one statement.
func compileOne(t *testing.T, op ir.Operation) string {
	t.Helper()

	stmts, err := compile(op)
	if err != nil {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	if len(stmts) != 1 {
		t.Fatalf("compiling %s gave %d statements, want 1", op.Kind(), len(stmts))
	}

	return stmts[0].SQL
}

func TestEveryLogicalTypeHasAMySQLDeclarationExceptTheOneMySQLHasNoneFor(t *testing.T) {
	for spelling, want := range map[string]string{
		"string(255)":   "varchar(255)",
		"text":          "longtext",
		"bool":          "tinyint(1)",
		"int16":         "smallint",
		"int32":         "int",
		"int64":         "bigint",
		"float32":       "float",
		"float64":       "double",
		"decimal(12,4)": "decimal(12,4)",
		"uuid":          "char(36)",
		"json":          "json",
		"timestamp":     "datetime(6)",
		"timestamptz":   "datetime(6)",
		"date":          "date",
		"time":          "time(6)",
		"binary":        "longblob",
	} {
		if got := nativeType(mustType(t, spelling)); got != want {
			t.Errorf("%s maps to %q, want %q", spelling, got, want)
		}
	}

	if got := nativeType(mustType(t, "enum(draft,live)")); got != "enum('draft', 'live')" {
		t.Errorf("an enum maps to %q", got)
	}

	if got := nativeType(mustType(t, "array(int32)")); got != "" {
		t.Errorf("an array maps to %q, and MySQL has no array type", got)
	}

	t.Log("text is longtext and binary is longblob because the logical types mean an unbounded")
	t.Log("string and unbounded bytes, where MySQL's TEXT and BLOB stop at 65535 bytes.")
	t.Log("timestamp and timestamptz map to the same declaration, which is a fact about MySQL: its")
	t.Log("one zone-aware type, TIMESTAMP, spans 1970 to 2038 and so cannot carry a column migr")
	t.Log("writes forever. Every instant is rendered as UTC and the session's time_zone is +00:00.")
}

func TestEveryKindHasACaseInTheTypeMap(t *testing.T) {
	for _, spelling := range []string{
		"string(1)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
		"decimal(1,0)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
		"enum(a)",
	} {
		if nativeType(mustType(t, spelling)) == "" {
			t.Errorf("%s has no MySQL declaration, and only array may have none", spelling)
		}
	}
}

func TestACreateTableCarriesItsColumnsItsNullabilityAndItsKey(t *testing.T) {
	got := compileOne(t, ir.CreateTable{
		Table: "users",
		Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "uuid")},
			{Name: "email", Type: mustType(t, "string(255)")},
			{Name: "note", Type: mustType(t, "text"), Nullable: true},
		},
		PrimaryKey: []string{"id"},
	})

	want := "CREATE TABLE `users` (\n" +
		"    `id` char(36) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,\n" +
		"    `email` varchar(255) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,\n" +
		"    `note` longtext CHARACTER SET utf8mb4 COLLATE utf8mb4_bin,\n" +
		"    PRIMARY KEY (`id`)\n" +
		") DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_bin;"

	if got != want {
		t.Errorf("compiled\n%s\nwant\n%s", got, want)
	}
}

func TestACompositePrimaryKeyIsRenderedAsOneConstraint(t *testing.T) {
	got := compileOne(t, ir.CreateTable{
		Table: "runs",
		Columns: []ir.Column{
			{Name: "batch", Type: mustType(t, "int64")},
			{Name: "ordinal", Type: mustType(t, "int32")},
		},
		PrimaryKey: []string{"batch", "ordinal"},
	})

	if !strings.Contains(got, "PRIMARY KEY (`batch`, `ordinal`)") {
		t.Errorf("compiled\n%s", got)
	}

	t.Log("This is the history table's own key, so it is the one composite key that has to work")
	t.Log("before there is a history store at all.")
}

func TestADefaultIsRenderedTheOneWayMySQLAcceptsForThatColumn(t *testing.T) {
	for name, c := range map[string]struct {
		column ir.Column
		want   string
	}{
		"a literal on a varchar": {
			column: ir.Column{Name: "s", Type: mustType(t, "string(8)"),
				Default: &ir.Default{Literal: literal("x")}},
			want: "DEFAULT 'x'",
		},
		"a quote, doubled": {
			column: ir.Column{Name: "s", Type: mustType(t, "string(8)"),
				Default: &ir.Default{Literal: literal("a'b")}},
			want: "DEFAULT 'a''b'",
		},
		"a backslash, left alone": {
			column: ir.Column{Name: "s", Type: mustType(t, "string(8)"),
				Default: &ir.Default{Literal: literal(`a\b`)}},
			want: `DEFAULT 'a\b'`,
		},
		"a literal on a text column": {
			column: ir.Column{Name: "s", Type: mustType(t, "text"),
				Default: &ir.Default{Literal: literal("x")}},
			want: "DEFAULT ('x')",
		},
		"a literal on a json column": {
			column: ir.Column{Name: "s", Type: mustType(t, "json"),
				Default: &ir.Default{Literal: literal("{}")}},
			want: "DEFAULT ('{}')",
		},
		"current_timestamp, carrying the column's precision": {
			column: ir.Column{Name: "s", Type: mustType(t, "timestamptz"),
				Default: &ir.Default{Function: ir.FuncCurrentTimestamp}},
			want: "DEFAULT CURRENT_TIMESTAMP(6)",
		},
		"current_date, parenthesised": {
			column: ir.Column{Name: "s", Type: mustType(t, "date"),
				Default: &ir.Default{Function: ir.FuncCurrentDate}},
			want: "DEFAULT (CURDATE())",
		},
		"current_time, parenthesised": {
			column: ir.Column{Name: "s", Type: mustType(t, "time"),
				Default: &ir.Default{Function: ir.FuncCurrentTime}},
			want: "DEFAULT (CURTIME(6))",
		},
		"uuid, parenthesised": {
			column: ir.Column{Name: "s", Type: mustType(t, "uuid"),
				Default: &ir.Default{Function: ir.FuncUUID}},
			want: "DEFAULT (UUID())",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := defaultClause(c.column); got != c.want {
				t.Errorf("rendered %q, want %q", got, c.want)
			}
		})
	}

	t.Log("Four measurements decide this table. A bare DEFAULT CURRENT_TIMESTAMP on a datetime(6) is")
	t.Log("error 1067 on MySQL, so the precision is carried; bare DEFAULT CURRENT_DATE is a syntax")
	t.Log("error there and legal on MariaDB, so the parenthesised form is the one spelling both take;")
	t.Log("a bare literal on a text, blob or json column is error 1101; and a backslash is left")
	t.Log("alone because NO_BACKSLASH_ESCAPES is pinned on every session.")
}

func TestAnIndexIsBuiltOverTheColumnsTheOperationNames(t *testing.T) {
	got := compileOne(t, ir.AddIndex{Table: "users", Name: "users_email_idx",
		Columns: []string{"email", "created_at"}})

	want := "CREATE INDEX `users_email_idx` ON `users` (`email`, `created_at`);"
	if got != want {
		t.Errorf("compiled %q, want %q", got, want)
	}
}

func TestEveryOperationCompilesAndOnlyTheColumnChangeNeedsADatabase(t *testing.T) {
	var compiled, emulated int

	for _, op := range drivertest.Operations(Name) {
		kind := op.Kind()

		stmts, err := compile(op)

		switch {
		case errors.Is(err, driver.ErrNeedsSession):
			if kind != ir.KindAlterColumn {
				t.Errorf("%s asks for a database, and only alter_column has to", kind)
			}

			emulated++
		case err != nil:
			t.Errorf("%s refused: %v", kind, err)
		case len(stmts) == 0:
			t.Errorf("%s compiled to no statements at all, which reports success and changes nothing",
				kind)
		default:
			compiled++
		}
	}

	if compiled != len(ir.Kinds())-1 || emulated != 1 {
		t.Errorf("%d compiled and %d need a database, want %d and 1", compiled, emulated,
			len(ir.Kinds())-1)
	}

	t.Log("Coverage is derived from ir.Kinds(), so a twenty-first operation fails this the day it is")
	t.Log("added rather than the day someone remembers to widen a list.")
}

func TestEachOperationIsSpelledTheWayBothServersAccept(t *testing.T) {
	for name, c := range map[string]struct {
		op   ir.Operation
		want string
	}{
		"drop_table": {
			op:   ir.DropTable{Table: "users"},
			want: "DROP TABLE `users`;",
		},
		"rename_table": {
			op:   ir.RenameTable{From: "users", To: "accounts"},
			want: "ALTER TABLE `users` RENAME TO `accounts`;",
		},
		"add_column": {
			op: ir.AddColumn{Table: "users", Column: ir.Column{Name: "n",
				Type: mustType(t, "int32"), Nullable: true}},
			want: "ALTER TABLE `users` ADD COLUMN `n` int;",
		},
		"drop_column": {
			op:   ir.DropColumn{Table: "users", Column: "n"},
			want: "ALTER TABLE `users` DROP COLUMN `n`;",
		},
		"rename_column": {
			op:   ir.RenameColumn{Table: "users", From: "e", To: "email"},
			want: "ALTER TABLE `users` RENAME COLUMN `e` TO `email`;",
		},
		"drop_index names its table": {
			op:   ir.DropIndex{Table: "users", Name: "idx"},
			want: "DROP INDEX `idx` ON `users`;",
		},
		"add_foreign_key": {
			op: ir.AddForeignKey{Table: "orders", Name: "fk", Columns: []string{"uid"},
				ReferencesTable: "users", ReferencesColumns: []string{"id"},
				OnDelete: ir.ActionCascade, OnUpdate: ir.ActionRestrict},
			want: "ALTER TABLE `orders` ADD CONSTRAINT `fk` FOREIGN KEY (`uid`) " +
				"REFERENCES `users` (`id`) ON DELETE CASCADE ON UPDATE RESTRICT;",
		},
		"drop_foreign_key has its own verb": {
			op:   ir.DropForeignKey{Table: "orders", Name: "fk"},
			want: "ALTER TABLE `orders` DROP FOREIGN KEY `fk`;",
		},
		"add_unique": {
			op:   ir.AddUnique{Table: "users", Name: "uq", Columns: []string{"email"}},
			want: "ALTER TABLE `users` ADD CONSTRAINT `uq` UNIQUE (`email`);",
		},
		"drop_unique is an index drop": {
			op:   ir.DropUnique{Table: "users", Name: "uq"},
			want: "ALTER TABLE `users` DROP INDEX `uq`;",
		},
		"add_check": {
			op:   ir.AddCheck{Table: "orders", Name: "ck", Expression: "total >= 0"},
			want: "ALTER TABLE `orders` ADD CONSTRAINT `ck` CHECK (total >= 0);",
		},
		"drop_check is DROP CONSTRAINT": {
			op:   ir.DropCheck{Table: "orders", Name: "ck"},
			want: "ALTER TABLE `orders` DROP CONSTRAINT `ck`;",
		},
		"add_primary_key drops the name": {
			op:   ir.AddPrimaryKey{Table: "legacy", Name: "pk_legacy", Columns: []string{"id"}},
			want: "ALTER TABLE `legacy` ADD PRIMARY KEY (`id`);",
		},
		"drop_primary_key needs no name": {
			op:   ir.DropPrimaryKey{Table: "legacy", Name: "pk_legacy"},
			want: "ALTER TABLE `legacy` DROP PRIMARY KEY;",
		},
		"create_view": {
			op:   ir.CreateView{View: "v", Query: "SELECT 1"},
			want: "CREATE VIEW `v` AS SELECT 1;",
		},
		"create_view replacing": {
			op:   ir.CreateView{View: "v", Query: "SELECT 1;", OrReplace: true},
			want: "CREATE OR REPLACE VIEW `v` AS SELECT 1;",
		},
		"drop_view": {
			op:   ir.DropView{View: "v"},
			want: "DROP VIEW `v`;",
		},
		"raw passes through with no terminator added": {
			op:   ir.Raw{Driver: Name, SQL: "ANALYZE TABLE `users`"},
			want: "ANALYZE TABLE `users`",
		},
		"alter_column dropping a default alone": {
			op:   ir.AlterColumn{Table: "users", Column: "n", DropDefault: true},
			want: "ALTER TABLE `users` ALTER COLUMN `n` DROP DEFAULT;",
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := compileOne(t, c.op); got != c.want {
				t.Errorf("compiled\n%s\nwant\n%s", got, c.want)
			}
		})
	}

	t.Log("Four of these are MySQL's own spelling rather than the other drivers'. drop_index names its")
	t.Log("table, MySQL indexes being table-scoped. drop_foreign_key is DROP FOREIGN KEY. drop_unique")
	t.Log("is DROP INDEX, which works on every version of both servers where DROP CONSTRAINT arrived")
	t.Log("in MySQL 8.0.19. And drop_check is DROP CONSTRAINT, because DROP CHECK is error 1064 on")
	t.Log("MariaDB 11.4.13 -- measured, and the reverse of the case above.")
}

func TestAnIdentifierMySQLWouldRefuseIsRefusedHereWithAReason(t *testing.T) {
	for name, c := range map[string]struct {
		op   ir.Operation
		says string
	}{
		"a table name past 64 characters": {
			op:   ir.CreateTable{Table: strings.Repeat("t", identifierLimit+1)},
			says: "64",
		},
		"a column name past 64 characters": {
			op: ir.CreateTable{Table: "users", Columns: []ir.Column{
				{Name: strings.Repeat("c", identifierLimit+1), Type: mustType(t, "int32")},
			}},
			says: "64",
		},
		"a NUL byte in a table name": {
			op:   ir.CreateTable{Table: "us\x00ers"},
			says: "NUL",
		},
		"a trailing space": {
			op:   ir.CreateTable{Table: "users "},
			says: "space",
		},
		"an index name past 64 characters": {
			op: ir.AddIndex{Table: "users", Name: strings.Repeat("i", identifierLimit+1),
				Columns: []string{"email"}},
			says: "64",
		},
		"an array column": {
			op: ir.CreateTable{Table: "users", Columns: []ir.Column{
				{Name: "tags", Type: mustType(t, "array(string(8))")},
			}},
			says: "no array type",
		},
		"an enum default that is not a label": {
			op: ir.CreateTable{Table: "posts", Columns: []ir.Column{
				{Name: "state", Type: mustType(t, "enum(draft,live)"),
					Default: &ir.Default{Literal: literal("archived")}},
			}},
			says: "not one of its labels",
		},
		"a NUL byte in a literal default": {
			op: ir.CreateTable{Table: "users", Columns: []ir.Column{
				{Name: "s", Type: mustType(t, "string(8)"),
					Default: &ir.Default{Literal: literal("a\x00b")}},
			}},
			says: "NUL",
		},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := compile(c.op)
			if !errors.Is(err, driver.ErrUnsupported) {
				t.Fatalf("compile returned %v, want %v", err, driver.ErrUnsupported)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not say %q: %v", c.says, err)
			}
		})
	}

	t.Log("MySQL keeps 64 characters and reports 1059 on 8.0 and 1103 on 11.4 -- the same limit with")
	t.Log("two messages -- and refuses a name ending in a space even quoted. A driver that forwarded")
	t.Log("the server's answer would report a migration's defect halfway through the run.")
}

func TestAnIdentifierIsCountedInCharactersRatherThanBytes(t *testing.T) {
	name := strings.Repeat("é", identifierLimit)

	if problem := checkIdentifier("table", name); problem != "" {
		t.Errorf("a %d-character name was refused: %s", identifierLimit, problem)
	}

	if problem := checkIdentifier("table", name+"é"); problem == "" {
		t.Errorf("a %d-character name was accepted", identifierLimit+1)
	}

	t.Log("MySQL's limit is characters, so counting bytes would refuse a legal 32-character name")
	t.Log("written in anything but ASCII.")
}

func TestABacktickInAnIdentifierIsDoubledRatherThanRefused(t *testing.T) {
	got := compileOne(t, ir.CreateTable{Table: "we`ird",
		Columns: []ir.Column{{Name: "a`b", Type: mustType(t, "int32")}}})

	if !strings.Contains(got, "`we``ird`") || !strings.Contains(got, "`a``b`") {
		t.Errorf("compiled\n%s", got)
	}
}

func TestCompilingNothingIsAFailureRatherThanNoStatements(t *testing.T) {
	if _, err := compile(nil); err == nil {
		t.Error("compiling no operation at all succeeded")
	}
}
