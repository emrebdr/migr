//go:build integration

package mysql

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// manyPeople stages a keyed table of n rows with NULL nicknames, and an empty archive beside it.
func manyPeople(t *testing.T, c *mysqlConn, people, archive string, n int) {
	t.Helper()

	dropped(t, c, people, archive)
	drop(t.Context(), c, people, archive)

	applied(t, c, ir.Operations{
		ir.CreateTable{Table: people, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
			{Name: "email", Type: liveType(t, "string(255)")},
			{Name: "nickname", Type: liveType(t, "string(64)"), Nullable: true},
			{Name: "email_key", Type: liveType(t, "string(255)"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
		ir.CreateTable{Table: archive, Columns: []ir.Column{
			{Name: "id", Type: liveType(t, "int64")},
			{Name: "email", Type: liveType(t, "string(255)")},
		}, PrimaryKey: []string{"id"}},
	})

	for i := 1; i <= n; i++ {
		raw(t, c, "INSERT INTO "+quote(people)+" (`id`, `email`) VALUES ("+strconv.Itoa(i)+
			", 'User"+strconv.Itoa(i)+"@Example.COM')")
	}
}

// chunkEvery runs an operation a chunk at a time until a chunk says it was the last.
func chunkEvery(t *testing.T, c *mysqlConn, op ir.Operation, key []string, rows int) int {
	t.Helper()

	var after []string

	for chunks := 1; chunks <= 1000; chunks++ {
		var sliced driver.Sliced

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			out, err := c.Chunk(ctx, driver.Slice{Op: op, Key: key, After: after, Rows: rows})
			sliced = out

			return err
		})
		if err != nil {
			t.Fatalf("chunk %d: %v", chunks, err)
		}

		if sliced.Done {
			return chunks
		}

		after = sliced.Last
	}

	t.Fatal("the chunks never reached the end of the table")

	return 0
}

// countOf reads the one integer a query answers.
func countOf(t *testing.T, c *mysqlConn, query string) int {
	t.Helper()

	n, err := strconv.Atoi(read(t, c, query)[0])
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}

	return n
}

func TestChunkedDataOperationsWriteEveryRowOnBothServers(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		people, archive := owned(t, "people"), owned(t, "archive")
		manyPeople(t, c, people, archive, 40)
		raw(t, c, "UPDATE "+quote(people)+" SET `nickname` = 'kept' WHERE `id` % 4 = 0")

		key := []string{"id"}
		chunks := []int{
			chunkEvery(t, c, ir.DataBackfill{Table: people, Column: "nickname",
				Type: liveType(t, "string(64)"), Value: ir.Literal("anonymous")}, key, 7),
			chunkEvery(t, c, ir.DataTransform{Table: people, Column: "email_key",
				Type: liveType(t, "string(255)"), From: "email", Function: ir.TransformLower}, key, 9),
			chunkEvery(t, c, ir.DataCopy{From: people, To: archive, Columns: []ir.CopyColumn{
				{From: "id", To: "id"}, {From: "email", To: "email"}}}, key, 40),
		}

		if !slices.Equal(chunks, []int{6, 5, 2}) {
			t.Errorf("the three operations took %v chunks over 40 rows, want 6, 5 and 2", chunks)
		}

		for query, want := range map[string]int{
			"SELECT count(*) FROM " + quote(people) + " WHERE `nickname` = 'kept'":                  10,
			"SELECT count(*) FROM " + quote(people) + " WHERE `nickname` IS NULL":                   0,
			"SELECT count(*) FROM " + quote(people) + " WHERE NOT (`email_key` <=> LOWER(`email`))": 0,
			"SELECT count(*) FROM " + quote(archive):                                                40,
		} {
			if got := countOf(t, c, query); got != want {
				t.Errorf("%s answered %d, want %d", query, got, want)
			}
		}
	})
}

func TestAChunkOverACompositeKeyLocksItsOwnRangeAndNotTheTable(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		members := owned(t, "members")
		dropped(t, c, members)
		drop(t.Context(), c, members)

		applied(t, c, ir.Operations{ir.CreateTable{Table: members, Columns: []ir.Column{
			{Name: "tenant", Type: liveType(t, "int32")},
			{Name: "id", Type: liveType(t, "int32")},
			{Name: "label", Type: liveType(t, "string(16)"), Nullable: true},
		}, PrimaryKey: []string{"tenant", "id"}}})

		for tenant := 1; tenant <= 20; tenant++ {
			for id := 1; id <= 10; id++ {
				raw(t, c, "INSERT INTO "+quote(members)+" (`tenant`, `id`) VALUES ("+
					strconv.Itoa(tenant)+", "+strconv.Itoa(id)+")")
			}
		}

		other := openServer(t, s, driver.Options{HistoryTable: uniqueName(t)})
		raw(t, other, "SET SESSION innodb_lock_wait_timeout = 1")

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			if _, err := c.Chunk(ctx, driver.Slice{Op: ir.DataBackfill{Table: members,
				Column: "label", Type: liveType(t, "string(16)"), Value: ir.Literal("x")},
				Key: []string{"tenant", "id"}, After: []string{"10", "5"}, Rows: 10}); err != nil {
				return err
			}

			return other.Exec(ctx, driver.Statement{SQL: "UPDATE " + quote(members) +
				" SET `label` = 'elsewhere' WHERE `tenant` = 2 AND `id` = 3"})
		})
		if err != nil {
			t.Errorf("a write far outside the chunk's range waited on the chunk: %v", err)
			t.Log("Both servers answer (tenant, id) > (?, ?) by scanning the key from its start, and")
			t.Log("an UPDATE locks every row it scans -- measured, a chunk written that way locked the")
			t.Log("whole table. The cursor is spelled tenant >= ? AND (tenant > ? OR id > ?) for that.")
		}

		if got := countOf(t, c, "SELECT count(*) FROM "+quote(members)+" WHERE `label` = 'x'"); got != 10 {
			t.Errorf("the chunk after (10, 5) labeled %d rows, want the 10 it covers", got)
		}
	})
}

func TestTheCheckpointTableIsCreatedBesideTheHistoryOnBothServers(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		held := c.History().(mysqlHistory)
		dropLater(t, c, held.table, held.progressTable(), held.releaseTable())
		drop(t.Context(), c, held.table, held.progressTable(), held.releaseTable())

		if cps, err := held.Checkpoints(t.Context()); err != nil || cps != nil {
			t.Fatalf("a database with no checkpoint table answered %v, %v", cps, err)
		}

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		want := history.Checkpoint{Version: "20260727T153000", Direction: ir.Up, Step: 1,
			Cursor: []string{"3", "17"}, Chunks: 3, Written: 2999}

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			return held.SaveCheckpoint(ctx, want)
		})
		if err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}

		cps, err := held.Checkpoints(t.Context())
		if err != nil || len(cps) != 1 || !slices.Equal(cps[0].Cursor, want.Cursor) {
			t.Errorf("the checkpoint read back as %+v, %v, want %+v", cps, err, want)
		}
	})
}

func TestAStoppedChunkedMigrationResumesAgainstARealServerWithEveryRowCopiedOnce(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		people, archive := owned(t, "people"), owned(t, "archive")
		manyPeople(t, c, people, archive, 60)

		held := c.History().(mysqlHistory)
		dropLater(t, c, held.table, held.progressTable(), held.releaseTable())
		drop(t.Context(), c, held.table, held.progressTable(), held.releaseTable())

		opts := staged(t, ir.Operations{ir.DataCopy{From: people, To: archive,
			Columns: []ir.CopyColumn{{From: "id", To: "id"}, {From: "email", To: "email"}}}})
		opts.Planner.ChunkRows, opts.Chunker, opts.Pacer = 10, c, engine.Steady{Rows: 10}

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		waited := 0
		opts.Sleep = func(ctx context.Context, _ time.Duration) error {
			waited++
			if waited == 4 {
				cancel()
			}

			return ctx.Err()
		}

		if _, err := engine.Apply(ctx, c, opts); !errors.Is(err, engine.ErrDirty) {
			t.Fatalf("the canceled run reported %v, want the dirty state it left", err)
		}

		if got := countOf(t, c, "SELECT count(*) FROM "+quote(archive)); got != 30 {
			t.Errorf("the canceled run left %d rows copied, want the 3 chunks of 10 that committed", got)
		}

		opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

		result, err := engine.Apply(t.Context(), c, opts)
		if err != nil {
			t.Fatalf("resuming: %v", err)
		}

		if got := countOf(t, c, "SELECT count(*) FROM "+quote(archive)); got != 60 ||
			!result.Runs[0].Resumed {
			t.Errorf("after the resume the archive holds %d rows and the run reports resumed %v, "+
				"want 60 and true", got, result.Runs[0].Resumed)
		}
	})
}
