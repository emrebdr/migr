//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

// servers are the PostgreSQL majors this driver is held to, and the environment variables
// that override the DSNs test/docker-compose.yml provides.
var servers = []struct {
	name  string
	env   string
	dsn   string
	major int
}{
	{
		name:  "postgres16",
		env:   "MIGR_TEST_PG16_DSN",
		dsn:   "postgres://migr:migr@127.0.0.1:55416/migr?sslmode=disable",
		major: 16,
	},
	{
		name:  "postgres17",
		env:   "MIGR_TEST_PG17_DSN",
		dsn:   "postgres://migr:migr@127.0.0.1:55417/migr?sslmode=disable",
		major: 17,
	},
}

// dsnFor returns the connection string for a server, preferring the environment override.
func dsnFor(env, fallback string) string {
	if override := os.Getenv(env); override != "" {
		return override
	}

	return fallback
}

// open connects to a server, failing rather than skipping when it is not there.
func open(t *testing.T, dsn string, opts driver.Options) driver.Conn {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-integration`, which brings the servers up first.", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return conn
}

func TestConnectingToEveryServerUnderTest(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			conn := open(t, dsnFor(server.env, server.dsn), driver.Options{})

			reported := conn.ServerVersion()
			if !strings.HasPrefix(reported, strconv.Itoa(server.major)+".") {
				t.Errorf("ServerVersion() = %q, want a %d.x release", reported, server.major)
			}

			if conn.Capabilities().Server() != reported {
				t.Errorf("Capabilities().Server() = %q but ServerVersion() = %q",
					conn.Capabilities().Server(), reported)
				t.Log("Both report the same fact, so they are derived from one detection inside")
				t.Log("Open. Two detections could disagree, and a lock verdict keyed to the wrong")
				t.Log("one is exactly the outage invariant #13 exists to prevent.")
			}

			if conn.Capabilities().Driver() != Name {
				t.Errorf("the session declares capabilities for %q", conn.Capabilities().Driver())
			}

			checkHistory(t, conn)
		})
	}
}

// checkHistory holds a session's history store to what it must be before M3-T6 writes one.
func checkHistory(t *testing.T, conn driver.Conn) {
	t.Helper()

	store := conn.History()
	if store == nil {
		t.Error("the session handed back no history store")
		t.Log("History returns no error, so nil is a value no caller can check before it")
		t.Log("panics. A store that refuses is the only honest answer before M3-T6.")

		return
	}

	if err := store.Ensure(t.Context()); err == nil {
		t.Error("the history store created a table before M3-T6 wrote one")
	}
}

func TestAnUnreachableServerFailsClosed(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = d.Open(t.Context(), "postgres://migr:migr@127.0.0.1:1/migr?sslmode=disable")
	if err == nil {
		t.Fatal("opening a session against a closed port succeeded")
	}

	if !errors.Is(err, ErrConnect) {
		t.Errorf("Open failed with %v, want ErrConnect", err)
	}

	if strings.Contains(err.Error(), "migr:migr") {
		t.Errorf("the failure rendered the credentials: %s", err)
	}
}

// searchPath reads back what a session's schema option actually set.
func searchPath(t *testing.T, schema string) string {
	t.Helper()

	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{Schema: schema})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	var current string
	if err := session.conn.QueryRow(t.Context(), "SHOW search_path").Scan(&current); err != nil {
		t.Fatalf("reading search_path: %v", err)
	}

	return current
}

func TestAnOrdinarySchemaIsSetPlainly(t *testing.T) {
	if got := searchPath(t, "public"); got != "public" {
		t.Errorf("search_path = %q, want %q", got, "public")
		t.Log("quote_ident leaves a legitimate identifier unquoted, so the ordinary case is")
		t.Log("unchanged by the escaping the case below needs.")
	}
}

func TestTheSearchPathCannotBeEscaped(t *testing.T) {
	const injection = "public, pg_catalog"

	createSchema(t, injection)

	current := searchPath(t, injection)

	if !strings.Contains(current, `"`+injection+`"`) {
		t.Errorf("search_path is %q, want the schema kept whole as one quoted identifier", current)
	}

	t.Log("A schema may legitimately be created with a comma in its name, so the existence check")
	t.Log("M3-T3 added does not make quote_ident redundant -- it makes it the second line rather")
	t.Log("than the only one. Without the quoting this name would become two search path entries,")
	t.Log("one of them pg_catalog, which is a privilege surface rather than a typo.")
	t.Log("config never validates schema: not for identifier legality, not for length, not for")
	t.Log("commas. This test creates the schema first precisely because the injected value would")
	t.Log("otherwise be refused before reaching search_path at all.")
}

// createSchema makes a schema exist for the duration of one test, and drops it afterwards.
func createSchema(t *testing.T, name string) {
	t.Helper()

	session := openSession(t, dsnFor(servers[0].env, servers[0].dsn))

	if _, err := session.conn.Exec(t.Context(), "CREATE SCHEMA "+quote(name)); err != nil {
		t.Fatalf("creating schema %q: %v", name, err)
	}

	t.Cleanup(func() {
		ctx := context.WithoutCancel(t.Context())
		if _, err := session.conn.Exec(ctx, "DROP SCHEMA "+quote(name)); err != nil {
			t.Errorf("dropping schema %q: %v", name, err)
		}
	})
}

func TestTheConformanceSuitePasses(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			d, err := New(driver.Options{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			conn := open(t, dsnFor(server.env, server.dsn), driver.Options{})

			conformance.Suite{Driver: d, Conn: conn}.Run(t)
		})
	}

	t.Log("The same suite runs untagged against a session double, because rules() needs no")
	t.Log("connection. This run is the other half: it holds the declaration a live server")
	t.Log("actually produced to what that server's driver compiles.")
}

// script is an ordered set of operations that reach every form this driver compiles.
func script(t *testing.T) ir.Operations {
	t.Helper()

	return ir.Operations{
		ir.CreateTable{
			Table:      "migr_probe_users",
			Columns:    []ir.Column{{Name: "id", Type: probeType(t, "uuid")}},
			PrimaryKey: []string{"id"},
		},
		ir.CreateTable{
			Table:   "migr_probe_orders",
			Columns: everyMappableColumn(t),
		},
		ir.AddColumn{Table: "migr_probe_orders", Column: ir.Column{
			Name:     "note",
			Type:     probeType(t, "string(64)"),
			Nullable: true,
			Default:  ir.Literal("it's fine"),
		}},
		ir.AlterColumn{
			Table:    "migr_probe_orders",
			Column:   "note",
			Type:     probePointer(probeType(t, "string(128)")),
			Nullable: probePointer(false),
			Default:  ir.Literal("still fine"),
		},
		ir.AlterColumn{Table: "migr_probe_orders", Column: "note", DropDefault: true},
		ir.RenameColumn{Table: "migr_probe_orders", From: "note", To: "order"},
		ir.AddPrimaryKey{Table: "migr_probe_orders", Name: "pk_probe_orders", Columns: []string{"id"}},
		ir.AddUnique{Table: "migr_probe_orders", Name: "uq_probe_orders", Columns: []string{"id"}},
		ir.AddCheck{Table: "migr_probe_orders", Name: "ck_probe_orders", Expression: "total >= 0"},
		ir.AddIndex{Table: "migr_probe_orders", Name: "idx_probe_orders", Columns: []string{"order"}},
		ir.AddForeignKey{
			Table:             "migr_probe_orders",
			Name:              "fk_probe_orders",
			Columns:           []string{"user_id"},
			ReferencesTable:   "migr_probe_users",
			ReferencesColumns: []string{"id"},
			OnDelete:          ir.ActionCascade,
			OnUpdate:          ir.ActionRestrict,
		},
		ir.CreateView{View: "migr_probe_view", Query: "SELECT id FROM migr_probe_orders;"},
		ir.CreateView{
			View:      "migr_probe_view",
			Query:     "SELECT id FROM migr_probe_orders WHERE id > 0",
			OrReplace: true,
		},
		ir.Raw{Driver: Name, SQL: "COMMENT ON TABLE migr_probe_users IS 'written by a raw op'"},
		ir.DropView{View: "migr_probe_view"},
		ir.DropForeignKey{Table: "migr_probe_orders", Name: "fk_probe_orders"},
		ir.DropIndex{Table: "migr_probe_orders", Name: "idx_probe_orders"},
		ir.DropCheck{Table: "migr_probe_orders", Name: "ck_probe_orders"},
		ir.DropUnique{Table: "migr_probe_orders", Name: "uq_probe_orders"},
		ir.DropPrimaryKey{Table: "migr_probe_orders", Name: "pk_probe_orders"},
		ir.DropColumn{Table: "migr_probe_orders", Column: "order"},
		ir.RenameTable{From: "migr_probe_orders", To: "migr_probe_archive"},
		ir.DropTable{Table: "migr_probe_archive"},
		ir.DropTable{Table: "migr_probe_users"},
	}
}

// everyMappableColumn returns one column per logical type this driver claims to have a spelling for.
func everyMappableColumn(t *testing.T) []ir.Column {
	t.Helper()

	spellings := []string{
		"string(255)", "text", "bool", "int16", "int32", "int64", "float32", "float64",
		"decimal(10,2)", "uuid", "json", "timestamp", "timestamptz", "date", "time", "binary",
		"array(int32)", "array(string(16))",
	}

	columns := []ir.Column{
		{Name: "id", Type: probeType(t, "int64")},
		{Name: "user_id", Type: probeType(t, "uuid"), Nullable: true},
		{Name: "total", Type: probeType(t, "int32"), Nullable: true, Default: ir.Literal("0")},
		{Name: "key", Type: probeType(t, "uuid"), Default: ir.Call(ir.FuncUUID)},
		{Name: "seen", Type: probeType(t, "timestamptz"), Default: ir.Call(ir.FuncCurrentTimestamp)},
		{Name: "day", Type: probeType(t, "date"), Default: ir.Call(ir.FuncCurrentDate)},
		{Name: "hour", Type: probeType(t, "time"), Default: ir.Call(ir.FuncCurrentTime)},
	}

	for i, spelling := range spellings {
		columns = append(columns, ir.Column{
			Name:     fmt.Sprintf("mapped_%02d", i),
			Type:     probeType(t, spelling),
			Nullable: true,
		})
	}

	return columns
}

// probeType parses a canonical logical type for the script above.
func probeType(t *testing.T, spelling string) schema.Type {
	t.Helper()

	typ, err := schema.Parse(spelling)
	if err != nil {
		t.Fatalf("schema.Parse(%q): %v", spelling, err)
	}

	return typ
}

// probePointer returns a pointer to a value, for the alter_column fields that are tri-state.
func probePointer[T any](v T) *T {
	return &v
}

func TestPostgresAcceptsEverythingThisDriverCompiles(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			runScript(t, dsnFor(server.env, server.dsn))
		})
	}

	t.Log("The golden files record what this driver emits; only a server can say whether the")
	t.Log("SQL is right. Twenty syntactically plausible and wrong statements would pass every")
	t.Log("untagged test in this repository.")
	t.Log("This is not M3-T7: it asserts that PostgreSQL parses and accepts each statement, not")
	t.Log("that Up followed by Down restores an introspected snapshot.")
}

// openSession opens a connection and returns this package's own session, which tests reach into.
func openSession(t *testing.T, dsn string) *pgConn {
	t.Helper()

	conn := open(t, dsn, driver.Options{})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	return session
}

// beginRolledBack starts a transaction the cleanup rolls back, so a test leaves no schema behind.
func beginRolledBack(t *testing.T, session *pgConn) {
	t.Helper()

	tx, err := session.conn.Begin(t.Context())
	if err != nil {
		t.Fatalf("Begin: %v", err)
	}

	t.Cleanup(func() {
		if err := tx.Rollback(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Rollback: %v", err)
		}
	})
}

// runScript compiles and executes every operation in the script, then rolls the whole thing back.
func runScript(t *testing.T, dsn string) {
	t.Helper()

	session := openSession(t, dsn)
	beginRolledBack(t, session)

	d := newDriver(t, driver.Options{})

	for _, op := range script(t) {
		execCompiled(t, session, d, op)
	}
}

// execCompiled compiles one operation and runs each statement it becomes.
func execCompiled(t *testing.T, session *pgConn, d driver.Driver, op ir.Operation) {
	t.Helper()

	if err := op.Validate(); err != nil {
		t.Fatalf("%s: the script is not a well-formed operation: %v", op.Kind(), err)
	}

	stmts, err := d.Compile(op)
	if err != nil {
		t.Fatalf("%s: %v", op.Kind(), err)
	}

	for i, stmt := range stmts {
		if err := session.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("%s statement %d:\n  %s\n  %v", op.Kind(), i+1, stmt.SQL, err)
		}
	}
}

func TestTheSessionPinsTheStringGrammarTheCompilerQuotesFor(t *testing.T) {
	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	var setting string
	if err := session.conn.QueryRow(t.Context(),
		"SHOW standard_conforming_strings").Scan(&setting); err != nil {
		t.Fatalf("reading standard_conforming_strings: %v", err)
	}

	if setting != "on" {
		t.Errorf("standard_conforming_strings is %q", setting)
		t.Log("A column default is written into the statement rather than bound, so doubling '")
		t.Log("is the whole defense -- and with this off a backslash starts an escape sequence")
		t.Log("that can be escaped past. Compile is pure and cannot check a session setting.")
	}
}
