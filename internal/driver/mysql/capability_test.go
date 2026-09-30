package mysql

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
)

// declaredFor builds the capability set Open would build for one server, with no server present.
func declaredFor(t *testing.T, version serverVersion) capability.Set {
	t.Helper()

	return declaredSet(t, version)
}

func TestThisEngineDeclaresThatAFailedMigrationLeavesTheSchemaChanged(t *testing.T) {
	set := declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46})

	if set.TransactionalDDL() {
		t.Error("this driver declares transactional DDL, and MySQL commits on every DDL statement")
	}

	t.Log("Measured rather than cited: a row inserted inside a transaction survived the ROLLBACK")
	t.Log("because the CREATE TABLE after it had already committed, on both servers. host.info and")
	t.Log("engine.DirtyError both carry this flag, so it is wrong from this commit or right from it.")
}

func TestEveryOperationIsDeclaredForEveryVersionThisDriverAccepts(t *testing.T) {
	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0, patch: 12},
		{variant: MySQL, major: 8, minor: 0, patch: 46},
		{variant: MariaDB, major: 10, minor: 5},
	} {
		set := declaredFor(t, version)

		for _, op := range operationsFor(t) {
			verdict := set.Resolve(op)
			if verdict.Reason() == "" && !verdict.Allowed() {
				t.Errorf("%s on %s is refused with no reason", op.Kind(), version)
			}
		}
	}
}

// operationsFor returns one operation per kind, which is what a declaration must cover.
func operationsFor(t *testing.T) ir.Operations {
	t.Helper()

	return drivertest.Operations(Name)
}

func TestTheColumnChangeIsDeclaredEmulatedAndNamesItsStrategy(t *testing.T) {
	set := declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46})

	verdict := set.Resolve(ir.AlterColumn{Table: "users", Column: "email",
		Type: typePointer(t, "string(512)")})

	if verdict.Support() != capability.SupportEmulated {
		t.Fatalf("a column retype is declared %q, and Compile asks for a session", verdict)
	}

	if verdict.Strategy() != redefineStrategy {
		t.Errorf("the strategy is %q, want %q", verdict.Strategy(), redefineStrategy)
	}

	if !verdict.Allowed() {
		t.Error("an emulated operation is not allowed, so nothing would run it")
	}

	t.Log("The declaration and the compiler are one statement of one fact, which the conformance suite")
	t.Log("asserts as a biconditional. migr plan prints the strategy, because a capability is never")
	t.Log("silently degraded.")
}

func TestDroppingADefaultIsNativeWhereTheRestOfTheSameKindIsEmulated(t *testing.T) {
	set := declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46})

	dropped := set.Resolve(ir.AlterColumn{Table: "users", Column: "n", DropDefault: true})
	if dropped.Support() != capability.SupportNative {
		t.Errorf("dropping a default is declared %q, and it compiles with no database", dropped)
	}

	retyped := set.Resolve(ir.AlterColumn{Table: "users", Column: "n",
		Type: typePointer(t, "int64")})
	if retyped.Support() != capability.SupportEmulated {
		t.Errorf("a retype is declared %q", retyped)
	}

	t.Log("One ir.Kind, two verdicts by payload. This is the case where a Rule receiving the whole")
	t.Log("operation is load-bearing rather than merely available -- a map from kind to verdict could")
	t.Log("not express it, and would have to call the whole kind emulated.")
}

func TestACheckConstraintIsRefusedOnAServerThatWouldAcceptAndDiscardIt(t *testing.T) {
	op := ir.AddCheck{Table: "orders", Name: "ck", Expression: "total >= 0"}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0},
		{variant: MySQL, major: 8, minor: 0, patch: 15},
	} {
		verdict := declaredFor(t, version).Resolve(op)
		if verdict.Allowed() {
			t.Errorf("%s accepts a check constraint, and below 8.0.16 MySQL discards it", version)
		}

		if !strings.Contains(verdict.Reason(), "8.0.16") {
			t.Errorf("the refusal does not name the release: %s", verdict.Reason())
		}
	}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0, patch: 16},
		{variant: MySQL, major: 8, minor: 4},
		{variant: MariaDB, major: 10, minor: 5},
		{variant: MariaDB, major: 11, minor: 4, patch: 13},
	} {
		if !declaredFor(t, version).Resolve(op).Allowed() {
			t.Errorf("%s refuses a check constraint", version)
		}
	}

	t.Log("Below 8.0.16 MySQL parses a CHECK and does not enforce it, so the migration reports success")
	t.Log("and the constraint does not exist. That is worse than refusing, and it is the one case where")
	t.Log("a compiled statement is valid SQL the server silently discards. MariaDB has enforced them")
	t.Log("since 10.2.1, below this driver's floor.")
}

func TestDroppingACheckIsGatedOnTheOneSpellingBothVariantsHave(t *testing.T) {
	op := ir.DropCheck{Table: "orders", Name: "ck"}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0, patch: 16},
		{variant: MySQL, major: 8, minor: 0, patch: 18},
	} {
		verdict := declaredFor(t, version).Resolve(op)
		if verdict.Allowed() {
			t.Errorf("%s accepts DROP CONSTRAINT, which MySQL gained in 8.0.19", version)
		}

		if !strings.Contains(verdict.Reason(), "8.0.19") {
			t.Errorf("the refusal does not name the release: %s", verdict.Reason())
		}
	}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0, patch: 19},
		{variant: MariaDB, major: 10, minor: 5},
	} {
		if !declaredFor(t, version).Resolve(op).Allowed() {
			t.Errorf("%s refuses to drop a check", version)
		}
	}

	t.Log("DROP CHECK is error 1064 on MariaDB 11.4.13 -- measured -- so it is not portable, and a pure")
	t.Log("Compile cannot choose a spelling by variant. One spelling is emitted and the three releases")
	t.Log("that enforce a check without being able to drop one that way are refused. A migration can")
	t.Log("add a check on 8.0.16 and not undo it there, which is stated rather than hidden.")
}

func TestRenamingAColumnIsRefusedOnTheTwoMariaDBReleasesWithoutIt(t *testing.T) {
	op := ir.RenameColumn{Table: "users", From: "e", To: "email"}

	for _, version := range []serverVersion{
		{variant: MariaDB, major: 10, minor: 5},
		{variant: MariaDB, major: 10, minor: 5, patch: 1},
	} {
		verdict := declaredFor(t, version).Resolve(op)
		if verdict.Allowed() {
			t.Errorf("%s accepts RENAME COLUMN, which MariaDB gained in 10.5.2", version)
		}

		if !strings.Contains(verdict.Reason(), "10.5.2") {
			t.Errorf("the refusal does not name the release: %s", verdict.Reason())
		}
	}

	for _, version := range []serverVersion{
		{variant: MariaDB, major: 10, minor: 5, patch: 2},
		{variant: MariaDB, major: 11, minor: 4, patch: 13},
		{variant: MySQL, major: 8, minor: 0},
	} {
		if !declaredFor(t, version).Resolve(op).Allowed() {
			t.Errorf("%s refuses to rename a column", version)
		}
	}

	t.Log("MySQL has had RENAME COLUMN since 8.0, which is this driver's floor, so the gate is")
	t.Log("MariaDB's alone -- the mirror of the expression-default one. Below 10.5.2 a rename is")
	t.Log("CHANGE COLUMN, which restates the whole definition and would make renaming a column an")
	t.Log("operation compiled against the database rather than by Compile.")
}

func TestAColumnAddedWithAnExpressionDefaultIsGatedLikeOneDeclaredInline(t *testing.T) {
	op := ir.AddColumn{Table: "posts", Column: ir.Column{Name: "body",
		Type: mustType(t, "text"), Default: &ir.Default{Literal: literal("x")}}}

	if declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 12}).
		Resolve(op).Allowed() {
		t.Error("add_column with a text default is allowed on 8.0.12, where it has no spelling")
	}

	if !declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}).
		Resolve(op).Allowed() {
		t.Error("add_column with a text default is refused on 8.0.46")
	}

	t.Log("The same clause reaches the server through ADD COLUMN as through CREATE TABLE, so gating")
	t.Log("only the second would let the first past on a server with no syntax for it.")
}

func TestAnExpressionDefaultIsRefusedOnAServerTooOldToHaveThem(t *testing.T) {
	op := ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "uuid"), Default: &ir.Default{Function: ir.FuncUUID}},
	}}

	old := declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 12})

	verdict := old.Resolve(op)
	if verdict.Allowed() {
		t.Fatal("MySQL 8.0.12 accepts a parenthesised expression default, and it gained them in 8.0.13")
	}

	for _, part := range []string{"8.0.13", "id"} {
		if !strings.Contains(verdict.Reason(), part) {
			t.Errorf("the refusal does not name %q: %s", part, verdict.Reason())
		}
	}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0, patch: 13},
		{variant: MySQL, major: 8, minor: 4},
		{variant: MariaDB, major: 10, minor: 5},
	} {
		if !declaredFor(t, version).Resolve(op).Allowed() {
			t.Errorf("%s refuses an expression default", version)
		}
	}

	t.Log("This is the version-dependent verdict invariant #13 asks for, and it is resolved from the")
	t.Log("server rather than assumed: below 8.0.13 there is no spelling of a uuid() default MySQL")
	t.Log("accepts at all, bare or parenthesised. MariaDB has had them since 10.2, below this")
	t.Log("driver's floor, so the gate is MySQL's alone.")
}

func TestALiteralDefaultOnATextColumnIsGatedTheSameWay(t *testing.T) {
	op := ir.CreateTable{Table: "posts", Columns: []ir.Column{
		{Name: "body", Type: mustType(t, "text"), Default: &ir.Default{Literal: literal("x")}},
	}}

	if declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 12}).
		Resolve(op).Allowed() {
		t.Error("a text column's literal default is allowed on 8.0.12, where it has no spelling")
	}

	if !declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}).
		Resolve(op).Allowed() {
		t.Error("a text column's literal default is refused on 8.0.46")
	}

	t.Log("MySQL takes no bare literal default on a text, blob or json column -- error 1101 -- so")
	t.Log("the parenthesised form is the only one, which makes it an expression default too.")
}

func TestAnOrdinaryColumnIsNotGatedOnTheExpressionDefaultRelease(t *testing.T) {
	op := ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "email", Type: mustType(t, "string(255)"),
			Default: &ir.Default{Literal: literal("x")}},
		{Name: "at", Type: mustType(t, "timestamptz"),
			Default: &ir.Default{Function: ir.FuncCurrentTimestamp}},
	}}

	if !declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0}).Resolve(op).Allowed() {
		t.Error("a bare literal and a CURRENT_TIMESTAMP default are gated on 8.0.13, and neither is")
	}
}

func TestEveryAllowedVerdictCarriesALockImpactUntilM9T3ResolvesOne(t *testing.T) {
	set := declaredFor(t, serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46})

	for _, op := range operationsFor(t) {
		verdict := set.Resolve(op)
		if !verdict.Allowed() {
			continue
		}

		if verdict.Lock() != capability.LockUnknown {
			t.Errorf("%s declares %s, and M9-T3 is where a real verdict is resolved", op.Kind(),
				verdict.Lock())
		}
	}

	t.Log("LockUnknown ranks equal to TableRewrite for every threshold comparison, so declaring it")
	t.Log("is pessimistic rather than permissive. M9-T3 resolves the real answer from the ALGORITHM")
	t.Log("that applies, which depends on the column type and the alteration and so cannot be a")
	t.Log("static table.")
}
