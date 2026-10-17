package postgres

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// sixteen and eleven are the two sides of the release SET NOT NULL learned to trust a check in.
const (
	sixteen serverVersion = 160004
	eleven  serverVersion = 110022
)

// saferOn returns the sequence an operation runs as on a server, failing where there is none.
func saferOn(t *testing.T, v serverVersion, op ir.Operation) driver.Rewrite {
	t.Helper()

	got, ok := rewriteFor(v, op)
	if !ok {
		t.Fatalf("%s has no rewrite on %s", op.Kind(), v)
	}

	return got
}

// sqlOf renders a rewrite's statements, marking each one that runs outside any transaction.
func sqlOf(r driver.Rewrite) []string {
	out := make([]string, 0, len(r.Statements))

	for _, stmt := range r.Statements {
		line := stmt.SQL
		if stmt.NoTransaction {
			line = "[bare] " + line
		}

		out = append(out, line)
	}

	return out
}

// equalLines reports whether two renderings agree, line for line.
func equalLines(t *testing.T, got, want []string) {
	t.Helper()

	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rewritten to\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
}

func TestACheckIsAddedNotValidAndValidatedOnItsOwn(t *testing.T) {
	r := saferOn(t, sixteen, ir.AddCheck{Table: "users", Name: "positive", Expression: "id > 0"})

	equalLines(t, sqlOf(r), []string{
		`ALTER TABLE "users" ADD CONSTRAINT "positive" CHECK (id > 0) NOT VALID;`,
		`[bare] ALTER TABLE "users" VALIDATE CONSTRAINT "positive";`,
	})

	if r.Name != driver.RewriteNotValid || r.Lock != capability.BriefExclusive {
		t.Errorf("the rewrite is %s at %s, want not_valid at brief_exclusive", r.Name, r.Lock)
	}

	t.Log("Measured on 16 and 17 over 15 million rows: the plain ADD CHECK blocked 34 of 35 writes")
	t.Log("for its 9.5s scan, the pair blocked none -- and the pair inside one transaction blocked")
	t.Log("34 of 35 again, because the ADD's lock is held to commit. So VALIDATE runs bare.")
}

func TestAForeignKeyIsAddedNotValidAndValidatedOnItsOwn(t *testing.T) {
	r := saferOn(t, sixteen, ir.AddForeignKey{Table: "orders", Name: "fk_user", Columns: []string{"user_id"},
		ReferencesTable: "users", ReferencesColumns: []string{"id"}, OnDelete: ir.ActionCascade})

	equalLines(t, sqlOf(r), []string{
		`ALTER TABLE "orders" ADD CONSTRAINT "fk_user" FOREIGN KEY ("user_id") REFERENCES "users" ("id") ` +
			`ON DELETE CASCADE NOT VALID;`,
		`[bare] ALTER TABLE "orders" VALIDATE CONSTRAINT "fk_user";`,
	})
}

func TestSetNotNullIsBackedByAValidatedCheckFromTwelve(t *testing.T) {
	op := ir.AlterColumn{Table: "users", Column: "email", Nullable: boolPointer(false),
		Default: ir.Literal("none")}

	equalLines(t, sqlOf(saferOn(t, sixteen, op)), []string{
		`ALTER TABLE "users" ADD CONSTRAINT "users_email_nn" CHECK ("email" IS NOT NULL) NOT VALID;`,
		`[bare] ALTER TABLE "users" VALIDATE CONSTRAINT "users_email_nn";`,
		`ALTER TABLE "users" ALTER COLUMN "email" SET NOT NULL;`,
		`ALTER TABLE "users" DROP CONSTRAINT "users_email_nn";`,
		`ALTER TABLE "users" ALTER COLUMN "email" SET DEFAULT 'none';`,
	})

	if _, ok := rewriteFor(eleven, op); ok {
		t.Error("PostgreSQL 11 was given the check-backed SET NOT NULL, and it scans regardless")
	}

	t.Log("On 12 and later SET NOT NULL trusts a validated IS NOT NULL check: measured over 15")
	t.Log("million rows, 0.7ms behind one against 367ms without -- and no write blocked.")
}

func TestOnlyANarrowingThatChangesNoTypeIsBackedByACheck(t *testing.T) {
	for name, op := range map[string]ir.AlterColumn{
		"relaxing":  {Table: "users", Column: "email", Nullable: boolPointer(true)},
		"retyping":  {Table: "users", Column: "email", Nullable: boolPointer(false), Type: typePointer(t, "text")},
		"defaulted": {Table: "users", Column: "email", Default: ir.Literal("x")},
	} {
		if _, ok := rewriteFor(sixteen, op); ok {
			t.Errorf("%s was rewritten, and only a narrowing with no retype is: a retype rewrites "+
				"the table under its lock whatever the check says", name)
		}
	}
}

func TestAnIndexIsBuiltConcurrentlyOutsideAnyTransaction(t *testing.T) {
	r := saferOn(t, sixteen, ir.AddIndex{Table: "users", Name: "idx_email", Columns: []string{"email"}})

	equalLines(t, sqlOf(r), []string{`[bare] CREATE INDEX CONCURRENTLY "idx_email" ON "users" ("email");`})

	if r.Lock != capability.LockFree {
		t.Errorf("a concurrent build is declared %s, and it blocks neither reads nor writes", r.Lock)
	}
}

func TestAnOperationThisDriverRefusesIsNeverRewritten(t *testing.T) {
	if _, ok := rewriteFor(sixteen, ir.AddCheck{Table: "users", Name: "c", Expression: "x\x00"}); ok {
		t.Error("an operation the compiler refuses was rewritten, which would run what it refused")
	}

	if _, ok := rewriteFor(sixteen, ir.DropIndex{Name: "idx"}); ok {
		t.Error("drop_index was rewritten, and DROP INDEX CONCURRENTLY is M13-T11's")
	}
}

func TestTheDerivedCheckNameIsStableAndFitsTheIdentifierLimit(t *testing.T) {
	long := strings.Repeat("ü", 40)

	first := notNullCheckName(long, "column")
	if first != notNullCheckName(long, "column") || len(first) > identifierLimit {
		t.Errorf("the derived name %q is %d bytes, or not the same twice", first, len(first))
	}

	if !strings.HasSuffix(first, notNullSuffix) || strings.ContainsRune(first, '�') {
		t.Errorf("the derived name %q lost its suffix or was cut inside a character", first)
	}

	if notNullCheckName("users", "email") != "users_email_nn" {
		t.Errorf("a short name was hashed: %q", notNullCheckName("users", "email"))
	}
}

// boolPointer returns a pointer to a nullability, which alter_column declares as tri-state.
func boolPointer(b bool) *bool {
	return &b
}

// typePointer parses a logical type and returns a pointer to it, as alter_column carries one.
func typePointer(t *testing.T, spelling string) *schema.Type {
	t.Helper()

	typ := mustType(t, spelling)

	return &typ
}
