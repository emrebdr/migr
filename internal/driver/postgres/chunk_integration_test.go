//go:build integration

package postgres

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

// chunkedPeople stages a keyed table of n rows with NULL nicknames, and an empty archive beside it.
func chunkedPeople(t *testing.T, session *pgConn, n int) {
	t.Helper()

	apply(t, session, ir.Operations{
		ir.CreateTable{Table: "people", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
			{Name: "email", Type: probeType(t, "string(255)")},
			{Name: "nickname", Type: probeType(t, "string(64)"), Nullable: true},
			{Name: "email_key", Type: probeType(t, "string(255)"), Nullable: true},
		}, PrimaryKey: []string{"id"}},
		ir.CreateTable{Table: "archive", Columns: []ir.Column{
			{Name: "id", Type: probeType(t, "int64")},
			{Name: "email", Type: probeType(t, "string(255)")},
		}, PrimaryKey: []string{"id"}},
		ir.Raw{Driver: Name, SQL: `INSERT INTO "people" ("id", "email") SELECT g, 'User' || g || ` +
			`'@Example.COM' FROM generate_series(1, ` + strconv.Itoa(n) + `) g`},
	})
}

// chunkThrough runs an operation a chunk at a time until a chunk says it was the last.
func chunkThrough(t *testing.T, session *pgConn, op ir.Operation, key []string, rows int) int {
	t.Helper()

	var after []string

	for chunks := 1; chunks <= 1000; chunks++ {
		var sliced driver.Sliced

		err := session.Transact(t.Context(), func(ctx context.Context) error {
			out, err := session.Chunk(ctx, driver.Slice{Op: op, Key: key, After: after, Rows: rows})
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

// counted reads the one integer a query over the staged tables answers.
func counted(t *testing.T, session *pgConn, query string) int {
	t.Helper()

	rows, err := session.Query(t.Context(), driver.Query{SQL: query})
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}

	n, err := strconv.Atoi(*rows.Values[0][0])
	if err != nil {
		t.Fatalf("%s returned %q: %v", query, *rows.Values[0][0], err)
	}

	return n
}

func TestChunkedDataOperationsWriteEveryRowOnBothMajors(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		chunkedPeople(t, session, 40)
		apply(t, session, ir.Operations{ir.Raw{Driver: Name,
			SQL: `UPDATE "people" SET "nickname" = 'kept' WHERE "id" % 4 = 0`}})

		key := []string{"id"}

		chunks := []int{
			chunkThrough(t, session, ir.DataBackfill{Table: "people", Column: "nickname",
				Type: probeType(t, "string(64)"), Value: ir.Literal("anonymous")}, key, 7),
			chunkThrough(t, session, ir.DataTransform{Table: "people", Column: "email_key",
				Type: probeType(t, "string(255)"), From: "email", Function: ir.TransformLower}, key, 9),
			chunkThrough(t, session, ir.DataCopy{From: "people", To: "archive",
				Columns: []ir.CopyColumn{{From: "id", To: "id"}, {From: "email", To: "email"}}}, key, 40),
		}

		if !slices.Equal(chunks, []int{6, 5, 2}) {
			t.Errorf("the three operations took %v chunks over 40 rows, want 6, 5 and 2", chunks)
			t.Log("Each chunk covers exactly its size until fewer rows than that remain; a chunk")
			t.Log("ordered by the key's text would cover keys 1 through 18 on its first pass of 7.")
		}

		for query, want := range map[string]int{
			`SELECT count(*) FROM "people" WHERE "nickname" = 'kept'`:                       10,
			`SELECT count(*) FROM "people" WHERE "nickname" IS NULL`:                        0,
			`SELECT count(*) FROM "people" WHERE "email_key" IS DISTINCT FROM lower(email)`: 0,
			`SELECT count(*) FROM "archive"`:                                                40,
		} {
			if got := counted(t, session, query); got != want {
				t.Errorf("%s answered %d, want %d", query, got, want)
			}
		}
	})
}

func TestAUUIDKeyAndACompositeKeyPageAsTheirOwnTypes(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		apply(t, session, ir.Operations{
			ir.CreateTable{Table: "tokens", Columns: []ir.Column{
				{Name: "id", Type: probeType(t, "uuid")},
				{Name: "label", Type: probeType(t, "string(16)"), Nullable: true},
			}, PrimaryKey: []string{"id"}},
			ir.CreateTable{Table: "members", Columns: []ir.Column{
				{Name: "tenant", Type: probeType(t, "string(16)")},
				{Name: "id", Type: probeType(t, "int32")},
				{Name: "label", Type: probeType(t, "string(16)"), Nullable: true},
			}, PrimaryKey: []string{"tenant", "id"}},
			ir.Raw{Driver: Name, SQL: `INSERT INTO "tokens" ("id") SELECT gen_random_uuid() ` +
				`FROM generate_series(1, 30)`},
			ir.Raw{Driver: Name, SQL: `INSERT INTO "members" ("tenant", "id") SELECT chr(97 + g % 3), ` +
				`g FROM generate_series(1, 30) g`},
		})

		for _, table := range []struct {
			name string
			key  []string
		}{{"tokens", []string{"id"}}, {"members", []string{"tenant", "id"}}} {
			chunkThrough(t, session, ir.DataBackfill{Table: table.name, Column: "label",
				Type: probeType(t, "string(16)"), Value: ir.Literal("x")}, table.key, 4)

			query := `SELECT count(*) FROM "` + table.name + `" WHERE "label" IS NULL`
			if got := counted(t, session, query); got != 0 {
				t.Errorf("%d rows of %s were skipped by its chunks", got, table.name)
				t.Log("A cursor is bound as text and takes the key column's type from the comparison,")
				t.Log("so a uuid compares as a uuid and the row value orders a composite key whole.")
			}
		}
	})
}

func TestTheCheckpointTableIsCreatedBesideTheHistoryAndKeptOutOfTheSnapshot(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		store := session.History().(history.Checkpointer)

		if cps, err := store.Checkpoints(t.Context()); err != nil || cps != nil {
			t.Fatalf("a database with no checkpoint table answered %v, %v", cps, err)
		}

		if err := session.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		if err := session.Seeds().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure the seeds: %v", err)
		}

		want := history.Checkpoint{Version: "20260727T153000", Direction: ir.Up, Step: 2,
			Cursor: []string{"a", "17"}, Chunks: 3, Written: 2999}

		err := session.Transact(t.Context(), func(ctx context.Context) error {
			return store.SaveCheckpoint(ctx, want)
		})
		if err != nil {
			t.Fatalf("SaveCheckpoint: %v", err)
		}

		cps, err := store.Checkpoints(t.Context())
		if err != nil || len(cps) != 1 || !slices.Equal(cps[0].Cursor, want.Cursor) ||
			cps[0].Step != 2 || cps[0].Written != 2999 {
			t.Errorf("the checkpoint read back as %+v, %v, want %+v", cps, err, want)
		}

		snapshot := introspect(t, session)
		for _, name := range []string{session.history.table, session.seeds.table,
			history.ProgressTable(session.history.table), history.ProgressTable(session.seeds.table)} {
			if _, held := snapshot.Table(name); held {
				t.Errorf("the snapshot holds %q, which every migr diff would then propose dropping",
					name)
			}
		}
	})
}

func TestAStoppedChunkedMigrationResumesThroughTheEngineWithEveryRowCopiedOnce(t *testing.T) {
	eachMajor(t, func(t *testing.T, session *pgConn) {
		chunkedPeople(t, session, 60)

		opts := queueOptions(t, staged{version: "20260727T153000", name: "archive_people",
			ops: ir.Operations{ir.DataCopy{From: "people", To: "archive", Columns: []ir.CopyColumn{
				{From: "id", To: "id"}, {From: "email", To: "email"}}}}})
		opts.Planner.ChunkRows, opts.Chunker, opts.Pacer = 10, session, engine.Steady{Rows: 10}

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

		if _, err := engine.Apply(ctx, session, opts); !errors.Is(err, engine.ErrDirty) {
			t.Fatalf("the canceled run reported %v, want the dirty state it left", err)
		}

		if got := counted(t, session, `SELECT count(*) FROM "archive"`); got != 30 {
			t.Errorf("the canceled run left %d rows copied, want the 3 chunks of 10 that committed",
				got)
			t.Log("PostgreSQL resolves an unqualified ORDER BY against the output columns first, and")
			t.Log("the bound's output column is the key ::text -- ordering 1, 10, 11 ... 18 rather than")
			t.Log("1 through 10. That one is caught here: the chunk was the right range at the wrong size.")
		}

		opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

		result, err := engine.Apply(t.Context(), session, opts)
		if err != nil {
			t.Log("The archive's primary key is what a duplicate would fail on, so a resume that")
			t.Log("re-ran a committed chunk would surface here as a unique violation.")
			t.Fatalf("resuming: %v", err)
		}

		if got := counted(t, session, `SELECT count(*) FROM "archive"`); got != 60 ||
			!result.Runs[0].Resumed {
			t.Errorf("after the resume the archive holds %d rows and the run reports resumed %v, "+
				"want 60 and true", got, result.Runs[0].Resumed)
		}

		cps, _ := session.History().(history.Checkpointer).Checkpoints(t.Context())
		if len(cps) != 0 {
			t.Errorf("the checkpoint survived the finished run: %+v", cps)
		}
	})
}
