//go:build integration

package mysql

import (
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// probeRows is enough rows that a rebuild is a real one rather than a metadata shuffle.
const probeRows = 3000

// errAlgorithmUnsupported is ER_ALTER_OPERATION_NOT_SUPPORTED, the server refusing an algorithm.
//
// 1846 is its _REASON variant, which both servers answer with for most operations; the plain 1845
// comes back for a few. Either is the server saying it cannot do this that way.
const (
	errAlgorithmUnsupported = 1845
	errAlgorithmReason      = 1846
)

// tableIDQuery reads what InnoDB calls a table, which it stops calling it when it rebuilds it.
func tableIDQuery(v Variant) string {
	if v == MariaDB {
		return "SELECT TABLE_ID FROM information_schema.INNODB_SYS_TABLES WHERE NAME = ?"
	}

	return "SELECT TABLE_ID FROM information_schema.INNODB_TABLES WHERE NAME = ?"
}

// probe is one operation, run against a populated table and watched for a rebuild.
type probe struct {
	// op is the operation whose declared verdict is being checked against the server.
	op ir.Operation
	// setup is what must exist before the operation can run, each run through the compiler too.
	setup []ir.Operation
	// named is the table the operation leaves behind, where that is not the one it started on.
	named string
}

// probeTable is the one table every probe alters, named per test so cases cannot collide.
func probeTable(t *testing.T) string {
	t.Helper()

	return uniqueName(t) + "_p"
}

// parentTable is what a foreign key probe points at, which must exist and must have the rows.
func parentTable(t *testing.T) string {
	t.Helper()

	return uniqueName(t) + "_q"
}

// probes returns an operation per declared verdict, each able to run against the seeded table.
func probes(t *testing.T) map[string]probe {
	t.Helper()

	table, parent := probeTable(t), parentTable(t)
	index := ir.AddIndex{Table: table, Name: table + "_i", Columns: []string{"email"}}
	unique := ir.AddUnique{Table: table, Name: table + "_u", Columns: []string{"email"}}
	check := ir.AddCheck{Table: table, Name: table + "_c", Expression: "n >= 0"}
	key := ir.AddForeignKey{Table: table, Name: table + "_f", Columns: []string{"pid"},
		ReferencesTable: parent, ReferencesColumns: []string{"id"}}
	dropKey := ir.DropPrimaryKey{Table: table}

	return map[string]probe{
		"add_column": {op: ir.AddColumn{Table: table, Column: ir.Column{
			Name: "plain", Type: liveType(t, "int32"), Nullable: true,
		}}},
		"add_column with a literal default": {op: ir.AddColumn{Table: table, Column: ir.Column{
			Name: "literal", Type: liveType(t, "int32"), Nullable: true, Default: ir.Literal("5"),
		}}},
		"add_column with a parenthesised default": {op: ir.AddColumn{Table: table, Column: ir.Column{
			Name: "stamped", Type: liveType(t, "date"), Nullable: true,
			Default: ir.Call(ir.FuncCurrentDate),
		}}},
		"drop_column":   {op: ir.DropColumn{Table: table, Column: "n"}},
		"rename_column": {op: ir.RenameColumn{Table: table, From: "n", To: "m"}},
		"rename_table": {op: ir.RenameTable{From: table, To: table + "x"},
			named: table + "x"},
		"alter_column dropping a default": {op: ir.AlterColumn{Table: table, Column: "n",
			DropDefault: true}},
		"add_index":  {op: index},
		"drop_index": {op: ir.DropIndex{Table: table, Name: index.Name}, setup: []ir.Operation{index}},
		"add_unique": {op: unique},
		"drop_unique": {op: ir.DropUnique{Table: table, Name: unique.Name},
			setup: []ir.Operation{unique}},
		"add_check": {op: check},
		"drop_check": {op: ir.DropCheck{Table: table, Name: check.Name},
			setup: []ir.Operation{check}},
		"add_foreign_key": {op: key},
		"drop_foreign_key": {op: ir.DropForeignKey{Table: table, Name: key.Name},
			setup: []ir.Operation{key}},
		"drop_primary_key": {op: dropKey},
		"add_primary_key": {op: ir.AddPrimaryKey{Table: table, Name: table + "_pk",
			Columns: []string{"id"}}, setup: []ir.Operation{dropKey}},
	}
}

// seed builds the populated table every probe runs against, and the parent a foreign key needs.
func seed(t *testing.T, c *mysqlConn) {
	t.Helper()

	table, parent := probeTable(t), parentTable(t)

	drop(t.Context(), c, table, table+"x", parent)

	apply(t, c, ir.CreateTable{Table: parent, Columns: []ir.Column{
		{Name: "id", Type: liveType(t, "int64")},
	}, PrimaryKey: []string{"id"}})

	apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
		{Name: "id", Type: liveType(t, "int64")},
		{Name: "pid", Type: liveType(t, "int64")},
		{Name: "n", Type: liveType(t, "int32"), Default: ir.Literal("7")},
		{Name: "email", Type: liveType(t, "string(100)")},
	}, PrimaryKey: []string{"id"}})

	fill(t, c, parent, "INSERT INTO "+quote(parent)+" (`id`) SELECT `i` FROM "+numbers(c))
	fill(t, c, table, "INSERT INTO "+quote(table)+" (`id`, `pid`, `n`, `email`) "+
		"SELECT `i`, `i`, `i`, concat('e', `i`, '@example.com') FROM "+numbers(c))
}

// numbers renders a derived table of probeRows rows, which the two servers spell differently.
func numbers(c *mysqlConn) string {
	if c.version.variant == MariaDB {
		return fmt.Sprintf("(SELECT `seq` AS `i` FROM seq_1_to_%d) AS `s`", probeRows)
	}

	return fmt.Sprintf("(WITH RECURSIVE `q`(`i`) AS (SELECT 1 UNION ALL SELECT `i`+1 FROM `q` "+
		"WHERE `i` < %d) SELECT `i` FROM `q`) AS `s`", probeRows)
}

// fill runs one seeding statement, raising MySQL's recursion ceiling for the one that needs it.
func fill(t *testing.T, c *mysqlConn, table, sql string) {
	t.Helper()

	if c.version.variant != MariaDB {
		exec(t, c, fmt.Sprintf("SET SESSION cte_max_recursion_depth = %d", probeRows+1))
	}

	exec(t, c, sql)

	if rows := read(t, c, "SELECT count(*) FROM "+quote(table)); rows[0] != fmt.Sprint(probeRows) {
		t.Fatalf("%s holds %s rows, want %d", table, rows[0], probeRows)
	}
}

// tableID asks InnoDB what it currently calls a table, and reports nothing for one it has dropped.
func tableID(t *testing.T, c *mysqlConn, table string) string {
	t.Helper()

	rows := read(t, c, tableIDQuery(c.version.variant),
		c.database+"/"+storedName(table, c.folding))
	if len(rows) == 0 {
		t.Fatalf("InnoDB holds no table called %q, so no rebuild can be detected", table)
	}

	return rows[0]
}

// rebuilt runs one probe and reports whether InnoDB replaced the table underneath it.
func rebuilt(t *testing.T, c *mysqlConn, p probe) bool {
	t.Helper()

	seed(t, c)

	for _, before := range p.setup {
		apply(t, c, before)
	}

	was := tableID(t, c, probeTable(t))

	apply(t, c, p.op)

	return tableID(t, c, p.landed(t)) != was
}

// assessedLock is what the session says a probe costs once it has read the seeded table it runs on.
func assessedLock(t *testing.T, c *mysqlConn, caps capability.Set, p probe) capability.LockImpact {
	t.Helper()

	seed(t, c)

	for _, before := range p.setup {
		apply(t, c, before)
	}

	v, err := c.Assess(t.Context(), p.op, caps.Resolve(p.op))
	if err != nil {
		t.Fatalf("Assess(%s): %v", p.op.Kind(), err)
	}

	return v.Lock()
}

// landed names the table a probe's operation left behind, which a rename moves.
func (p probe) landed(t *testing.T) string {
	t.Helper()

	if p.named != "" {
		return p.named
	}

	return probeTable(t)
}

func TestNoTableRewriteHappensThatWasNotDeclared(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		dropLater(t, c, probeTable(t), probeTable(t)+"x", parentTable(t))

		caps, rewrites := c.Capabilities(), 0

		for name, p := range probes(t) {
			declared := assessedLock(t, c, caps, p)

			if !rebuilt(t, c, p) {
				if declared == capability.TableRewrite {
					t.Logf("over-reported: %s is declared %s and rewrote nothing", name, declared)
				}

				continue
			}

			rewrites++

			if declared != capability.TableRewrite {
				t.Errorf("%s rewrote the table and is declared %s", name, declared)
			}
		}

		t.Logf("%d of %d probes rewrote the table", rewrites, len(probes(t)))

		if rewrites == 0 {
			t.Error("no probe rewrote anything, so this test proved nothing")
			t.Log("Five must rewrite: add_check, add_foreign_key, add_primary_key, drop_primary_key,")
			t.Log("and add_column with a parenthesised default on MySQL. If none did, the oracle is")
			t.Log("watching the wrong thing.")
		}
	})

	t.Log("A rebuild gives the table a new InnoDB TABLE_ID, which is the one axis a server settles")
	t.Log("directly. The id is read back under whatever name the table then has, and the schema is")
	t.Log("part of the name the catalog holds: a rename keeps the id and changes the name, so a probe")
	t.Log("keyed to the old name would read every rename as a rewrite -- the same false positive")
	t.Log("PostgreSQL's probe avoids by looking a table up by oid rather than by name.")
	t.Log("directly -- the relfilenode oracle one engine over. The assertion runs one way only:")
	t.Log("whatever rewrote must have been declared table_rewrite. The converse is deliberately")
	t.Log("false and the over-reports are logged by name, because a retype is declared a rewrite even")
	t.Log("where the cast is free (ADR 0016), and MariaDB adds a parenthesised default instantly")
	t.Log("where MySQL rewrites. Separating brief from long is a duration under load: M13-T12's.")
}

func TestTheAlgorithmTheServerAcceptsIsTheOneTheVerdictAssumes(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		dropLater(t, c, probeTable(t), probeTable(t)+"x", parentTable(t))

		caps, instant := c.Capabilities(), 0

		for name, p := range probes(t) {
			if !alterable(p.op) || assessedLock(t, c, caps, p) != capability.BriefExclusive {
				continue
			}

			switch measureBrief(t, c, p) {
			case algorithmInstant:
				instant++
			case algorithmInPlace:
				t.Logf("in place without rebuilding: %s", name)
			default:
				t.Errorf("%s is declared brief_exclusive and the server neither does it instantly "+
					"nor does it in place without rebuilding", name)
			}
		}

		if instant == 0 {
			t.Error("no probe was proved metadata-only, so this test proved nothing")
		}

		checkDearProbesAreNotReportedInstant(t, c, caps)
	})

	t.Log("The instrument PostgreSQL cannot offer: told which algorithm to use, MySQL answers 1845 or")
	t.Log("1846 when it cannot, so INSTANT being accepted is the server itself saying the work is")
	t.Log("metadata-only. That is what brief_exclusive claims, and it is the half the rewrite oracle")
	t.Log("cannot reach -- add_index is INPLACE and rebuilds nothing and still reads every row, which")
	t.Log("is why in-place acceptance is logged rather than asserted and only INSTANT counts as proof.")
	t.Log("ALGORITHM is asked for here and never compiled into a statement migr sends: that would")
	t.Log("make migr refuse DDL a server would have performed, and is M13-T2's call to make.")
	t.Log("")
	t.Log("The control is what makes the instrument mean anything, and the sweep is what found it")
	t.Log("missing: every operation here accepts INPLACE, so asking for INPLACE instead of INSTANT")
	t.Log("reported all of them metadata-only and nothing failed. Each rewriting operation is now")
	t.Log("put through the same instrument and must come back dearer -- which is SQLite's suite")
	t.Log("pairing a rewrite of every row against an update of one, in the vocabulary this engine has.")
}

// checkDearProbesAreNotReportedInstant holds the instrument to telling a rewrite from a metadata change.
func checkDearProbesAreNotReportedInstant(t *testing.T, c *mysqlConn, caps capability.Set) {
	t.Helper()

	controls := 0

	for name, p := range probes(t) {
		if !alterable(p.op) || assessedLock(t, c, caps, p) != capability.TableRewrite {
			continue
		}

		controls++

		if got := measureBrief(t, c, p); got != algorithmDearer {
			t.Errorf("%s rewrites the whole table and the instrument reports it %q", name, got)
		}
	}

	if controls == 0 {
		t.Error("no rewriting probe was measured, so the instrument was never shown to discriminate")
	}
}

// What the server answered about one operation, cheapest first.
const (
	algorithmInstant = "instant"
	algorithmInPlace = "in place"
	algorithmDearer  = "dearer"
)

// measureBrief reports how cheaply the server will perform an operation declared metadata-only.
func measureBrief(t *testing.T, c *mysqlConn, p probe) string {
	t.Helper()

	if err := attempt(t, c, p, "INSTANT"); err == nil {
		return algorithmInstant
	}

	if err := attempt(t, c, p, "INPLACE"); err != nil {
		return algorithmDearer
	}

	if rebuilt(t, c, p) {
		return algorithmDearer
	}

	return algorithmInPlace
}

// alterable reports an operation this driver compiles to an ALTER TABLE, which is what carries the clause.
func alterable(op ir.Operation) bool {
	stmts, err := compile(op)
	if err != nil || len(stmts) != 1 {
		return false
	}

	return strings.HasPrefix(stmts[0].SQL, "ALTER TABLE ")
}

// attempt runs one probe's operation with an algorithm demanded of the server, and reports its answer.
func attempt(t *testing.T, c *mysqlConn, p probe, algorithm string) error {
	t.Helper()

	seed(t, c)

	for _, before := range p.setup {
		apply(t, c, before)
	}

	stmts, err := compile(p.op)
	if err != nil {
		t.Fatalf("compiling %s: %v", p.op.Kind(), err)
	}

	sql := strings.TrimSuffix(stmts[0].SQL, terminator) + ", ALGORITHM=" + algorithm

	return c.Exec(t.Context(), driver.Statement{SQL: sql})
}

func TestAnOperationDeclaredARewriteIsOneTheServerWillNotDoInPlace(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		dropLater(t, c, probeTable(t), probeTable(t)+"x", parentTable(t))

		caps := c.Capabilities()

		for name, p := range probes(t) {
			if !alterable(p.op) || assessedLock(t, c, caps, p) != capability.TableRewrite {
				continue
			}

			err := attempt(t, c, p, "INPLACE")
			if err == nil {
				continue
			}

			if !hasNumber(err, errAlgorithmUnsupported) && !hasNumber(err, errAlgorithmReason) {
				t.Errorf("%s failed for a reason that is not the algorithm: %v", name, err)
			}
		}
	})

	t.Log("The other direction, and it is weaker on purpose: an INPLACE the server accepts may still")
	t.Log("rebuild the table -- add_primary_key is exactly that, measured -- so acceptance disproves")
	t.Log("nothing and only the failures are read. What this does catch is a rewrite verdict whose")
	t.Log("statement fails for some reason other than the algorithm, which would mean the probe")
	t.Log("never measured the operation it names.")
}

func TestTheTwoServersAgreeAboutDroppingAColumnOnlyFromTheReleaseTheyBothHave(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		dropLater(t, c, probeTable(t), probeTable(t)+"x", parentTable(t))

		p := probe{op: ir.DropColumn{Table: probeTable(t), Column: "n"}}

		if got := assessedLock(t, c, c.Capabilities(), p); got != capability.BriefExclusive {
			t.Errorf("drop_column is assessed %s on %s", got, c.version)
		}

		if rebuilt(t, c, p) {
			t.Error("dropping a column rewrote the table, and both servers do it instantly")
		}
	})

	t.Log("Both containers drop instantly, so what this pins is the live half of a verdict whose")
	t.Log("other half no container can reach: MySQL gained the instant drop at 8.0.29 and MariaDB at")
	t.Log("10.4.3, below this driver's floor, so between 8.0 and 8.0.28 the two variants answer")
	t.Log("differently for one operation. That span is tested against constructed versions in")
	t.Log("impact_test.go, which is ADR 0052's reading of invariant #13.")
}

func TestAUUIDDefaultIsRefusedByTheServerExactlyWhereTheDeclarationSaysSo(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t) + "_u"
		dropLater(t, c, table)
		drop(t.Context(), c, table)

		apply(t, c, ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
		}, PrimaryKey: []string{"id"}})

		op := ir.AddColumn{Table: table, Column: ir.Column{Name: "token",
			Type: liveType(t, "uuid"), Nullable: true, Default: ir.Call(ir.FuncUUID)}}

		allowed := c.Capabilities().Resolve(op).Allowed()

		stmts, err := compile(op)
		if err != nil {
			t.Fatalf("compiling %s: %v", op.Kind(), err)
		}

		accepted := c.Exec(t.Context(), stmts[0]) == nil

		if accepted != allowed {
			t.Errorf("the declaration allows this column %v and the server accepts it %v",
				allowed, accepted)
		}

		logged := read(t, c, binaryLogQuery)[0] == variableOn
		if refusable := logged && c.version.variant == MySQL; allowed == refusable {
			t.Errorf("the server reports log_bin %v on %s and the declaration allows the column %v",
				logged, c.version, allowed)
		}
	})

	t.Log("The two halves of one fact, measured against both servers rather than asserted: the")
	t.Log("MySQL image writes a binary log and refuses the statement with error 1674, and the")
	t.Log("MariaDB image does not write one and accepts it. Started with --log-bin, MariaDB still")
	t.Log("accepts it -- checked against a throwaway container -- which is why the refusal is keyed")
	t.Log("to the variant and the setting together and not to the setting alone.")
}
