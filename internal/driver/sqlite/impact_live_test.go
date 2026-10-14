package sqlite

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// The two table sizes every operation is measured at, ten rows apart so scaling is unmistakable.
const (
	smallTable = 50
	largeTable = 500
)

// scales is the factor above which work counts as proportional to the table rather than constant.
const scales = 2.0

// rewrites is the factor above which a statement counts as having rewritten rows already there,
// set clear of drop_index's 1.5 and well below the 6.1 a real rewrite produces.
const rewrites = 3.0

// pageSize is SQLite's default page, which turns journal bytes into a count of pages.
const pageSize = 4096

// work is what one statement cost the database file, counted in pages rather than in seconds.
type work struct {
	// rewritten is pages the statement modified in place, read off the rollback journal.
	rewritten int64
	// allocated is pages the statement added to the file.
	allocated int64
	// freed is pages the statement released onto the freelist.
	freed int64
}

// touched is every page the statement disturbed, however it disturbed it.
func (w work) touched() int64 {
	return w.rewritten + w.allocated + w.freed
}

// growth reports how much more of something the larger table cost, as a factor.
func growth(small, large int64) float64 {
	return float64(large) / float64(max(small, 1))
}

// measured reports what an operation actually costs, which is what invariant #13 asks for.
func measured(t *testing.T, op ir.Operation, setup []ir.Operation) capability.LockImpact {
	t.Helper()

	small, large := cost(t, smallTable, op, setup), cost(t, largeTable, op, setup)

	if growth(small.rewritten, large.rewritten) >= rewrites {
		return capability.TableRewrite
	}

	if growth(small.touched(), large.touched()) >= scales {
		return capability.LongExclusive
	}

	return capability.BriefExclusive
}

// cost runs one operation against a table of n rows and counts the pages it disturbed.
func cost(t *testing.T, n int, op ir.Operation, setup []ir.Operation) work {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.db")
	c := open(t, path, driver.Options{})

	fill(t, c, n)

	for _, before := range setup {
		apply(t, c, before)
	}

	pagesBefore, freedBefore := counters(t, c)

	return work{
		rewritten: journalled(t, c, path, op) / pageSize,
		allocated: pageCount(t, c) - pagesBefore,
		freed:     freeCount(t, c) - freedBefore,
	}
}

// fill builds the table every measurement runs against, with a column wide enough to span pages.
func fill(t *testing.T, c *sqliteConn, n int) {
	t.Helper()

	apply(t, c, ir.CreateTable{Table: "users", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "payload", Type: mustType(t, "text")},
	}, PrimaryKey: []string{"id"}})

	for i := range n {
		if _, err := c.Perform(t.Context(), driver.Query{
			SQL: fmt.Sprintf(`INSERT INTO "users" VALUES (%d, 'e%d@example.com', '%s')`,
				i, i, padding(i)),
		}); err != nil {
			t.Fatalf("seeding row %d: %v", i, err)
		}
	}
}

// padding returns a value wide enough that a few hundred rows span many pages.
func padding(i int) string {
	out := make([]byte, 0, 160)
	for range 20 {
		out = append(out, fmt.Sprintf("%08d", i)...)
	}

	return string(out)
}

// journalled runs one operation inside a transaction and returns the rollback journal it produced.
func journalled(t *testing.T, c *sqliteConn, path string, op ir.Operation) int64 {
	t.Helper()

	stmts, err := compile(op)
	if err != nil {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	var peak int64

	err = c.Transact(t.Context(), func(ctx context.Context) error {
		for _, stmt := range stmts {
			if err := c.Exec(ctx, stmt); err != nil {
				return err
			}
		}

		if info, err := os.Stat(path + "-journal"); err == nil {
			peak = info.Size()
		}

		return nil
	})
	if err != nil {
		t.Fatalf("running %s: %v", op.Kind(), err)
	}

	return peak
}

// counters reads both page counters at once, since every measurement needs the pair.
func counters(t *testing.T, c *sqliteConn) (int64, int64) {
	t.Helper()

	return pageCount(t, c), freeCount(t, c)
}

// pageCount reports how many pages the database file holds.
func pageCount(t *testing.T, c *sqliteConn) int64 {
	t.Helper()

	return number(t, c, `PRAGMA page_count`)
}

// freeCount reports how many of those pages are on the freelist.
func freeCount(t *testing.T, c *sqliteConn) int64 {
	t.Helper()

	return number(t, c, `PRAGMA freelist_count`)
}

// number reads one integer back through the read path, failing the test rather than the driver.
func number(t *testing.T, c *sqliteConn, sql string) int64 {
	t.Helper()

	n, err := strconv.ParseInt(value(t, read(t, c, sql)), 10, 64)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}

	return n
}

// nickname is a nullable column every row of the seeded table holds NULL in, for a write to fill.
func nickname(t *testing.T) ir.Operation {
	t.Helper()

	return ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname",
		Type: mustType(t, "string(64)"), Nullable: true}}
}

// measurable lists what can be run against the seeded table, with whatever each one needs first.
func measurable(t *testing.T) map[ir.Kind]struct {
	op    ir.Operation
	setup []ir.Operation
} {
	t.Helper()

	index := ir.AddIndex{Table: "users", Name: "idx_users_email", Columns: []string{"email"}}
	unique := ir.AddUnique{Table: "users", Name: "uq_users_email", Columns: []string{"email"}}
	view := ir.CreateView{View: "listing", Query: `SELECT "id" FROM "users"`}

	return map[ir.Kind]struct {
		op    ir.Operation
		setup []ir.Operation
	}{
		ir.KindCreateTable: {op: ir.CreateTable{Table: "other", Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")},
		}}},
		ir.KindCreateView:   {op: view},
		ir.KindDropView:     {op: ir.DropView{View: "listing"}, setup: []ir.Operation{view}},
		ir.KindAddColumn:    {op: ir.AddColumn{Table: "users", Column: ir.Column{Name: "nickname", Type: mustType(t, "string(64)"), Nullable: true}}},
		ir.KindRenameTable:  {op: ir.RenameTable{From: "users", To: "accounts"}},
		ir.KindRenameColumn: {op: ir.RenameColumn{Table: "users", From: "email", To: "email_address"}},
		ir.KindDropTable:    {op: ir.DropTable{Table: "users"}},
		ir.KindAddIndex:     {op: index},
		ir.KindDropIndex: {op: ir.DropIndex{Table: "users", Name: "idx_users_email"},
			setup: []ir.Operation{index}},
		ir.KindAddUnique: {op: unique},
		ir.KindDropUnique: {op: ir.DropUnique{Table: "users", Name: "uq_users_email"},
			setup: []ir.Operation{unique}},
		ir.KindDropColumn: {op: ir.DropColumn{Table: "users", Column: "payload"}},
		ir.KindDataBackfill: {op: ir.DataBackfill{Table: "users", Column: "nickname",
			Type: mustType(t, "string(64)"), Value: ir.Literal("anonymous")},
			setup: []ir.Operation{nickname(t)}},
		ir.KindDataTransform: {op: ir.DataTransform{Table: "users", Column: "nickname",
			Type: mustType(t, "string(64)"), From: "email", Function: ir.TransformUpper},
			setup: []ir.Operation{nickname(t)}},
		ir.KindDataCopy: {op: ir.DataCopy{From: "users", To: "archive", Columns: []ir.CopyColumn{
			{From: "id", To: "id"}, {From: "payload", To: "payload"},
		}}, setup: []ir.Operation{ir.CreateTable{Table: "archive", Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")},
			{Name: "payload", Type: mustType(t, "text")},
		}, PrimaryKey: []string{"id"}}}},
	}
}

func TestEveryLockVerdictIsWhatTheOperationCostTheDatabase(t *testing.T) {
	for kind, c := range measurable(t) {
		t.Run(kind.String(), func(t *testing.T) {
			if got := measured(t, c.op, c.setup); got != declared[kind] {
				t.Errorf("%s is declared %s and cost %s", kind, declared[kind], got)
			}
		})
	}

	t.Log("Invariant #13 says lock impact is measured, not assumed, and this is the one driver")
	t.Log("where that can be mechanical: SQLite is in-process, so a live database is a t.TempDir()")
	t.Log("and the file itself says what a statement did. Two instruments, neither a clock: the")
	t.Log("rollback journal counts pages rewritten in place, and the page and freelist counters")
	t.Log("count pages allocated and released. Separating brief from long by *time* needs load,")
	t.Log("and that is M13-T12's zerodowntime suite -- this measures the work, not the wait.")
}

func TestTheMeasurementSeparatesARewriteFromAReadOfTheSameSize(t *testing.T) {
	rewriteAll := ir.Raw{Driver: Name, SQL: `UPDATE "users" SET "email" = 'x@example.com'`}
	oneRow := ir.Raw{Driver: Name, SQL: `UPDATE "users" SET "email" = 'x@example.com' WHERE "id" = 1`}

	if got := measured(t, rewriteAll, nil); got != capability.TableRewrite {
		t.Errorf("a statement that rewrites every row measured %s", got)
	}

	if got := measured(t, oneRow, nil); got != capability.BriefExclusive {
		t.Errorf("a statement that touches one row measured %s", got)
	}

	t.Log("The two controls are what make the thresholds mean something rather than being tuned")
	t.Log("to pass: an UPDATE over every row is the definition of a rewrite, and one over a single")
	t.Log("row is the definition of constant work. Every verdict above is measured with the same")
	t.Log("instrument that puts these two at opposite ends.")
}

func TestEveryOperationThisDriverCompilesIsMeasured(t *testing.T) {
	measurements := measurable(t)

	for _, kind := range ir.Kinds() {
		_, measured := measurements[kind]

		if measured == (needsRebuild[kind] || kind == ir.KindRaw) {
			t.Errorf("%s is %s and the measurement %s it", kind,
				refusalOrSupport(needsRebuild[kind]), inclusion(measured))
		}
	}

	t.Log("raw is the one compiled operation with no verdict to measure: it is LockUnknown")
	t.Log("permanently, because nothing can know what an operator's own SQL does. The seven the")
	t.Log("rebuild owns have nothing to run yet, and M8-T4 is what adds them here.")
}
