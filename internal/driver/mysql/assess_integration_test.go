//go:build integration

package mysql

import (
	"fmt"
	"testing"

	lib "github.com/go-sql-driver/mysql"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// unprivilegedUser is test/initdb/unprivileged-user.sql's: its own database and no PROCESS.
const unprivilegedUser = "migr_plain"

// shape is one way a table can be built, and whether each server can still change it instantly.
type shape struct {
	// options follow the table definition, such as a row format.
	options string
	// extra runs after the rows are in, such as building an index.
	extra []string
	// rowVersions is how many instant changes to spend before the probe runs.
	rowVersions int
	// instant reports, per variant, whether the server changes this shape without a rebuild.
	instant map[Variant]bool
}

// shapes are the table states measured on 8.0.46 and 11.4.13 for an added and a dropped column.
var shapes = map[string]shape{
	"a plain table": {instant: map[Variant]bool{MySQL: true, MariaDB: true}},
	"a COMPRESSED table": {options: "ROW_FORMAT=COMPRESSED",
		instant: map[Variant]bool{MySQL: false, MariaDB: false}},
	"a table with a FULLTEXT index": {extra: []string{"ALTER TABLE %s ADD FULLTEXT INDEX ft (body)"},
		instant: map[Variant]bool{MySQL: false, MariaDB: false}},
	"a table at the row-version ceiling": {rowVersions: maxRowVersions,
		instant: map[Variant]bool{MySQL: false, MariaDB: true}},
}

// build creates a populated table in one shape, spending the instant row versions it asks for.
func build(t *testing.T, c *mysqlConn, table string, sh shape) {
	t.Helper()

	drop(t.Context(), c, table)
	exec(t, c, fmt.Sprintf("CREATE TABLE %s (`id` bigint PRIMARY KEY, `n` int, `body` text, "+
		"`email` varchar(100)) %s", quote(table), sh.options))
	fill(t, c, table, "INSERT INTO "+quote(table)+" (`id`, `n`, `body`, `email`) "+
		"SELECT `i`, `i`, concat('b', `i`), concat('e', `i`) FROM "+numbers(c))

	for _, sql := range sh.extra {
		exec(t, c, fmt.Sprintf(sql, quote(table)))
	}

	for i := range sh.rowVersions / 2 {
		exec(t, c, fmt.Sprintf("ALTER TABLE %s ADD COLUMN `v%d` int NULL", quote(table), i))
		exec(t, c, fmt.Sprintf("ALTER TABLE %s DROP COLUMN `v%d`", quote(table), i))
	}
}

// assess resolves an operation on this session and asks the session to confirm it from the table.
func assess(t *testing.T, c driver.Conn, op ir.Operation) capability.Verdict {
	t.Helper()

	assessor, ok := c.(driver.Assessor)
	if !ok {
		t.Fatalf("%T is no driver.Assessor", c)
	}

	v, err := assessor.Assess(t.Context(), op, c.Capabilities().Resolve(op))
	if err != nil {
		t.Fatalf("Assess(%s): %v", op.Kind(), err)
	}

	return v
}

// changedInstantly runs an operation and reports whether InnoDB kept the table it ran on.
func changedInstantly(t *testing.T, c *mysqlConn, table string, op ir.Operation) bool {
	t.Helper()

	was := tableID(t, c, table)
	apply(t, c, op)

	return tableID(t, c, table) == was
}

func TestAssessLowersAColumnChangeExactlyWhereTheServerMakesItInstant(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t)
		dropLater(t, c, table)

		ops := map[string]ir.Operation{
			"add_column": ir.AddColumn{Table: table, Column: ir.Column{Name: "added",
				Type: liveType(t, "int32"), Nullable: true}},
			"drop_column": ir.DropColumn{Table: table, Column: "email"},
		}

		for name, sh := range shapes {
			for kind, op := range ops {
				build(t, c, table, sh)

				lowered := assess(t, c, op).Lock() == capability.BriefExclusive
				instant := changedInstantly(t, c, table, op)

				if instant != sh.instant[s.variant] {
					t.Fatalf("%s on %s: the server changed it instantly = %v, and the measurement "+
						"this test encodes says %v", kind, name, instant, sh.instant[s.variant])
				}

				if lowered != instant {
					t.Errorf("%s on %s: Assess lowered it = %v, and the server changed it "+
						"instantly = %v", kind, name, lowered, instant)
				}
			}
		}
	})

	t.Log("The table id is the oracle M9-T3 built: InnoDB gives a rebuilt table a new one. Assess")
	t.Log("lowering a verdict is a claim the server then keeps, and keeping it pessimistic is a claim")
	t.Log("the server then rebuilds -- so both directions are held, not only the safe one.")
}

func TestDroppingAnIndexedColumnRebuildsOnMySQLAndNotOnMariaDB(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := uniqueName(t)
		dropLater(t, c, table)

		build(t, c, table, shape{extra: []string{"ALTER TABLE %s ADD INDEX ie (email)"}})

		op := ir.DropColumn{Table: table, Column: "email"}
		lowered := assess(t, c, op).Lock() == capability.BriefExclusive
		instant := changedInstantly(t, c, table, op)

		if want := s.variant == MariaDB; instant != want || lowered != want {
			t.Errorf("Assess lowered it = %v and the table was kept = %v, want both %v",
				lowered, instant, want)
		}
	})

	t.Log("MySQL refuses ALGORITHM=INSTANT here (1845) and rebuilds; MariaDB refuses it too (1846)")
	t.Log("and drops the column and its index in place, keeping the table -- which is the shape")
	t.Log("drop_unique already has on both servers, and it is declared brief_exclusive.")
}

func TestATableNotYetCreatedIsAssessedAtItsWorst(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		op := ir.AddColumn{Table: uniqueName(t) + "_absent", Column: ir.Column{Name: "added",
			Type: liveType(t, "int32"), Nullable: true}}

		if v := assess(t, c, op); v.Lock() != capability.TableRewrite || v.Condition() == "" {
			t.Errorf("Assess of a table nothing created = %s, want the unconfirmed rewrite", v)
		}
	})

	t.Log("A migration queued after the one creating its table is planned before that table exists,")
	t.Log("so there is nothing to read and the verdict stays the one no table can make worse.")
}

func TestWithoutPROCESSOnlyTheRowVersionCountGoesUnread(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			owner := openServer(t, s, driver.Options{HistoryTable: uniqueName(t)})
			plain := openUnprivileged(t, s)

			table := uniqueName(t)
			dropLater(t, owner, table)
			build(t, owner, table, shape{})

			op := ir.AddColumn{Table: table, Column: ir.Column{Name: "added",
				Type: liveType(t, "int32"), Nullable: true}}

			lowered := assess(t, plain, op).Lock() == capability.BriefExclusive
			if want := s.variant == MariaDB; lowered != want {
				t.Errorf("Assess without PROCESS lowered it = %v, want %v", lowered, want)
			}

			if assess(t, owner, op).Lock() != capability.BriefExclusive {
				t.Error("Assess with PROCESS kept a plain table's add at its rewrite cost")
			}
		})
	}

	t.Log("information_schema answers the row format and the indexes to any user, and refuses")
	t.Log("INNODB_TABLES with 1227 without PROCESS -- measured. MySQL from 8.0.29 needs that table")
	t.Log("for the row-version count, so there an add stays a rewrite; MariaDB keeps no count and")
	t.Log("is lowered either way. migr never needs the grant: lacking it costs a flag, not a refusal.")
}

// openUnprivileged connects as the user that holds what a migr deployment holds and nothing more.
func openUnprivileged(t *testing.T, s server) driver.Conn {
	t.Helper()

	config, err := lib.ParseDSN(s.dsnFor())
	if err != nil {
		t.Fatalf("ParseDSN: %v", err)
	}

	config.User, config.Passwd = unprivilegedUser, unprivilegedUser

	d, err := New(driver.Options{HistoryTable: uniqueName(t)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), config.FormatDSN())
	if err != nil {
		t.Fatalf("Open as %s: %v\n\nRecreate the containers with `docker compose -f "+
			"test/docker-compose.yml down -v`, since an init script runs on a fresh volume only.",
			unprivilegedUser, err)
	}

	t.Cleanup(func() {
		if err := conn.Close(t.Context()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return conn
}
