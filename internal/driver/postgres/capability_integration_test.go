//go:build integration

package postgres

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// relfilenodeQuery reads a table's current storage, which a rewrite replaces.
//
// The table is looked up by oid rather than by name: a rename leaves the same storage under a new
// name, and a probe keyed by name reads the rename as a rewrite. That false positive is what this
// query's shape exists to avoid.
const relfilenodeQuery = `SELECT relfilenode FROM pg_class WHERE oid = $1::regclass`

// probeTable is the populated table every rewrite probe alters, so a rewrite has rows to copy.
const probeTable = "migr_impact_probe"

// probeRows is enough rows that a rewrite is a real one rather than a metadata shuffle.
const probeRows = 2000

// rewriteProbe is one operation, run against a populated table and watched for a rewrite.
type rewriteProbe struct {
	op       ir.Operation
	rewrites bool
}

// impactProbes returns operations covering every lock verdict this driver declares.
func impactProbes(t *testing.T) []rewriteProbe {
	t.Helper()

	return []rewriteProbe{
		{op: ir.AddColumn{Table: probeTable, Column: ir.Column{
			Name: "plain", Type: probeType(t, "int32"), Nullable: true,
		}}},
		{op: ir.AddColumn{Table: probeTable, Column: ir.Column{
			Name: "literal", Type: probeType(t, "int32"), Default: ir.Literal("5"),
		}}},
		{op: ir.AddColumn{Table: probeTable, Column: ir.Column{
			Name: "stamped", Type: probeType(t, "timestamptz"),
			Default: ir.Call(ir.FuncCurrentTimestamp),
		}}},
		{op: ir.AddColumn{Table: probeTable, Column: ir.Column{
			Name: "key", Type: probeType(t, "uuid"), Default: ir.Call(ir.FuncUUID),
		}}, rewrites: true},
		{op: ir.AlterColumn{
			Table: probeTable, Column: "n", Type: probePointer(probeType(t, "int64")),
		}, rewrites: true},
		{op: ir.AlterColumn{
			Table: probeTable, Column: "email", Type: probePointer(probeType(t, "string(512)")),
		}},
		{op: ir.AlterColumn{Table: probeTable, Column: "email", Nullable: probePointer(false)}},
		{op: ir.AlterColumn{Table: probeTable, Column: "email", Default: ir.Literal("none")}},
		{op: ir.AddIndex{Table: probeTable, Name: "ix_probe", Columns: []string{"email"}}},
		{op: ir.DropIndex{Table: probeTable, Name: "ix_probe"}},
		{op: ir.AddCheck{Table: probeTable, Name: "ck_probe", Expression: "n >= 0"}},
		{op: ir.AddUnique{Table: probeTable, Name: "uq_probe", Columns: []string{"id"}}},
		{op: ir.AddPrimaryKey{Table: probeTable, Name: "pk_probe", Columns: []string{"id"}}},
		{op: ir.DropPrimaryKey{Table: probeTable, Name: "pk_probe"}},
		{op: ir.DropColumn{Table: probeTable, Column: "plain"}},
		{op: ir.RenameColumn{Table: probeTable, From: "literal", To: "renamed"}},
		{op: ir.RenameTable{From: probeTable, To: probeTable + "_moved"}},
	}
}

func TestNoTableRewriteHappensThatWasNotDeclared(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			observed := runImpactProbes(t, dsnFor(server.env, server.dsn))

			if observed == 0 {
				t.Error("no probe rewrote anything, so this test proved nothing")
				t.Log("Two probes must rewrite: a volatile default, and a retype that is not")
				t.Log("binary coercible. If neither did, the harness is watching the wrong thing.")
			}
		})
	}

	t.Log("A rewrite replaces the table's relfilenode, so it is the one axis a server settles")
	t.Log("directly. The assertion runs one way only: whatever rewrote must have been declared")
	t.Log("table_rewrite. The converse is deliberately false -- a retype is always declared a")
	t.Log("rewrite even when the cast is binary coercible, because the operation carries what an")
	t.Log("author recorded the old type as rather than what the column is (invariant #13).")
	t.Log("The brief/long distinction is a duration under load and is M13-T12's, not this one's.")
}

// runImpactProbes runs each probe against a populated table and returns how many rewrote it.
func runImpactProbes(t *testing.T, dsn string) int {
	t.Helper()

	session := openSession(t, dsn)
	beginRolledBack(t, session)
	createProbeTable(t, session)

	d := newDriver(t, driver.Options{})
	caps := session.Capabilities()
	rewrites := 0

	for _, probe := range impactProbes(t) {
		if checkProbe(t, session, d, caps, probe) {
			rewrites++
		}
	}

	return rewrites
}

// checkProbe runs one operation and reports whether it replaced the table's storage.
func checkProbe(t *testing.T, session *pgConn, d driver.Driver, caps capability.Set,
	probe rewriteProbe,
) bool {
	t.Helper()

	was, becomes := probeNames(probe.op)
	before := relfilenode(t, session, was)
	declared := caps.Resolve(probe.op).Lock()

	execCompiled(t, session, d, probe.op)

	rewrote := relfilenode(t, session, becomes) != before

	if rewrote && declared != capability.TableRewrite {
		t.Errorf("%s rewrote the table but is declared %q", describeProbe(probe.op), declared)
	}

	if rewrote != probe.rewrites {
		t.Errorf("%s rewrote=%v, and this test expected %v -- PostgreSQL's behavior changed, so "+
			"the declaration needs rereading", describeProbe(probe.op), rewrote, probe.rewrites)
	}

	if !rewrote && declared == capability.TableRewrite {
		t.Logf("over-reported (deliberate): %s is declared %q and did not rewrite",
			describeProbe(probe.op), declared)
	}

	return rewrote
}

// describeProbe names an operation and the column or object it touches, for a failure message.
func describeProbe(op ir.Operation) string {
	switch o := op.(type) {
	case ir.AddColumn:
		return string(op.Kind()) + " " + o.Column.Name
	case ir.AlterColumn:
		return string(op.Kind()) + " " + o.Column
	default:
		return string(op.Kind())
	}
}

// probeNames returns the table a probe reads before and after, which only a rename changes between.
func probeNames(op ir.Operation) (was, becomes string) {
	if rename, renaming := op.(ir.RenameTable); renaming {
		return rename.From, rename.To
	}

	return probeTable, probeTable
}

// relfilenode reads a table's storage identity, which changes only when the table is rewritten.
func relfilenode(t *testing.T, session *pgConn, table string) uint32 {
	t.Helper()

	var node uint32
	if err := session.conn.QueryRow(t.Context(), relfilenodeQuery, table).Scan(&node); err != nil {
		t.Fatalf("reading the relfilenode of %s: %v", table, err)
	}

	return node
}

// createProbeTable builds a populated table, so a rewrite has rows to copy rather than none.
func createProbeTable(t *testing.T, session *pgConn) {
	t.Helper()

	d := newDriver(t, driver.Options{})

	execCompiled(t, session, d, ir.CreateTable{
		Table: probeTable,
		Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
			{Name: "email", Type: probeType(t, "string(255)"), Nullable: true},
			{Name: "n", Type: probeType(t, "int32")},
		},
	})

	fill := ir.Raw{Driver: Name, SQL: "INSERT INTO " + quote(probeTable) +
		" SELECT g, 'e' || g, g FROM generate_series(1, " + strconv.Itoa(probeRows) + ") g"}

	execCompiled(t, session, d, fill)
}

func TestTransactionalDDLIsProvenRatherThanDocumented(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session := openSession(t, dsnFor(server.env, server.dsn))

			if !session.Capabilities().TransactionalDDL() {
				t.Fatal("this driver declares no transactional DDL")
			}

			beginRolledBack(t, session)
			createProbeTable(t, session)

			if !tableExists(t, session, probeTable) {
				t.Fatal("the table was not created inside the transaction")
			}
		})
	}

	t.Log("Each subtest creates the table inside a transaction the cleanup rolls back, and the")
	t.Log("next subtest's create succeeds against the same server -- which it could not if the")
	t.Log("rollback had left the table behind. That is the declaration proven rather than read")
	t.Log("out of documentation, which is what invariant #13 asks of every fact a driver states.")
}

func TestARolledBackCreateLeavesNothingBehind(t *testing.T) {
	session := openSession(t, dsnFor(servers[0].env, servers[0].dsn))

	tx, err := session.conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	createProbeTable(t, session)

	if err := tx.Rollback(t.Context()); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	if tableExists(t, session, probeTable) {
		t.Errorf("%s survived a rolled-back transaction", probeTable)
		t.Log("PostgreSQL is the reference driver because DDL is transactional here. MySQL")
		t.Log("declares false in M9-T3, and M4-T9 builds the dirty-state model that needs.")
	}
}

// tableExists reports whether a table is visible on this session right now.
func tableExists(t *testing.T, session *pgConn, table string) bool {
	t.Helper()

	var exists bool

	err := session.conn.QueryRow(t.Context(),
		`SELECT EXISTS (SELECT 1 FROM pg_class WHERE relname = $1 AND relkind = 'r')`,
		table).Scan(&exists)
	if err != nil {
		t.Fatalf("looking for %s: %v", table, err)
	}

	return exists
}

func TestTheUUIDDefaultFunctionIsAskedAboutRatherThanAssumed(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session := openSession(t, dsnFor(server.env, server.dsn))

			present, err := describeFunction(t.Context(), session.conn, uuidFunction)
			if err != nil {
				t.Fatalf("describeFunction(%s): %v", uuidFunction, err)
			}

			if !present.exists || !present.volatile {
				t.Errorf("%s reported exists=%v volatile=%v", uuidFunction, present.exists,
					present.volatile)
			}

			stable, err := describeFunction(t.Context(), session.conn, "now()")
			if err != nil {
				t.Fatalf("describeFunction(now()): %v", err)
			}

			if !stable.exists || stable.volatile {
				t.Errorf("now() reported exists=%v volatile=%v, want a stable function that exists",
					stable.exists, stable.volatile)
				t.Log("now() is what CURRENT_TIMESTAMP becomes and PostgreSQL marks it stable, so")
				t.Log("this is the case that proves provolatile is read rather than assumed. Without")
				t.Log("it, a lookup that always answered volatile would look correct: the only other")
				t.Log("function asked about is one.")
			}

			absent, err := describeFunction(t.Context(), session.conn, "migr_no_such_function()")
			if err != nil {
				t.Fatalf("describeFunction on a missing function: %v", err)
			}

			if absent.exists {
				t.Error("a function this server does not have was reported as present")
			}
		})
	}

	t.Log("Three cases, because the headline result alone proves nothing: on 16 and 17 a")
	t.Log("hard-coded version test gives the same answer as asking. The missing-function case")
	t.Log("shows the query is a lookup, and the stable-function case shows the volatility is")
	t.Log("read -- which is why a PostgreSQL 12 carrying pgcrypto is accepted where AtLeast(13)")
	t.Log("would refuse a server that works.")
}

func TestASchemaTheServerDoesNotHaveIsRefusedAtOpen(t *testing.T) {
	for _, schema := range []string{"pubic", "public, pg_catalog"} {
		t.Run(schema, func(t *testing.T) {
			d := newDriver(t, driver.Options{Schema: schema})

			_, err := d.Open(t.Context(), dsnFor(servers[0].env, servers[0].dsn))
			if err == nil {
				t.Fatal("a schema this server does not have was accepted")
			}

			if !errors.Is(err, ErrNoSchema) {
				t.Errorf("Open failed with %v, want ErrNoSchema", err)
			}

			if !strings.Contains(err.Error(), schema) {
				t.Errorf("the failure %q does not name the schema that is missing", err)
			}
		})
	}

	t.Log("PostgreSQL drops an unknown entry from search_path rather than reporting it, so the")
	t.Log("session opens cleanly and the first CREATE TABLE fails with \"no schema has been")
	t.Log("selected to create in\" -- naming neither the schema nor the config key. Verified.")
	t.Log("The second case is the injection M3-T1 defended with quote_ident, which now never")
	t.Log("reaches search_path: the value names no schema. The quoting still matters, because a")
	t.Log("schema can be created with a comma in its name -- see the M3-T1 test, which does.")
}

func TestAnExistingSchemaStillOpens(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{Schema: "public"})

	if conn.Capabilities().Driver() != Name {
		t.Errorf("the session declares capabilities for %q", conn.Capabilities().Driver())
	}
}
