package mysql

import (
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// current is the release both containers are at or above, which most verdicts are measured against.
var current = serverFacts{version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}}

// representative returns the one operation of a kind a declaration is asked about.
func representative(t *testing.T, kind ir.Kind) ir.Operation {
	t.Helper()

	for _, op := range operationsFor(t) {
		if op.Kind() == kind {
			return op
		}
	}

	t.Fatalf("no representative operation for %s", kind)

	return nil
}

// declared is what this driver says each operation costs a live database on a current MySQL.
var declared = map[ir.Kind]capability.LockImpact{
	ir.KindCreateTable:    capability.LockFree,
	ir.KindCreateView:     capability.LockFree,
	ir.KindDropView:       capability.BriefExclusive,
	ir.KindRenameTable:    capability.BriefExclusive,
	ir.KindRenameColumn:   capability.BriefExclusive,
	ir.KindAddColumn:      capability.BriefExclusive,
	ir.KindDropColumn:     capability.BriefExclusive,
	ir.KindDropIndex:      capability.BriefExclusive,
	ir.KindDropUnique:     capability.BriefExclusive,
	ir.KindDropForeignKey: capability.BriefExclusive,
	ir.KindDropCheck:      capability.BriefExclusive,
	ir.KindDropTable:      capability.LongExclusive,
	ir.KindAddIndex:       capability.LongExclusive,
	ir.KindAddUnique:      capability.LongExclusive,
	ir.KindAlterColumn:    capability.TableRewrite,
	ir.KindAddCheck:       capability.TableRewrite,
	ir.KindAddForeignKey:  capability.TableRewrite,
	ir.KindAddPrimaryKey:  capability.TableRewrite,
	ir.KindDropPrimaryKey: capability.TableRewrite,
	ir.KindRaw:            capability.LockUnknown,
	ir.KindDataCopy:       capability.LongExclusive,
	ir.KindDataBackfill:   capability.TableRewrite,
	ir.KindDataTransform:  capability.TableRewrite,
}

func TestEveryOperationCostsSomethingThisDriverCanName(t *testing.T) {
	for _, kind := range ir.Kinds() {
		want, listed := declared[kind]
		if !listed {
			t.Fatalf("%s is an operation this driver's lock table says nothing about", kind)
		}

		if got := current.lockImpact(representative(t, kind)); got != want {
			t.Errorf("lockImpact(%s) = %s, want %s", kind, got, want)
		}

		if !want.Valid() {
			t.Errorf("%s is declared %q, which no ceiling recognizes", kind, want)
		}
	}

	t.Log("The table is walked from ir.Kinds() rather than from the driver's own switch, so a")
	t.Log("twenty-first operation fails here instead of falling to the default arm and reading")
	t.Log("`unknown` for the rest of its life. Every row is a measurement: the statement was run")
	t.Log("against 8.0.46 and 11.4.13 with ALGORITHM= asked for and the InnoDB table id read either")
	t.Log("side, and impact_live_test.go runs the same two instruments as a test.")
}

func TestOnlyTheTwoOperationsThatTouchNothingExistingAreLockFree(t *testing.T) {
	free := map[ir.Kind]bool{ir.KindCreateTable: true, ir.KindCreateView: true}

	for _, kind := range ir.Kinds() {
		impact := current.lockImpact(representative(t, kind))
		if (impact == capability.LockFree) != free[kind] {
			t.Errorf("%s is declared %s, and lock_free is %v for it", kind, impact, free[kind])
		}
	}

	t.Log("This is where M9-T3's own done-when was corrected: it reads \"INSTANT is LockFree\", and")
	t.Log("The verdicts had already rejected that for PostgreSQL's instant ADD COLUMN ... DEFAULT. An")
	t.Log("INSTANT alteration is metadata-only once it holds its lock, and it still requests an")
	t.Log("exclusive metadata lock and still waits for it -- so it can hold a table hostage for the")
	t.Log("length of the long-running SELECT ahead of it, which is invariant #12's whole subject.")
	t.Log("LockFree would let --require-safe pass that. So the two LockFree operations here are")
	t.Log("PostgreSQL's two, and for PostgreSQL's reason: neither touches an object that exists.")
}

func TestReplacingAViewLocksTheViewItOverwrites(t *testing.T) {
	fresh := ir.CreateView{View: "listing", Query: "SELECT 1"}
	if got := current.lockImpact(fresh); got != capability.LockFree {
		t.Errorf("a new view is declared %s", got)
	}

	replacement := ir.CreateView{View: "listing", Query: "SELECT 1", OrReplace: true}
	if got := current.lockImpact(replacement); got != capability.BriefExclusive {
		t.Errorf("a replaced view is declared %s", got)
	}
}

func TestAddingAColumnIsAMetadataChangeOnlyFromTheReleaseThatMadeItOne(t *testing.T) {
	op := ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname",
		Type: mustType(t, "string(64)"), Nullable: true}}

	for _, c := range []struct {
		version serverVersion
		want    capability.LockImpact
	}{
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 11}, capability.TableRewrite},
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 12}, capability.BriefExclusive},
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}, capability.BriefExclusive},
		{serverVersion{variant: MariaDB, major: 10, minor: 5}, capability.BriefExclusive},
	} {
		facts := serverFacts{version: c.version}
		if got := facts.lockImpact(op); got != c.want {
			t.Errorf("%s declares %s for an added column, want %s", c.version, got, c.want)
		}
	}

	t.Log("8.0.11 is inside the supported range -- the floor is 8.0 -- so this boundary is reachable")
	t.Log("by a server and unreachable by either container, which is how invariant #13")
	t.Log("reads here: the version is asked for, never hardcoded, and a branch no available server produces")
	t.Log("is still written and held to a test. MariaDB has added instantly since 10.4.3, below this")
	t.Log("driver's own floor, so it never reaches the rewrite branch at all.")
}

func TestAColumnAddedWithADefaultMySQLCannotStoreRewritesEveryRow(t *testing.T) {
	for name, c := range map[string]struct {
		column ir.Column
		mysql  capability.LockImpact
		maria  capability.LockImpact
	}{
		"no default": {
			column: ir.Column{Name: "c", Type: mustType(t, "int32"), Nullable: true},
			mysql:  capability.BriefExclusive, maria: capability.BriefExclusive,
		},
		"a literal on a type that takes one bare": {
			column: ir.Column{Name: "c", Type: mustType(t, "int32"), Nullable: true,
				Default: ir.Literal("5")},
			mysql: capability.BriefExclusive, maria: capability.BriefExclusive,
		},
		"a current_timestamp default, which is bare too": {
			column: ir.Column{Name: "c", Type: mustType(t, "timestamptz"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentTimestamp)},
			mysql: capability.BriefExclusive, maria: capability.BriefExclusive,
		},
		"a literal on a text column, which must be parenthesised": {
			column: ir.Column{Name: "c", Type: mustType(t, "text"), Nullable: true,
				Default: ir.Literal("x")},
			mysql: capability.TableRewrite, maria: capability.BriefExclusive,
		},
		"a current_date default, which must be parenthesised": {
			column: ir.Column{Name: "c", Type: mustType(t, "date"), Nullable: true,
				Default: ir.Call(ir.FuncCurrentDate)},
			mysql: capability.TableRewrite, maria: capability.BriefExclusive,
		},
		"a uuid default, whose value differs per row": {
			column: ir.Column{Name: "c", Type: mustType(t, "uuid"), Nullable: true,
				Default: ir.Call(ir.FuncUUID)},
			mysql: capability.TableRewrite, maria: capability.TableRewrite,
		},
	} {
		t.Run(name, func(t *testing.T) {
			op := ir.AddColumn{Table: "users", Column: c.column}

			if got := current.lockImpact(op); got != c.mysql {
				t.Errorf("MySQL declares %s, want %s", got, c.mysql)
			}

			maria := serverFacts{version: serverVersion{variant: MariaDB, major: 11, minor: 4}}
			if got := maria.lockImpact(op); got != c.maria {
				t.Errorf("MariaDB declares %s, want %s", got, c.maria)
			}
		})
	}

	t.Log("The two servers disagree, and each answer is measured. MySQL 8.0.46 refuses ALGORITHM=")
	t.Log("INSTANT for any parenthesised default and rewrites the table -- DEFAULT (5) as much as")
	t.Log("DEFAULT (CURDATE()). MariaDB 11.4.13 adds every one of those instantly and rewrites only")
	t.Log("for DEFAULT (UUID()), whose value cannot be one stored constant. So this is the same")
	t.Log("distinction PostgreSQL draws by reading pg_proc.provolatile, reached from the other side:")
	t.Log("there the question is which defaults are volatile, here which the engine can store.")
}

func TestDroppingAColumnIsAMetadataChangeOnlyFromTheReleaseThatMadeItOne(t *testing.T) {
	op := ir.DropColumn{Table: "users", Column: "nickname"}

	for _, c := range []struct {
		version serverVersion
		want    capability.LockImpact
	}{
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 28}, capability.TableRewrite},
		{serverVersion{variant: MySQL, major: 8, minor: 0, patch: 29}, capability.BriefExclusive},
		{serverVersion{variant: MySQL, major: 8, minor: 4}, capability.BriefExclusive},
		{serverVersion{variant: MariaDB, major: 10, minor: 5}, capability.BriefExclusive},
	} {
		facts := serverFacts{version: c.version}
		if got := facts.lockImpact(op); got != c.want {
			t.Errorf("%s declares %s for a dropped column, want %s", c.version, got, c.want)
		}
	}

	t.Log("MySQL gained the instant drop at 8.0.29 and MariaDB at 10.4.3, which is below this")
	t.Log("driver's floor -- so between 8.0 and 8.0.28 the two variants give opposite answers for")
	t.Log("one operation, and that span is inside the supported range. Measured on 8.0.46 and")
	t.Log("11.4.13, where both are instant and the InnoDB table id does not move.")
}

func TestOnlyADefaultMovingLeavesTheRedefinitionAMetadataChange(t *testing.T) {
	for name, c := range map[string]struct {
		op   ir.AlterColumn
		want capability.LockImpact
	}{
		"dropping a default": {
			op:   ir.AlterColumn{Table: "users", Column: "n", DropDefault: true},
			want: capability.BriefExclusive,
		},
		"setting a default": {
			op:   ir.AlterColumn{Table: "users", Column: "n", Default: ir.Literal("9")},
			want: capability.BriefExclusive,
		},
		"a retype": {
			op: ir.AlterColumn{Table: "users", Column: "n",
				Type: typePointer(t, "int64")},
			want: capability.TableRewrite,
		},
		"a nullability change": {
			op:   ir.AlterColumn{Table: "users", Column: "n", Nullable: boolPointer(false)},
			want: capability.TableRewrite,
		},
		"a retype that also sets a default": {
			op: ir.AlterColumn{Table: "users", Column: "n", Type: typePointer(t, "int64"),
				Default: ir.Literal("9")},
			want: capability.TableRewrite,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got := current.lockImpact(c.op); got != c.want {
				t.Errorf("declared %s, want %s", got, c.want)
			}
		})
	}

	t.Log("One ir.Kind, two lock verdicts, which is the shape M9-T2 gave the support axis for the")
	t.Log("same operation -- and it is decidable from the payload alone because naming neither a type")
	t.Log("nor a nullability is exactly what makes restate() carry both through from the catalog")
	t.Log("verbatim. A MODIFY COLUMN that changes only the DEFAULT is INSTANT on both servers,")
	t.Log("measured, including on a longtext where SET DEFAULT is error 1101 and the redefinition is")
	t.Log("the only statement there is.")
}

func TestARetypeIsDeclaredARewriteEvenWhereTheServerWouldNotRewrite(t *testing.T) {
	widened := ir.AlterColumn{Table: "users", Column: "email", Type: typePointer(t, "string(512)")}

	if got := current.lockImpact(widened); got != capability.TableRewrite {
		t.Errorf("a widened varchar is declared %s", got)
	}

	t.Log("Measured: MySQL widens a varchar with ALGORITHM=INPLACE and no rebuild, and MariaDB does")
	t.Log("it instantly -- so this is an over-report, deliberately. Telling a widening from a")
	t.Log("narrowing needs the column's current type, which the operation carries only as `from`:")
	t.Log("what an author recorded, not what the column is. Keying the verdict to it was rejected")
	t.Log("on merit rather than cost, because over-reporting costs a flag and under-reporting")
	t.Log("costs an outage. The expansion does read the real type -- that is the condition named")
	t.Log("for revisiting -- but Resolve performs no I/O, so the precise answer is reachable")
	t.Log("from the expansion path and not from here. M13-T6 is where a Set becomes a Planner field.")
}

func TestTheWholeTableIsRewrittenForTheThreeConstraintsThatValidateIt(t *testing.T) {
	for _, op := range []ir.Operation{
		ir.AddCheck{Table: "users", Name: "ck", Expression: "n >= 0"},
		ir.AddForeignKey{Table: "orders", Name: "fk", Columns: []string{"user_id"},
			ReferencesTable: "users", ReferencesColumns: []string{"id"}},
		ir.AddPrimaryKey{Table: "legacy", Name: "pk", Columns: []string{"id"}},
	} {
		if got := current.lockImpact(op); got != capability.TableRewrite {
			t.Errorf("%s is declared %s", op.Kind(), got)
		}
	}

	t.Log("All three contradicted the plan, which had add_check and add_foreign_key as")
	t.Log("LongExclusive. Measured on both servers: ADD CONSTRAINT ... CHECK and ADD FOREIGN KEY are")
	t.Log("both ALGORITHM=COPY and both move the InnoDB table id, and ADD PRIMARY KEY is INPLACE and")
	t.Log("moves it too -- an INPLACE that rebuilds. PostgreSQL calls the first two LongExclusive")
	t.Log("because it validates under a lock without rewriting; InnoDB rebuilds the clustered index.")
	t.Log("add_foreign_key is COPY because foreign_key_checks is on, which this driver leaves alone:")
	t.Log("MySQL offers INPLACE for an added key only when the checks are off.")
}

func TestBuildingAnIndexScalesWithTheTableAndDroppingOneDoesNot(t *testing.T) {
	for _, op := range []ir.Operation{
		ir.AddIndex{Table: "users", Name: "ix", Columns: []string{"email"}},
		ir.AddUnique{Table: "users", Name: "uq", Columns: []string{"email"}},
	} {
		if got := current.lockImpact(op); got != capability.LongExclusive {
			t.Errorf("%s is declared %s", op.Kind(), got)
		}
	}

	for _, op := range []ir.Operation{
		ir.DropIndex{Table: "users", Name: "ix"},
		ir.DropUnique{Table: "users", Name: "uq"},
	} {
		if got := current.lockImpact(op); got != capability.BriefExclusive {
			t.Errorf("%s is declared %s", op.Kind(), got)
		}
	}

	t.Log("Both halves are ALGORITHM=INPLACE with no rebuild, measured, so the algorithm cannot")
	t.Log("separate them and the work does: an index build reads every row and a drop is a catalog")
	t.Log("change. The verdicts settled the vocabulary -- add_index is LongExclusive even where readers")
	t.Log("are admitted, because there is no verdict for \"blocks writers only\" and a fifth would be")
	t.Log("one more value in safety.max_lock_impact than an operator can order.")
}

func TestRawIsUnknownOnEveryServerAndAlwaysWillBe(t *testing.T) {
	op := ir.Raw{Driver: Name, SQL: "ANALYZE TABLE `users`"}

	for _, version := range []serverVersion{
		{variant: MySQL, major: 8, minor: 0},
		{variant: MySQL, major: 8, minor: 4},
		{variant: MariaDB, major: 11, minor: 4},
	} {
		facts := serverFacts{version: version}
		if got := facts.lockImpact(op); got != capability.LockUnknown {
			t.Errorf("%s declares %s for raw", version, got)
		}
	}
}

func TestAnInstantColumnChangeIsDeclaredARewriteUntilTheTableIsShownToAllowIt(t *testing.T) {
	added := ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname",
		Type: mustType(t, "string(64)"), Nullable: true}}
	dropped := ir.DropColumn{Table: "users", Column: "nickname"}

	mysql8028 := serverVersion{variant: MySQL, major: 8, minor: 0, patch: 28}
	mysql8029 := serverVersion{variant: MySQL, major: 8, minor: 0, patch: 29}
	maria := serverVersion{variant: MariaDB, major: 11, minor: 4}

	for _, c := range []struct {
		name    string
		version serverVersion
		op      ir.Operation
		want    string
	}{
		{"an add before row versions", mysql8028, added, instantTableCondition},
		{"an add counting row versions", mysql8029, added, instantTableCondition + rowVersionCondition},
		{"a drop counting row versions", mysql8029, dropped,
			instantTableCondition + rowVersionCondition + indexedColumnCondition},
		{"an add on MariaDB", maria, added, instantTableCondition},
		{"a drop on MariaDB", maria, dropped, instantTableCondition},
	} {
		t.Run(c.name, func(t *testing.T) {
			v := serverFacts{version: c.version}.verdict(c.op)

			if v.Lock() != capability.TableRewrite || v.Lowest() != capability.BriefExclusive {
				t.Errorf("verdict = %s, want table_rewrite lowerable to brief_exclusive", v)
			}

			if v.Condition() != c.want {
				t.Errorf("condition = %q, want %q", v.Condition(), c.want)
			}
		})
	}

	t.Log("Measured on 8.0.46 and 11.4.13 with the table id read either side: a COMPRESSED table and")
	t.Log("one carrying a FULLTEXT index are rebuilt by both servers, a table at 64 row versions by")
	t.Log("MySQL, and dropping an indexed column by MySQL alone -- MariaDB drops it in place. None of")
	t.Log("that is in the operation, so Resolve reports the rebuild and Assess reads the table.")
}

func TestAColumnChangeThatAlwaysRewritesCarriesNoCondition(t *testing.T) {
	added := ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname",
		Type: mustType(t, "string(64)"), Nullable: true}}
	dropped := ir.DropColumn{Table: "users", Column: "nickname"}

	for name, c := range map[string]struct {
		version serverVersion
		op      ir.Operation
	}{
		"an add before instant adds":  {serverVersion{variant: MySQL, major: 8, minor: 0, patch: 11}, added},
		"a drop before instant drops": {serverVersion{variant: MySQL, major: 8, minor: 0, patch: 28}, dropped},
	} {
		v := serverFacts{version: c.version}.verdict(c.op)
		if v.Lock() != capability.TableRewrite || v.Condition() != "" {
			t.Errorf("%s: verdict = %s, want a table_rewrite no table can lower", name, v)
		}
	}

	for _, kind := range ir.Kinds() {
		if kind == ir.KindAddColumn || kind == ir.KindDropColumn {
			continue
		}

		if v := current.verdict(representative(t, kind)); v.Condition() != "" {
			t.Errorf("%s carries the condition %q, and no other operation was measured to need one",
				kind, v.Condition())
		}
	}

	t.Log("A default change and a rename stayed instant on every table shape measured, COMPRESSED and")
	t.Log("FULLTEXT included, so they carry no condition: one that cannot fail would only cost a read.")
}
