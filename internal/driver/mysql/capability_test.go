package mysql

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
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

	return ir.Operations{
		ir.CreateTable{Table: "users", Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")},
		}},
		ir.AddIndex{Table: "users", Name: "users_id_idx", Columns: []string{"id"}},
	}
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
