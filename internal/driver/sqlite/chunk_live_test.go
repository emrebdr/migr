package sqlite

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// manyRows stages a keyed table of n rows whose nicknames are NULL and whose emails are mixed case.
func manyRows(t *testing.T, c *sqliteConn, n int) {
	t.Helper()

	apply(t, c, ir.CreateTable{Table: "people", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "nickname", Type: mustType(t, "string(64)"), Nullable: true},
		{Name: "email_key", Type: mustType(t, "string(255)"), Nullable: true},
	}, PrimaryKey: []string{"id"}})
	apply(t, c, ir.CreateTable{Table: "archive", Columns: []ir.Column{
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
	}, PrimaryKey: []string{"id"}})
	apply(t, c, ir.Raw{Driver: Name, SQL: `WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i + 1 ` +
		`FROM g WHERE i < ` + strconv.Itoa(n) + `) INSERT INTO "people" ("id", "email") ` +
		`SELECT i, 'User' || i || '@Example.COM' FROM g`})
}

// chunkAll runs an operation a chunk at a time until a chunk says it was the last, each in its own transaction.
func chunkAll(t *testing.T, c *sqliteConn, op ir.Operation, key []string, rows int) []driver.Sliced {
	t.Helper()

	var (
		done  []driver.Sliced
		after []string
	)

	for range 1000 {
		var sliced driver.Sliced

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			out, err := c.Chunk(ctx, driver.Slice{Op: op, Key: key, After: after, Rows: rows})
			sliced = out

			return err
		})
		if err != nil {
			t.Fatalf("chunk %d: %v", len(done)+1, err)
		}

		done = append(done, sliced)
		after = sliced.Last

		if sliced.Done {
			return done
		}
	}

	t.Fatal("the chunks never reached the end of the table")

	return nil
}

// count reads one integer a query over the staged tables answers.
func count(t *testing.T, c *sqliteConn, query string) int {
	t.Helper()

	n, err := strconv.Atoi(*read(t, c, query).Values[0][0])
	if err != nil {
		t.Fatalf("reading %q: %v", query, err)
	}

	return n
}

func TestEveryChunkSizeCoversEveryRowExactlyOnce(t *testing.T) {
	for _, size := range []int{1, 3, 10, 25, 26, 100} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			c := openFile(t)
			manyRows(t, c, 25)

			copied := chunkAll(t, c, ir.DataCopy{From: "people", To: "archive", Columns: []ir.CopyColumn{
				{From: "id", To: "id"}, {From: "email", To: "email"},
			}}, []string{"id"}, size)

			if got := count(t, c, `SELECT count(*) FROM "archive"`); got != 25 {
				t.Errorf("the chunked copy left %d rows in the archive, want 25", got)
				t.Log("A copy cannot be told from the rows already there, so a chunk that re-covered")
				t.Log("its cursor row would fail on the key, and one that skipped a row would lose it.")
			}

			var written int64
			for _, s := range copied {
				written += s.Written
			}

			if written != 25 {
				t.Errorf("the chunks report writing %d rows, want 25", written)
			}

			if want := 25/size + 1; len(copied) != want {
				t.Errorf("chunks of %d took %d passes over 25 rows, want %d", size, len(copied), want)
				t.Log("Each chunk covers exactly its size until fewer rows than that remain, and one")
				t.Log("more chunk finds nothing beyond its cursor -- which is the one that says done.")
			}
		})
	}
}

func TestAChunkedBackfillFillsOnlyTheNullsItsRangeHolds(t *testing.T) {
	c := openFile(t)
	manyRows(t, c, 30)
	apply(t, c, ir.Raw{Driver: Name, SQL: `UPDATE "people" SET "nickname" = 'kept' WHERE "id" % 3 = 0`})

	done := chunkAll(t, c, ir.DataBackfill{Table: "people", Column: "nickname",
		Type: mustType(t, "string(64)"), Value: ir.Literal("anonymous")}, []string{"id"}, 7)

	if got := count(t, c, `SELECT count(*) FROM "people" WHERE "nickname" = 'kept'`); got != 10 {
		t.Errorf("%d rows still hold the value set before the backfill, want 10", got)
	}

	if got := count(t, c, `SELECT count(*) FROM "people" WHERE "nickname" IS NULL`); got != 0 {
		t.Errorf("%d NULLs survived the chunked backfill", got)
	}

	if done[0].Written != 5 || !slices.Equal(done[0].Last, []string{"7"}) {
		t.Errorf("the first chunk wrote %d rows through %v, want the 5 NULLs among keys 1 to 7",
			done[0].Written, done[0].Last)
	}
}

func TestAChunkedTransformRewritesEveryRowOfACompositeKey(t *testing.T) {
	c := openFile(t)
	apply(t, c, ir.CreateTable{Table: "members", Columns: []ir.Column{
		{Name: "tenant", Type: mustType(t, "string(16)")},
		{Name: "id", Type: mustType(t, "int64")},
		{Name: "email", Type: mustType(t, "string(255)")},
		{Name: "email_key", Type: mustType(t, "string(255)"), Nullable: true},
	}, PrimaryKey: []string{"tenant", "id"}})
	apply(t, c, ir.Raw{Driver: Name, SQL: `WITH RECURSIVE g(i) AS (SELECT 1 UNION ALL SELECT i + 1 ` +
		`FROM g WHERE i < 40) INSERT INTO "members" ("tenant", "id", "email") ` +
		`SELECT char(97 + i % 4), i, 'X' || i FROM g`})

	chunkAll(t, c, ir.DataTransform{Table: "members", Column: "email_key",
		Type: mustType(t, "string(255)"), From: "email", Function: ir.TransformLower},
		[]string{"tenant", "id"}, 6)

	if got := count(t, c, `SELECT count(*) FROM "members" WHERE "email_key" IS NULL OR `+
		`"email_key" <> lower("email")`); got != 0 {
		t.Errorf("%d rows were not rewritten through a composite key's chunks", got)
		t.Log("The row value (tenant, id) > (?, ?) is what pages a composite key; comparing either")
		t.Log("column alone would skip every row of a tenant after the first.")
	}
}

func TestATextKeyPagesInTheOrderItsCollationSorts(t *testing.T) {
	c := openFile(t)
	apply(t, c, ir.CreateTable{Table: "codes", Columns: []ir.Column{
		{Name: "code", Type: mustType(t, "string(16)")},
		{Name: "label", Type: mustType(t, "string(16)"), Nullable: true},
	}, PrimaryKey: []string{"code"}})
	apply(t, c, ir.Raw{Driver: Name, SQL: `INSERT INTO "codes" ("code") VALUES ('10'), ('9'), ` +
		`('a'), ('B'), ('ab')`})

	done := chunkAll(t, c, ir.DataBackfill{Table: "codes", Column: "label",
		Type: mustType(t, "string(16)"), Value: ir.Literal("x")}, []string{"code"}, 2)

	if got := count(t, c, `SELECT count(*) FROM "codes" WHERE "label" IS NULL`); got != 0 {
		t.Errorf("%d text keys were skipped: the chunks ended %v", got, done)
	}
}

func TestAChunkRunsOnlyInsideTheTransactionItsCheckpointIsWrittenIn(t *testing.T) {
	c := openFile(t)
	manyRows(t, c, 5)

	_, err := c.Chunk(t.Context(), driver.Slice{Op: ir.DataBackfill{Table: "people",
		Column: "nickname", Type: mustType(t, "string(64)"), Value: ir.Literal("x")},
		Key: []string{"id"}, Rows: 2})
	if !errors.Is(err, ErrTransaction) {
		t.Errorf("a chunk outside any transaction ran with %v", err)
	}
}

func TestAMisspelledKeyFailsTheChunkRatherThanReadingAString(t *testing.T) {
	c := openFile(t)
	manyRows(t, c, 5)

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		_, err := c.Chunk(ctx, driver.Slice{Op: ir.DataBackfill{Table: "people", Column: "nickname",
			Type: mustType(t, "string(64)"), Value: ir.Literal("x")}, Key: []string{"idd"}, Rows: 2})

		return err
	})
	if err == nil || !strings.Contains(err.Error(), "idd") {
		t.Errorf("a chunk keyed by a column that does not exist ran with %v", err)
		t.Log("Unqualified, \"idd\" is the string 'idd' on this engine, and every row compares")
		t.Log("greater than nothing -- a chunk over that would page by a constant.")
	}
}

func TestACheckpointSurvivesTheRoundTripAndIsClearedByItsOwnKey(t *testing.T) {
	_, held := ensured(t)

	if cps, err := held.Checkpoints(t.Context()); err != nil || len(cps) != 0 {
		t.Fatalf("a fresh checkpoint table holds %v, %v", cps, err)
	}

	want := history.Checkpoint{Version: "20260727T153000", Direction: ir.Up, Step: 1,
		Cursor: []string{"tenant, \"a\"", "42"}, Chunks: 7, Written: 6999}
	other := history.Checkpoint{Version: "20260727T153000", Direction: ir.Down, Step: 0,
		Cursor: []string{"1"}, Chunks: 1, Written: 1}

	for _, cp := range []history.Checkpoint{want, other, want} {
		if err := held.SaveCheckpoint(t.Context(), cp); err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}
	}

	cps, err := held.Checkpoints(t.Context())
	if err != nil {
		t.Fatalf("Checkpoints: %v", err)
	}

	if len(cps) != 2 || !slices.Equal(cps[1].Cursor, want.Cursor) || cps[1].Written != 6999 {
		t.Fatalf("the checkpoints read back as %+v, want the two saved, the first replaced", cps)
	}

	if err := held.ClearCheckpoint(t.Context(), want.Version, ir.Up); err != nil {
		t.Fatalf("ClearCheckpoint: %v", err)
	}

	cps, _ = held.Checkpoints(t.Context())
	if len(cps) != 1 || cps[0].Direction != ir.Down {
		t.Errorf("clearing the up checkpoint left %+v, want the down one alone", cps)
	}
}

func TestReadingCheckpointsCreatesNothingOnADatabaseWithoutTheTable(t *testing.T) {
	_, held := store(t)

	if cps, err := held.Checkpoints(t.Context()); err != nil || cps != nil {
		t.Fatalf("a database with no checkpoint table answered %v, %v", cps, err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.progressTable()); found {
		t.Error("reading the checkpoints created the table, which a report never may")
	}
}

func TestEnsureAddsTheCheckpointTableToAHistoryThatPredatesIt(t *testing.T) {
	c, held := ensured(t)
	apply(t, c, ir.DropTable{Table: held.progressTable()})

	if err := held.Ensure(t.Context()); err != nil {
		t.Fatalf("Ensure over a history with no checkpoint table: %v", err)
	}

	if _, found, _ := held.kindOf(t.Context(), held.progressTable()); !found {
		t.Error("Ensure did not create the checkpoint table beside a history that already existed")
		t.Log("Every database migrated before this build has a history and no checkpoint table, so")
		t.Log("creating it only alongside a new history would leave every one of them unable to chunk.")
	}
}
