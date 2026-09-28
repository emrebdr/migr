package sqlite

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

// exotic carries, in one table, everything this driver understands nothing about and must keep.
const exotic = `CREATE TABLE "users" (
    "id" integer PRIMARY KEY AUTOINCREMENT,
    "email" varchar(255) NOT NULL COLLATE NOCASE DEFAULT 'nobody@example.com',
    "shouty" text GENERATED ALWAYS AS (upper("email")) STORED,
    "org" integer REFERENCES "orgs"("id") ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    CONSTRAINT "ck_email" CHECK ("email" LIKE '%@%'),
    CONSTRAINT "fk_org" FOREIGN KEY ("org") REFERENCES "orgs" ("id"),
    CONSTRAINT "uq_email" UNIQUE ("email")
) STRICT`

// changed applies one operation to a definition for a test, failing rather than returning a reason.
func changed(t *testing.T, statement string, op ir.Operation) tableDefinition {
	t.Helper()

	d, err := modify("users", split(t, statement), op)
	if err != nil {
		t.Fatalf("modify(%s): %v", op.Kind(), err)
	}

	return d
}

func TestWhatAnOperationDoesNotTargetIsCopiedThroughVerbatim(t *testing.T) {
	untouched := []string{
		`"shouty" text GENERATED ALWAYS AS (upper("email")) STORED`,
		`"org" integer REFERENCES "orgs"("id") ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED`,
		`"id" integer PRIMARY KEY AUTOINCREMENT`,
	}

	operations := map[string]ir.Operation{
		"add_check":    ir.AddCheck{Table: "users", Name: "ck_new", Expression: `length("email") > 3`},
		"drop_check":   ir.DropCheck{Table: "users", Name: "ck_email"},
		"add_fk":       ir.AddForeignKey{Table: "users", Name: "fk_new", Columns: []string{"org"}, ReferencesTable: "orgs", ReferencesColumns: []string{"id"}},
		"drop_fk":      ir.DropForeignKey{Table: "users", Name: "fk_org"},
		"alter_column": ir.AlterColumn{Table: "users", Column: "email", Type: pointer(mustType(t, "string(512)"))},
	}

	for name, op := range operations {
		t.Run(name, func(t *testing.T) {
			rendered := changed(t, exotic, op).render("users")

			for _, fragment := range untouched {
				if !strings.Contains(rendered, fragment) {
					t.Errorf("%s lost\n  %s\nfrom\n%s", name, fragment, rendered)
				}
			}

			if !strings.HasSuffix(rendered, ") STRICT") {
				t.Errorf("%s lost the table options: %s", name, rendered)
			}
		})
	}

	t.Log("GENERATED ALWAYS AS, COLLATE, a column-level REFERENCES with DEFERRABLE, and")
	t.Log("AUTOINCREMENT are all things this driver never learned to read. They survive because")
	t.Log("an item the operation does not name is copied byte for byte -- which is what keeps the")
	t.Log("splitter small enough to trust.")
}

func TestAddingAConstraintPutsItAfterEverythingAlreadyDeclared(t *testing.T) {
	rendered := changed(t, exotic, ir.AddCheck{
		Table: "users", Name: "ck_new", Expression: `length("email") > 3`,
	}).render("users")

	if !strings.Contains(rendered, `CONSTRAINT "ck_new" CHECK (length("email") > 3)`) {
		t.Errorf("the check was not added: %s", rendered)
	}

	if strings.Index(rendered, `"ck_new"`) < strings.Index(rendered, `"uq_email"`) {
		t.Errorf("it was added before what was already there: %s", rendered)
	}
}

func TestDroppingAConstraintRemovesTheOneItNames(t *testing.T) {
	rendered := changed(t, exotic, ir.DropCheck{Table: "users", Name: "ck_email"}).render("users")

	if strings.Contains(rendered, `"ck_email"`) {
		t.Errorf("the check survived: %s", rendered)
	}

	for _, kept := range []string{`"fk_org"`, `"uq_email"`} {
		if !strings.Contains(rendered, kept) {
			t.Errorf("dropping one constraint took %s with it: %s", kept, rendered)
		}
	}
}

func TestAnUnnamedPrimaryKeyIsFoundByItsKeyword(t *testing.T) {
	statement := `CREATE TABLE "users" ("id" integer, "org" integer, PRIMARY KEY ("id", "org"))`

	rendered := changed(t, statement, ir.DropPrimaryKey{Table: "users"}).render("users")

	if strings.Contains(rendered, "PRIMARY KEY") {
		t.Errorf("the key survived: %s", rendered)
	}

	t.Log("drop_primary_key's name is optional in the v1 IR, because MySQL has no name for a")
	t.Log("primary key. Here the table constraint is found by its keyword instead.")
}

func TestAConstraintTheDefinitionDoesNotCarryIsRefused(t *testing.T) {
	for name, op := range map[string]ir.Operation{
		"a check nothing declares": ir.DropCheck{Table: "users", Name: "ck_absent"},
		"a key on the column":      ir.DropPrimaryKey{Table: "users"},
		"a second primary key": ir.AddPrimaryKey{Table: "users", Name: "pk",
			Columns: []string{"email"}},
		"a column nothing declares": ir.AlterColumn{Table: "users", Column: "absent",
			Nullable: pointer(true)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := modify("users", split(t, exotic), op)
			if !errors.Is(err, ErrRebuild) {
				t.Fatalf("modify returned %v, want %v", err, ErrRebuild)
			}
		})
	}

	t.Log("The key on the column is the interesting one: `id integer PRIMARY KEY AUTOINCREMENT`")
	t.Log("is part of that column's definition, and removing it changes the column rather than a")
	t.Log("constraint -- on an integer key it stops being the rowid alias. The refusal says so.")
}

func TestAlteringAColumnChangesOnlyWhatTheOperationNames(t *testing.T) {
	statement := `CREATE TABLE "users" (
    "email" varchar(255) NOT NULL COLLATE NOCASE DEFAULT 'x',
    "other" text
)`

	cases := map[string]struct {
		op   ir.AlterColumn
		want string
	}{
		"the type": {
			ir.AlterColumn{Table: "users", Column: "email", Type: pointer(mustType(t, "text"))},
			`"email" text NOT NULL COLLATE NOCASE DEFAULT 'x'`,
		},
		"nullability off": {
			ir.AlterColumn{Table: "users", Column: "email", Nullable: pointer(true)},
			`"email" varchar(255) COLLATE NOCASE DEFAULT 'x'`,
		},
		"the default": {
			ir.AlterColumn{Table: "users", Column: "email", Default: ir.Literal("y")},
			`"email" varchar(255) NOT NULL COLLATE NOCASE DEFAULT 'y'`,
		},
		"dropping the default": {
			ir.AlterColumn{Table: "users", Column: "email", DropDefault: true},
			`"email" varchar(255) NOT NULL COLLATE NOCASE`,
		},
		"all at once": {
			ir.AlterColumn{Table: "users", Column: "email", Type: pointer(mustType(t, "text")),
				Nullable: pointer(true), DropDefault: true},
			`"email" text COLLATE NOCASE`,
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rendered := changed(t, statement, c.op).render("users")

			if !strings.Contains(rendered, c.want) {
				t.Errorf("the column reads\n%s\nwant it to carry\n  %s", rendered, c.want)
			}

			if !strings.Contains(rendered, `"other" text`) {
				t.Errorf("the other column was disturbed: %s", rendered)
			}
		})
	}

	t.Log("COLLATE NOCASE is in every expected value above and this driver has no idea what it is.")
	t.Log("alter_column edits the clauses it was asked about and leaves the rest of the column")
	t.Log("definition where it found it, rather than re-rendering the column from the IR.")
}

func TestAddingNotNullToAColumnThatHasNone(t *testing.T) {
	statement := `CREATE TABLE "users" ("email" text DEFAULT 'x')`

	rendered := changed(t, statement, ir.AlterColumn{
		Table: "users", Column: "email", Nullable: pointer(false),
	}).render("users")

	if !strings.Contains(rendered, `"email" text DEFAULT 'x' NOT NULL`) {
		t.Errorf("NOT NULL was not added: %s", rendered)
	}
}

// pointer returns a pointer to a value, for the operation fields that are tri-state.
func pointer[T any](v T) *T {
	return &v
}
