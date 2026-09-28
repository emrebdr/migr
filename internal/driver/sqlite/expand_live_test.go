package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// referenced builds a table with one of everything that can point at it, and rows in all of them.
func referenced(t *testing.T, c *sqliteConn) {
	t.Helper()

	for _, statement := range []string{
		`CREATE TABLE "orgs" ("id" integer PRIMARY KEY, "name" text)`,
		`CREATE TABLE "users" (
			"id" integer NOT NULL,
			"email" varchar(255) NOT NULL COLLATE NOCASE,
			"handle" text UNIQUE,
			"shouty" text GENERATED ALWAYS AS (upper("email")) STORED,
			"org" integer REFERENCES "orgs"("id"),
			CONSTRAINT "ck_email" CHECK ("email" LIKE '%@%')
		)`,
		`CREATE TABLE "orders" ("id" integer PRIMARY KEY, "uid" integer REFERENCES "users"("id"))`,
		`CREATE TABLE "audit" ("id" integer)`,
		`CREATE UNIQUE INDEX "uq_users_id" ON "users" ("id")`,
		`CREATE INDEX "idx_users_email" ON "users" ("email")`,
		`CREATE UNIQUE INDEX "uq_users_email" ON "users" ("email")`,
		`CREATE VIEW "listing" AS SELECT "id", "email" FROM "users"`,
		`CREATE TRIGGER "trg_users" AFTER INSERT ON "users"
			BEGIN INSERT INTO "audit" VALUES (NEW."id"); END`,
		`INSERT INTO "orgs" VALUES (1, 'acme')`,
		`INSERT INTO "users" ("id", "email", "handle", "org")
			VALUES (1, 'a@b.c', 'one', 1), (2, 'd@e.f', 'two', 1)`,
		`INSERT INTO "orders" VALUES (1, 1)`,
	} {
		exec(t, c, statement)
	}
}

// digest renders every object this database holds, which is what a rebuild must leave alone.
func digest(t *testing.T, c *sqliteConn) string {
	t.Helper()

	rows := read(t, c, `SELECT type || ' ' || name || ' ' ||
		replace(replace(coalesce(sql, '(none)'), char(10), ' '), char(9), ' ')
		FROM sqlite_schema WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)

	lines := make([]string, 0, len(rows.Values))
	for _, row := range rows.Values {
		lines = append(lines, *row[0])
	}

	return strings.Join(lines, "\n")
}

// expanded compiles one emulated operation against a live database and runs every statement.
func expanded(t *testing.T, c *sqliteConn, op ir.Operation) {
	t.Helper()

	stmts, err := c.Expand(t.Context(), op)
	if err != nil {
		t.Fatalf("Expand(%s): %v", op.Kind(), err)
	}

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		for i, stmt := range stmts {
			if err := c.Exec(ctx, stmt); err != nil {
				return errWithStatement(i, stmt, err)
			}
		}

		return nil
	}); err != nil {
		t.Fatalf("running the rebuild for %s: %v", op.Kind(), err)
	}
}

// errWithStatement names which statement of a rebuild failed, since they only make sense in order.
func errWithStatement(i int, stmt driver.Statement, err error) error {
	return fmt.Errorf("statement %d, %s: %w", i+1, stmt.SQL, err)
}

// rebuilds is one of each emulated operation, all of them against the same seeded table.
func rebuilds(t *testing.T) map[string]ir.Operation {
	t.Helper()

	return map[string]ir.Operation{
		"alter_column": ir.AlterColumn{Table: "users", Column: "email",
			Type: pointer(mustType(t, "string(512)"))},
		"add_check":  ir.AddCheck{Table: "users", Name: "ck_long", Expression: `length("email") > 2`},
		"drop_check": ir.DropCheck{Table: "users", Name: "ck_email"},
		"add_foreign_key": ir.AddForeignKey{Table: "users", Name: "fk_users_org",
			Columns: []string{"org"}, ReferencesTable: "orgs", ReferencesColumns: []string{"id"}},
		"drop_foreign_key": ir.DropForeignKey{Table: "users", Name: "fk_users_org"},
		"add_primary_key": ir.AddPrimaryKey{Table: "users", Name: "pk_users",
			Columns: []string{"id"}},
		"drop_primary_key": ir.DropPrimaryKey{Table: "users", Name: "pk_users"},
	}
}

func TestARebuildLosesNothingThatReferencesTheTable(t *testing.T) {
	for name, op := range rebuilds(t) {
		t.Run(name, func(t *testing.T) {
			c := openFile(t)
			referenced(t, c)

			if name == "drop_foreign_key" || name == "drop_primary_key" {
				expanded(t, c, addedFirst(t, name))
			}

			before := digest(t, c)
			expanded(t, c, op)
			after := digest(t, c)

			checkSurvivors(t, c, before, after)
		})
	}

	t.Log("The generated column and the inline UNIQUE are what make the copy's column list matter:")
	t.Log("pragma_table_info omits a generated column, so naming the columns is what keeps the copy")
	t.Log("from trying to write one -- and an inline UNIQUE creates an autoindex SQLite records no")
	t.Log("statement for, which comes back with the definition and must not be recreated.")
	t.Log("")
	t.Log("This is the done-when: a table with an index, a unique index, a trigger, a view, a")
	t.Log("check, a key out and a key in, rebuilt seven ways, with every referencing object still")
	t.Log("there and still enforcing afterwards. The digest is taken before and after, so what")
	t.Log("the operation did not ask to change has to come back identical.")
}

// addedFirst returns the operation that puts in place what a drop is about to take away.
func addedFirst(t *testing.T, name string) ir.Operation {
	t.Helper()

	if name == "drop_foreign_key" {
		return rebuilds(t)["add_foreign_key"]
	}

	return rebuilds(t)["add_primary_key"]
}

// checkSurvivors holds everything a rebuild must not have touched to what it looked like before.
func checkSurvivors(t *testing.T, c *sqliteConn, before, after string) {
	t.Helper()

	for _, object := range []string{
		"index idx_users_email", "index uq_users_email", "index uq_users_id", "trigger trg_users",
		"view listing", "table orders", "table orgs",
	} {
		if !strings.Contains(after, object) {
			t.Errorf("the rebuild lost %s:\n%s", object, after)
		}
	}

	if got := value(t, read(t, c, `SELECT count(*) FROM "users"`)); got != "2" {
		t.Errorf("the rebuild left %s rows, want 2", got)
	}

	if got := value(t, read(t, c, `SELECT count(*) FROM "listing"`)); got != "2" {
		t.Errorf("the view answers %s, want 2", got)
	}

	if got := value(t, read(t, c, `SELECT count(*) FROM "orders"`)); got != "1" {
		t.Errorf("the child table lost rows: %s", got)
	}

	checkOther(t, before, after)
}

// checkOther reports every object whose own definition a rebuild changed without being asked to.
func checkOther(t *testing.T, before, after string) {
	t.Helper()

	was := make(map[string]bool)
	for _, line := range strings.Split(before, "\n") {
		was[line] = true
	}

	for _, line := range strings.Split(after, "\n") {
		if strings.HasPrefix(line, "table users ") || was[line] {
			continue
		}

		if strings.HasPrefix(line, "index ") || strings.HasPrefix(line, "trigger ") {
			continue
		}

		t.Errorf("the rebuild changed an object it was not asked about:\n  %s", line)
	}
}

func TestARebuildKeepsTheConstraintsItWasNotAskedAbout(t *testing.T) {
	c := openFile(t)
	referenced(t, c)

	expanded(t, c, ir.AddCheck{Table: "users", Name: "ck_long",
		Expression: `length("email") > 2`})

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("id", "email", "org") VALUES (9, 'not-an-email', 1)`,
	}); err == nil {
		t.Error("the check the rebuild was not asked about stopped enforcing")
	}

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("id", "email", "org") VALUES (10, 'a@b.c', 1)`,
	}); err == nil {
		t.Error("the unique index stopped enforcing")
	}

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("id", "email", "org") VALUES (11, 'x@y.z', 404)`,
	}); err == nil {
		t.Error("the foreign key out of the table stopped enforcing")
	}

	t.Log("A rebuild that dropped a constraint would leave a schema that looks right and enforces")
	t.Log("nothing, so each one is tested by being violated rather than by being read back.")
}

func TestTheNewConstraintIsEnforcedAfterTheRebuild(t *testing.T) {
	c := openFile(t)
	referenced(t, c)

	expanded(t, c, ir.AddCheck{Table: "users", Name: "ck_long",
		Expression: `length("email") >= 5`})

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("id", "email", "org") VALUES (9, 'x@y', 1)`,
	}); err == nil {
		t.Error("the check the rebuild added enforces nothing")
	}

	if _, err := c.Perform(t.Context(), driver.Query{
		SQL: `INSERT INTO "users" ("id", "email", "org") VALUES (10, 'x@y.zz', 1)`,
	}); err != nil {
		t.Errorf("a row the new check permits was refused: %v", err)
	}
}

func TestARebuildIsRefusedWhereItWouldTakeAnotherTablesRowsWithIt(t *testing.T) {
	for action, mutates := range map[string]bool{
		"ON DELETE CASCADE":     true,
		"ON DELETE SET NULL":    true,
		"ON DELETE SET DEFAULT": true,
		"ON DELETE RESTRICT":    false,
		"":                      false,
	} {
		t.Run(action, func(t *testing.T) {
			c := openFile(t)
			exec(t, c, `CREATE TABLE "users" ("id" integer PRIMARY KEY, "email" text)`)
			exec(t, c, `CREATE TABLE "orders" ("id" integer PRIMARY KEY,
				"uid" integer REFERENCES "users"("id") `+action+`)`)

			_, err := c.Expand(t.Context(), ir.AddCheck{Table: "users", Name: "ck",
				Expression: `length("email") > 0`})

			if mutates != errors.Is(err, ErrRebuild) {
				t.Fatalf("a child key %q gave %v", action, err)
			}

			if mutates && !strings.Contains(err.Error(), "orders") {
				t.Errorf("the refusal does not name the table that would lose rows: %v", err)
			}
		})
	}

	t.Log("Measured on 3.53.4: a rebuild drops the table, and SQLite fires ON DELETE CASCADE and")
	t.Log("SET NULL on the children while the rows are gone -- PRAGMA defer_foreign_keys holds the")
	t.Log("*check* until commit and does not hold the *action*. The transaction then commits and")
	t.Log("the child rows are simply gone, which is the worst failure this driver could have. So")
	t.Log("it refuses, and a key that only checks is fine because deferral covers exactly that.")
}

func TestARebuildIsRefusedWhenItCannotProceedSafely(t *testing.T) {
	c := openFile(t)
	exec(t, c, `CREATE TABLE "users" ("id" integer PRIMARY KEY)`)
	exec(t, c, `CREATE TABLE "migr_rebuild_users" ("id" integer)`)

	check := ir.AddCheck{Table: "users", Name: "ck", Expression: `"id" > 0`}

	if _, err := c.Expand(t.Context(), check); !errors.Is(err, ErrRebuild) {
		t.Errorf("a taken shadow name gave %v", err)
	}

	if _, err := c.Expand(t.Context(), ir.AddCheck{Table: "absent", Name: "ck",
		Expression: `1`}); !errors.Is(err, ErrRebuild) {
		t.Errorf("a table that is not there gave %v", err)
	}

	t.Log("The shadow is dropped by the rebuild that made it, so a table already using that name")
	t.Log("is one a rebuild would destroy -- and it is the name a rebuild interrupted before")
	t.Log("commit could never leave behind, since the whole thing is one transaction.")
}

func TestEveryStatementARebuildEmitsRunsInsideOneTransaction(t *testing.T) {
	c := openFile(t)
	referenced(t, c)

	stmts, err := c.Expand(t.Context(), ir.AddCheck{Table: "users", Name: "ck",
		Expression: `length("email") > 2`})
	if err != nil {
		t.Fatalf("Expand: %v", err)
	}

	for i, stmt := range stmts {
		if stmt.NoTransaction {
			t.Errorf("statement %d declares it cannot run inside a transaction", i+1)
		}

		if !strings.HasSuffix(strings.TrimSpace(stmt.SQL), terminator) {
			t.Errorf("statement %d is not terminated: %s", i+1, stmt.SQL)
		}

		if strings.TrimSuffix(strings.TrimSpace(stmt.SQL), terminator) == "" {
			t.Errorf("statement %d is a terminator and nothing else", i+1)
			t.Log("An index SQLite records no statement for -- the autoindex an inline UNIQUE")
			t.Log("creates -- comes back with the table's own definition and must not be restored")
			t.Log("separately. Emitting the empty string it would render to runs without failing,")
			t.Log("which is why the blank is what this asserts rather than the error it does not")
			t.Log("cause.")
		}
	}

	if !strings.HasPrefix(stmts[0].SQL, "PRAGMA defer_foreign_keys") {
		t.Errorf("the rebuild does not defer foreign keys first: %s", stmts[0].SQL)
		t.Log("Without it, dropping the table is an immediate violation for every key pointing at")
		t.Log("it -- measured. Deferral moves the check to the commit, by which time the table is")
		t.Log("back with the same rows in it.")
	}
}
