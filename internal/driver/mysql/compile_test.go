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
		"    `id` char(36) NOT NULL,\n" +
		"    `email` varchar(255) NOT NULL,\n" +
		"    `note` longtext,\n" +
		"    PRIMARY KEY (`id`)\n" +
		");"

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

func TestEighteenOperationsRefuseNamingTheTaskThatWritesThem(t *testing.T) {
	var refused int

	for _, op := range drivertest.Operations(Name) {
		kind := op.Kind()

		_, err := compile(op)
		if err == nil {
			continue
		}

		if !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("%s refused with %v, which is not a declared refusal", kind, err)
		}

		if !strings.Contains(err.Error(), "M9-T2") {
			t.Errorf("%s refuses without naming the task that implements it: %v", kind, err)
		}

		refused++
	}

	if refused != len(ir.Kinds())-2 {
		t.Errorf("%d of %d operations refuse, want all but create_table and add_index", refused,
			len(ir.Kinds()))
	}
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
