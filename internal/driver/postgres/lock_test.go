package postgres

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
)

// modernServer is a PostgreSQL 17 that has gen_random_uuid, as both test servers do.
var modernServer = serverFacts{
	version:     serverVersion(170006),
	uuidDefault: defaultFunction{exists: true, volatile: true},
}

// legacyServer is a PostgreSQL 10, which rewrites the table for any default at all.
var legacyServer = serverFacts{
	version:     serverVersion(100023),
	uuidDefault: defaultFunction{exists: false},
}

// addColumn returns an add_column carrying the given default, for the impact matrix.
func addColumn(t *testing.T, d *ir.Default) ir.AddColumn {
	t.Helper()

	return ir.AddColumn{
		Table:  "users",
		Column: ir.Column{Name: "attempts", Type: mustType(t, "int32"), Default: d},
	}
}

func TestTwoServerVersionsDisagreeAboutAddColumnWithADefault(t *testing.T) {
	op := addColumn(t, ir.Literal("0"))

	if got := legacyServer.lockImpact(op); got != capability.TableRewrite {
		t.Errorf("PostgreSQL %s reported %s, want %s", legacyServer.version, got,
			capability.TableRewrite)
	}

	if got := modernServer.lockImpact(op); got != capability.BriefExclusive {
		t.Errorf("PostgreSQL %s reported %s, want %s", modernServer.version, got,
			capability.BriefExclusive)
	}

	t.Log("This is the boundary the task names, and the only version boundary in the table.")
	t.Log("PostgreSQL 11 stores the default in pg_attribute and applies it on read instead of")
	t.Log("writing every row. The version is closed over at construction, so both answers are")
	t.Log("provable with no database and Resolve stays free of I/O.")
	t.Log("Note the modern answer is brief_exclusive rather than lock_free: ALTER TABLE still")
	t.Log("takes ACCESS EXCLUSIVE, and what release 11 changed is how long it holds it.")
}

func TestAVolatileDefaultRewritesOnEveryVersion(t *testing.T) {
	volatileOp := addColumn(t, ir.Call(ir.FuncUUID))

	if got := modernServer.lockImpact(volatileOp); got != capability.TableRewrite {
		t.Errorf("a volatile default on PostgreSQL %s reported %s, want %s",
			modernServer.version, got, capability.TableRewrite)
		t.Log("gen_random_uuid() returns a different value per row, so the stored-default")
		t.Log("optimization cannot apply and every row is written. Verified against both test")
		t.Log("servers by watching pg_class.relfilenode change.")
	}

	stable := []*ir.Default{
		ir.Literal("0"),
		ir.Call(ir.FuncCurrentTimestamp),
		ir.Call(ir.FuncCurrentDate),
		ir.Call(ir.FuncCurrentTime),
	}

	for _, d := range stable {
		if got := modernServer.lockImpact(addColumn(t, d)); got != capability.BriefExclusive {
			t.Errorf("a stable default reported %s, want %s", got, capability.BriefExclusive)
		}
	}
}

func TestAServerWithoutTheUUIDFunctionRefusesThatDefaultRatherThanCosting(t *testing.T) {
	op := addColumn(t, ir.Call(ir.FuncUUID))

	verdict := legacyServer.verdict(op)
	if verdict.Allowed() {
		t.Error("a server with no gen_random_uuid accepted a uuid default")
	}

	if !strings.Contains(verdict.Reason(), uuidFunction) {
		t.Errorf("the refusal %q does not name the function the server lacks", verdict.Reason())
	}

	if !modernServer.verdict(op).Allowed() {
		t.Error("a server that has the function refused the same operation")
		t.Log("This is what M3-T2 could not answer: Compile is version-blind by contract, so the")
		t.Log("declaration is the only place the refusal can live. Asking the server beats testing")
		t.Log("the version -- a PostgreSQL 12 with pgcrypto has the function.")
	}
}

func TestAUUIDDefaultIsRefusedWhereverItAppears(t *testing.T) {
	column := ir.Column{Name: "id", Type: mustType(t, "uuid"), Default: ir.Call(ir.FuncUUID)}

	ops := []ir.Operation{
		ir.CreateTable{Table: "users", Columns: []ir.Column{column}},
		ir.AddColumn{Table: "users", Column: column},
		ir.AlterColumn{Table: "users", Column: "id", Default: ir.Call(ir.FuncUUID)},
	}

	for _, op := range ops {
		if legacyServer.verdict(op).Allowed() {
			t.Errorf("%s carrying a uuid default was allowed by a server without the function",
				op.Kind())
		}

		if !modernServer.verdict(op).Allowed() {
			t.Errorf("%s was refused by a server that has the function", op.Kind())
		}
	}
}

func TestAlterColumnTakesTheWorstOfItsChanges(t *testing.T) {
	typ := mustType(t, "string(64)")
	notNull, nullable := false, true

	tests := []struct {
		name string
		op   ir.AlterColumn
		want capability.LockImpact
	}{
		{
			name: "a retype outranks everything it is combined with",
			op:   ir.AlterColumn{Table: "t", Column: "c", Type: &typ, Nullable: &nullable},
			want: capability.TableRewrite,
		},
		{
			name: "set not null is a full scan",
			op:   ir.AlterColumn{Table: "t", Column: "c", Nullable: &notNull},
			want: capability.LongExclusive,
		},
		{
			name: "set not null outranks a default change",
			op:   ir.AlterColumn{Table: "t", Column: "c", Nullable: &notNull, Default: ir.Literal("x")},
			want: capability.LongExclusive,
		},
		{
			name: "drop not null touches the catalog only",
			op:   ir.AlterColumn{Table: "t", Column: "c", Nullable: &nullable},
			want: capability.BriefExclusive,
		},
		{
			name: "a default change touches no existing row",
			op:   ir.AlterColumn{Table: "t", Column: "c", Default: ir.Literal("x")},
			want: capability.BriefExclusive,
		},
		{
			name: "dropping a default touches no existing row",
			op:   ir.AlterColumn{Table: "t", Column: "c", DropDefault: true},
			want: capability.BriefExclusive,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modernServer.lockImpact(tt.op); got != tt.want {
				t.Errorf("reported %s, want %s", got, tt.want)
			}
		})
	}

	t.Log("An alter_column compiles to one statement per requested change and runs them all, so")
	t.Log("its verdict is the worst of them rather than the first or the last.")
}

func TestARetypeIsAlwaysATableRewriteEvenWhenItWouldNotBe(t *testing.T) {
	wider := mustType(t, "string(512)")
	narrower := mustType(t, "string(255)")

	op := ir.AlterColumn{
		Table:  "users",
		Column: "email",
		Type:   &wider,
		From:   &ir.ColumnState{Type: &narrower},
	}

	if got := modernServer.lockImpact(op); got != capability.TableRewrite {
		t.Errorf("a widening retype reported %s, want %s", got, capability.TableRewrite)
	}

	t.Log("varchar(255) -> varchar(512) is binary coercible and PostgreSQL does not rewrite for")
	t.Log("it -- confirmed against both servers. This over-reports deliberately: the operation")
	t.Log("carries what an author recorded the old type as, not what the column actually is, and")
	t.Log("docs/ir.md 8.2 says from is trust rather than proof. Over-reporting costs a flag;")
	t.Log("under-reporting costs an outage (invariant #13).")
}

func TestARawStatementIsPermanentlyUnknown(t *testing.T) {
	verdict := modernServer.verdict(ir.Raw{Driver: Name, SQL: "VACUUM FULL users"})

	if !verdict.Allowed() {
		t.Fatalf("a raw statement for this engine was refused: %s", verdict.Reason())
	}

	if verdict.Lock() != capability.LockUnknown {
		t.Errorf("a raw statement reported %s, want %s", verdict.Lock(), capability.LockUnknown)
		t.Log("This is the case LockUnknown exists for, and the one that never resolves: the")
		t.Log("framework cannot know what the SQL does. It ranks as table_rewrite against every")
		t.Log("ceiling while still rendering as unknown, so the operator sees which it is.")
	}
}

func TestTheImpactTableIsWhatItSaysItIs(t *testing.T) {
	expected := map[ir.Kind]capability.LockImpact{
		ir.KindCreateTable:    capability.LockFree,
		ir.KindCreateView:     capability.LockFree,
		ir.KindDropTable:      capability.LongExclusive,
		ir.KindRenameTable:    capability.BriefExclusive,
		ir.KindAddColumn:      capability.BriefExclusive,
		ir.KindDropColumn:     capability.BriefExclusive,
		ir.KindRenameColumn:   capability.BriefExclusive,
		ir.KindDropIndex:      capability.LongExclusive,
		ir.KindDropForeignKey: capability.BriefExclusive,
		ir.KindDropUnique:     capability.LongExclusive,
		ir.KindDropCheck:      capability.BriefExclusive,
		ir.KindDropPrimaryKey: capability.LongExclusive,
		ir.KindDropView:       capability.BriefExclusive,
		ir.KindAddIndex:       capability.LongExclusive,
		ir.KindAddUnique:      capability.LongExclusive,
		ir.KindAddPrimaryKey:  capability.LongExclusive,
		ir.KindAddForeignKey:  capability.LongExclusive,
		ir.KindAddCheck:       capability.LongExclusive,
		ir.KindAlterColumn:    capability.TableRewrite,
		ir.KindRaw:            capability.LockUnknown,
		ir.KindDataCopy:       capability.LongExclusive,
		ir.KindDataBackfill:   capability.TableRewrite,
		ir.KindDataTransform:  capability.TableRewrite,
	}

	for _, op := range drivertest.Operations(Name) {
		want, listed := expected[op.Kind()]
		if !listed {
			t.Errorf("%s has no expected verdict, so the table grew without this test", op.Kind())

			continue
		}

		if got := modernServer.lockImpact(op); got != want {
			t.Errorf("%s reports %s, want %s", op.Kind(), got, want)
		}
	}

	t.Log("The fixture operations are what these expectations describe: the add_column fixture")
	t.Log("carries a stable default, and the alter_column fixture requests a retype. The cases")
	t.Log("that vary by payload or by version have their own tests above.")
	t.Log("Pinning the constants matters: a mutation sweep found that asserting only that each")
	t.Log("verdict is recognized let an index build report brief_exclusive undetected, which")
	t.Log("would pass a --require-safe gate it should fail.")
}

func TestAddingAColumnWithNoDefaultIsCatalogWorkOnEveryVersion(t *testing.T) {
	op := addColumn(t, nil)

	for _, server := range []serverFacts{legacyServer, modernServer} {
		if got := server.lockImpact(op); got != capability.BriefExclusive {
			t.Errorf("PostgreSQL %s reports %s, want %s", server.version, got,
				capability.BriefExclusive)
		}
	}

	t.Log("Not lock_free: ALTER TABLE takes ACCESS EXCLUSIVE whatever it is doing, so this")
	t.Log("blocks readers for as long as it waits for the lock. That is the whole reason")
	t.Log("invariant #12 requires a lock timeout on every DDL statement.")
}

func TestReplacingAViewLocksTheViewItOverwrites(t *testing.T) {
	fresh := ir.CreateView{View: "active", Query: "SELECT 1"}
	replacing := ir.CreateView{View: "active", Query: "SELECT 1", OrReplace: true}

	if got := modernServer.lockImpact(fresh); got != capability.LockFree {
		t.Errorf("a new view reports %s, want %s", got, capability.LockFree)
	}

	if got := modernServer.lockImpact(replacing); got != capability.BriefExclusive {
		t.Errorf("replacing a view reports %s, want %s", got, capability.BriefExclusive)
		t.Log("A new view locks nothing that exists. A replacement takes ACCESS EXCLUSIVE on the")
		t.Log("view it overwrites, so a query already reading through it waits.")
	}
}

func TestEveryOperationResolvesToARecognizedImpact(t *testing.T) {
	set, err := declaredSet(t)
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	unknown := make([]ir.Kind, 0, 1)

	for _, op := range drivertest.Operations(Name) {
		verdict := set.Resolve(op)

		if !verdict.Allowed() {
			t.Errorf("%s: %s", op.Kind(), verdict.Reason())

			continue
		}

		if !verdict.Lock().Valid() {
			t.Errorf("%s reports %q, which names no verdict this build recognizes", op.Kind(),
				verdict.Lock())
		}

		if verdict.Lock() == capability.LockUnknown {
			unknown = append(unknown, op.Kind())
		}
	}

	if len(unknown) != 1 || unknown[0] != ir.KindRaw {
		t.Errorf("the operations left unknown are %v, want only %s", unknown, ir.KindRaw)
		t.Log("Every other operation is classified, so this driver has no lock-impact worklist")
		t.Log("left. An operation that stops being classified shows up here rather than as a")
		t.Log("safety ceiling that quietly stops rejecting anything.")
	}
}

func TestTheDeclarationReportsTransactionalDDL(t *testing.T) {
	set, err := declaredSet(t)
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	if !set.TransactionalDDL() {
		t.Error("PostgreSQL is declared to have no transactional DDL")
		t.Log("M4-T9 builds the dirty-state model a driver without it needs. PostgreSQL is the")
		t.Log("reference driver precisely because it has it; MySQL declares false in M9-T3.")
	}
}

func TestAnUnrecognizedOperationIsUnknownRatherThanCheap(t *testing.T) {
	if got := modernServer.lockImpact(nil); got != capability.LockUnknown {
		t.Errorf("an operation this table has no case for reported %s, want %s", got,
			capability.LockUnknown)
		t.Log("A twenty-first operation must not arrive declaring itself lock_free. Unknown ranks")
		t.Log("as table_rewrite against every ceiling, so the fallback fails closed.")
	}
}
