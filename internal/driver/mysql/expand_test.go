package mysql

import (
	"errors"
	"strings"
	"testing"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/ir"
)

// reported builds the error the library reports for one server error number.
func reported(number uint16, message string) error {
	return &lib.MySQLError{Number: number, Message: message}
}

func TestTheTwoKeyRefusalsAPureCompileCannotPreventAreExplained(t *testing.T) {
	for name, c := range map[string]struct {
		err   error
		says  []string
		plain bool
	}{
		"a text column in a key": {
			err: reported(errBlobKeyWithoutLength,
				"BLOB/TEXT column 'body' used in key specification without a key length"),
			says: []string{"prefix length", "MariaDB indexes", "name columns and not types", "1170"},
		},
		"a key too long": {
			err:  reported(errKeyTooLong, "Specified key was too long; max key length is 3072 bytes"),
			says: []string{"3072", "768", "utf8mb4", "1071"},
		},
		"anything else passes through": {
			err:   reported(1146, "Table 'migr.nope' doesn't exist"),
			plain: true,
		},
		"an error the library did not report": {
			err:   errors.New("connection reset"),
			plain: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := explainExec(c.err)

			if c.plain {
				if !errors.Is(got, c.err) {
					t.Fatalf("explainExec replaced %v with %v", c.err, got)
				}

				if errors.Is(got, ErrIndexKey) {
					t.Errorf("an unrelated failure was reported as a key refusal: %v", got)
				}

				return
			}

			if !errors.Is(got, ErrIndexKey) {
				t.Fatalf("explainExec returned %v, want %v", got, ErrIndexKey)
			}

			if !errors.Is(got, c.err) {
				t.Error("the server's own failure did not survive being explained")
			}

			for _, phrase := range c.says {
				if !strings.Contains(got.Error(), phrase) {
					t.Errorf("the explanation does not mention %q: %v", phrase, got)
				}
			}
		})
	}

	t.Log("Both are about a column's type, and add_index, add_unique and add_primary_key name columns.")
	t.Log("So the server's refusal is what an operator sees and what this adds is the rule -- including")
	t.Log("that MariaDB indexes a longtext without a prefix, so the same migration succeeds there.")
	t.Log("Anything else passes through, because reporting an unrelated failure as a key refusal is how")
	t.Log("an operator learns to ignore the explanation.")
}

func TestTheServersOwnMessageIsKeptRatherThanReplaced(t *testing.T) {
	server := reported(errBlobKeyWithoutLength,
		"BLOB/TEXT column 'body' used in key specification without a key length")

	got := explainExec(server)

	if !strings.Contains(got.Error(), "BLOB/TEXT column 'body'") {
		t.Errorf("the server's own message is gone: %v", got)
	}

	t.Log("An operator searches for the server's wording, so it is carried rather than paraphrased.")
	t.Log("What this driver adds sits beside it.")
}

func TestExpandRefusesAnOperationItNeverAsksForASessionFor(t *testing.T) {
	c := &mysqlConn{}

	_, err := c.Expand(t.Context(), ir.DropTable{Table: "users"})
	if !errors.Is(err, ErrRedefine) {
		t.Fatalf("Expand returned %v, want %v", err, ErrRedefine)
	}

	if !strings.Contains(err.Error(), "drop_table") {
		t.Errorf("the refusal does not name the operation: %v", err)
	}

	t.Log("Only what the declaration calls emulated reaches Expand, so anything else arriving is a")
	t.Log("bug in this driver rather than a migration's problem -- and it says so.")
}

func TestACatalogRowCarryingAnAttributeARedefinitionWouldLoseIsRefused(t *testing.T) {
	op := ir.AlterColumn{Table: "users", Column: "id", Type: typePointer(t, "int64")}

	for name, c := range map[string]struct {
		found existingColumn
		says  string
	}{
		"auto_increment": {
			found: existingColumn{declared: "int", data: "int", extra: "auto_increment"},
			says:  "auto_increment",
		},
		"an ON UPDATE clause on MySQL": {
			found: existingColumn{declared: "datetime(6)", data: "datetime",
				extra: "on update CURRENT_TIMESTAMP(6)"},
			says: "on update",
		},
		"an ON UPDATE clause on MariaDB": {
			found: existingColumn{declared: "datetime(6)", data: "datetime",
				extra: "on update current_timestamp(6)"},
			says: "on update",
		},
		"a virtual generated column": {
			found: existingColumn{declared: "int", data: "int", extra: "VIRTUAL GENERATED"},
			says:  "VIRTUAL GENERATED",
		},
		"a stored generated column": {
			found: existingColumn{declared: "int", data: "int", extra: "STORED GENERATED"},
			says:  "STORED GENERATED",
		},
		"a collation this driver did not set": {
			found: existingColumn{declared: "varchar(10)", data: "varchar",
				collation: "utf8mb4_general_ci"},
			says: "utf8mb4_general_ci",
		},
		"an EXTRA this build has never seen": {
			found: existingColumn{declared: "int", data: "int", extra: "SOMETHING NEW"},
			says:  "SOMETHING NEW",
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := checkRestatableColumn(op, c.found)

			if !errors.Is(err, ErrRedefine) {
				t.Fatalf("the column was accepted, or refused with %v", err)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal does not mention %q: %v", c.says, err)
			}

			if !strings.Contains(err.Error(), "users.id") {
				t.Errorf("the refusal does not name the column: %v", err)
			}
		})
	}

	t.Log("The last case is the whole shape of this check: it fails closed. EXTRA is empty for an")
	t.Log("ordinary column and carries every attribute a redefinition would drop, so anything in it")
	t.Log("this build cannot name is refused rather than silently discarded. DEFAULT_GENERATED is the")
	t.Log("one value stripped first, because it describes the default and the default is handled apart.")
	t.Log("")
	t.Log("EXTRA is the only field consulted, and the mutation sweep is why. A separate check on")
	t.Log("GENERATION_EXPRESSION survived every test: both servers set EXTRA to VIRTUAL GENERATED or")
	t.Log("STORED GENERATED for the same column, so it was a second producer of an answer this one")
	t.Log("already gives -- the finding M8-T6 and M9-T1 each reached by deleting one.")
}

func TestAColumnThisDriverCouldHaveCreatedIsRestatedRatherThanRefused(t *testing.T) {
	op := ir.AlterColumn{Table: "users", Column: "email", Type: typePointer(t, "string(512)")}

	for name, found := range map[string]existingColumn{
		"a collated column carrying this driver's own collation": {
			declared: "varchar(255)", data: "varchar", collation: collation,
		},
		"an uncollated column, which records no collation at all": {
			declared: "int", data: "int",
		},
		"a column whose only EXTRA describes its expression default": {
			declared: "longtext", data: "longtext", collation: collation,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := checkRestatableColumn(op, found); err != nil {
				t.Errorf("a restatable column was refused: %v", err)
			}
		})
	}
}

func TestDefaultGeneratedIsStrippedSoItIsNotMistakenForAnAttribute(t *testing.T) {
	for extra, want := range map[string]string{
		"DEFAULT_GENERATED": "",
		"DEFAULT_GENERATED on update CURRENT_TIMESTAMP(6)": "on update CURRENT_TIMESTAMP(6)",
		"auto_increment": "auto_increment",
		"":               "",
	} {
		got := unrestatableExtra(extra)
		if got != want {
			t.Errorf("stripping %q left %q, want %q", extra, got, want)
		}
	}

	t.Log("MySQL sets EXTRA to DEFAULT_GENERATED for any column with an expression default, so a check")
	t.Log("that refused a non-empty EXTRA outright would refuse every column this driver gave one to.")
	t.Log("The same reasoning as the history store's GENERATION_EXPRESSION filter, one field over.")
}

func TestWhetherAColumnCarriesADefaultIsReadPerVariant(t *testing.T) {
	recorded := func(v string) *string { return &v }

	for name, c := range map[string]struct {
		variant  Variant
		recorded *string
		want     bool
	}{
		"nothing recorded, MySQL":        {variant: MySQL, want: false},
		"nothing recorded, MariaDB":      {variant: MariaDB, want: false},
		"MariaDB's unquoted NULL":        {variant: MariaDB, recorded: recorded("NULL"), want: false},
		"MySQL's unquoted NULL is real":  {variant: MySQL, recorded: recorded("NULL"), want: true},
		"MariaDB's quoted NULL is real":  {variant: MariaDB, recorded: recorded("'NULL'"), want: true},
		"a value, MySQL":                 {variant: MySQL, recorded: recorded("it's"), want: true},
		"a value, MariaDB":               {variant: MariaDB, recorded: recorded("'it''s'"), want: true},
		"an expression default, MySQL":   {variant: MySQL, recorded: recorded(`_utf8mb4\'x\'`), want: true},
		"an expression default, MariaDB": {variant: MariaDB, recorded: recorded("'x'"), want: true},
	} {
		t.Run(name, func(t *testing.T) {
			session := &mysqlConn{version: serverVersion{variant: c.variant}}

			if got := session.carriesDefault(c.recorded); got != c.want {
				t.Errorf("carriesDefault is %v, want %v", got, c.want)
			}
		})
	}

	t.Log("The two middle cases are why this is variant-shaped rather than one rule. DEFAULT NULL on a")
	t.Log("nullable column means the same as no default; MySQL records it as SQL null and MariaDB as")
	t.Log("the four characters NULL. On MySQL those same four characters are a column whose default is")
	t.Log("the string NULL, which is a real default. Measured on both, and anything unrecognized counts")
	t.Log("as present -- which refuses rather than drops.")
}
