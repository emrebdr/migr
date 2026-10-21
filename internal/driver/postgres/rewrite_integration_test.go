//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// rewriteRows is how many rows the tables here hold, enough for a plain statement's scan to be seen.
const rewriteRows = 2_000_000

// writerWait is how long one probe write waits for a lock before it counts as blocked.
const writerWait = "50ms"

// writer inserts into a table in a loop and counts the writes that could not get their lock in time.
type writer struct {
	mu      sync.Mutex
	writes  int
	blocked int
	err     error
	stop    context.CancelFunc
	done    chan struct{}
}

// startWriter begins writing to a table as an application would, each write bounded by writerWait.
func startWriter(t *testing.T, dsn, schemaName string) *writer {
	t.Helper()

	conn, err := pgx.Connect(t.Context(), dsn)
	if err != nil {
		t.Fatalf("connecting the writer: %v", err)
	}

	if _, err := conn.Exec(t.Context(), "SET lock_timeout = '"+writerWait+"'"); err != nil {
		t.Fatalf("bounding the writer: %v", err)
	}

	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	w := &writer{stop: stop, done: make(chan struct{})}

	go w.loop(ctx, conn, "INSERT INTO "+quote(schemaName)+".people (id, v, parent) VALUES ($1, 1, 1)")

	t.Cleanup(func() {
		w.finish()
		_ = conn.Close(context.WithoutCancel(t.Context()))
	})

	return w
}

// loop writes until it is stopped, counting each write that gave up on its lock.
func (w *writer) loop(ctx context.Context, conn *pgx.Conn, insert string) {
	defer close(w.done)

	for id := rewriteRows + 1; ctx.Err() == nil; id++ {
		_, err := conn.Exec(ctx, insert, id)

		var pgErr *pgconn.PgError

		w.mu.Lock()
		switch {
		case err == nil:
			w.writes++
		case errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable:
			w.blocked++
		case ctx.Err() == nil:
			w.err = err
		}
		w.mu.Unlock()

		if w.err != nil {
			return
		}
	}
}

// finish stops the loop and reports how many writes landed and how many waited past their bound.
func (w *writer) finish() (int, int, error) {
	w.stop()
	<-w.done

	w.mu.Lock()
	defer w.mu.Unlock()

	return w.writes, w.blocked, w.err
}

// people fills a table the rewrites change, its rows numbered so a foreign key over them holds.
func people(t *testing.T, session *pgConn) {
	t.Helper()

	for _, sql := range []string{
		"CREATE TABLE people (id int PRIMARY KEY, v int, parent int)",
		fmt.Sprintf("INSERT INTO people SELECT i, i, i FROM generate_series(1, %d) i", rewriteRows),
		"ANALYZE people",
	} {
		if err := session.Exec(t.Context(), driver.Statement{SQL: sql}); err != nil {
			t.Fatalf("staging the table: %v", err)
		}
	}
}

// rewriteCase is one operation, run once as declared and once as the session rewrites it.
type rewriteCase struct {
	name string
	op   ir.Operation
}

// rewriteCases are the operations whose plain statement blocks writes for a scan.
func rewriteCases() []rewriteCase {
	return []rewriteCase{
		{name: "add_check", op: ir.AddCheck{Table: "people", Name: "people_v_hashed",
			Expression: "length(md5(v::text)) = 32"}},
		{name: "add_foreign_key", op: ir.AddForeignKey{Table: "people", Name: "people_parent",
			Columns: []string{"parent"}, ReferencesTable: "people", ReferencesColumns: []string{"id"}}},
		{name: "add_index", op: ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}}},
	}
}

// rewriteRun applies one operation over a filled table under a writer, and reports what the writer saw.
func rewriteRun(t *testing.T, at int, op ir.Operation, rewrites []driver.RewriteName) (int, int, *pgConn) {
	t.Helper()

	dsn := dsnFor(servers[at].env, servers[at].dsn)
	session, target := splitSession(t, at, 30*time.Second)
	people(t, session)

	w := startWriter(t, dsn, target)

	opts := queueOptions(t, staged{version: "20260727T153000", name: "make_safer", ops: ir.Operations{op}})
	opts.Planner.Rewriter, opts.Planner.Rewrites = session, rewrites

	if _, err := engine.Apply(t.Context(), session, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	writes, blocked, err := w.finish()
	if err != nil {
		t.Fatalf("the writer failed: %v", err)
	}

	return writes, blocked, session
}

func TestARewrittenOperationLetsWritesThroughWhereThePlainOneBlocksThem(t *testing.T) {
	for at, server := range servers {
		for _, c := range rewriteCases() {
			t.Run(server.name+"/"+c.name, func(t *testing.T) {
				var plainBlocked, writes, blocked int

				t.Run("plain", func(t *testing.T) { _, plainBlocked, _ = rewriteRun(t, at, c.op, nil) })
				t.Run("rewritten", func(t *testing.T) {
					writes, blocked, _ = rewriteRun(t, at, c.op, driver.RewriteNames())
				})

				if plainBlocked == 0 {
					t.Fatalf("the plain %s blocked no write, so this table is too small for the test "+
						"to tell a rewrite from no rewrite", c.name)
				}

				if blocked != 0 {
					t.Errorf("the rewritten %s left %d of %d writes waiting past %s, where the plain "+
						"one left %d", c.name, blocked, writes+blocked, writerWait, plainBlocked)
				}

				t.Logf("plain: %d writes blocked; rewritten: %d of %d", plainBlocked, blocked, writes+blocked)
			})
		}
	}

	t.Log("The control is the plain statement on the same table: it holds a lock that blocks writes")
	t.Log("for its whole scan, which the writer sees as lock timeouts. The rewritten sequence's only")
	t.Log("long statement -- VALIDATE, or the concurrent build -- runs bare under a lock writes pass.")
}

func TestARewriteLeavesTheSchemaThePlainOperationWouldHave(t *testing.T) {
	ops := append([]ir.Operation{}, ir.AlterColumn{Table: "people", Column: "v", Nullable: boolPointer(false),
		From: &ir.ColumnState{Nullable: boolPointer(true)}})
	for _, c := range rewriteCases() {
		ops = append(ops, c.op)
	}

	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			var snapshots [2]string

			for i, rewrites := range [][]driver.RewriteName{nil, driver.RewriteNames()} {
				t.Run([]string{"plain", "rewritten"}[i], func(t *testing.T) {
					session, _ := splitSession(t, at, 30*time.Second)
					people(t, session)

					opts := queueOptions(t, staged{version: "20260727T153000", name: "make_safer", ops: ops})
					opts.Planner.Rewriter, opts.Planner.Rewrites = session, rewrites

					result, err := engine.Apply(t.Context(), session, opts)
					if err != nil {
						t.Fatalf("Apply: %v", err)
					}

					if i == 1 && result.Runs[0].Rewritten != len(ops) {
						t.Errorf("%d of %d operations were rewritten", result.Runs[0].Rewritten, len(ops))
					}

					snapshot := introspect(t, session)
					snapshots[i] = schema.Render(*snapshot)
				})
			}

			if snapshots[0] != snapshots[1] {
				t.Errorf("the rewrites left a different schema:\n%s", schema.Difference(snapshots[0], snapshots[1]))
			}

			if strings.Contains(snapshots[1], notNullSuffix) {
				t.Errorf("the check that stood in for the scan is still there:\n%s", snapshots[1])
			}
		})
	}

	t.Log("A rewrite changes how an operation runs and never what it leaves: the snapshot after it")
	t.Log("is the plain run's, and the derived check behind SET NOT NULL is gone with it.")
}

func TestAValidateThatFailsLeavesTheRunDirtyAndResumesOnceTheRowsAreFixed(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)

			check := ir.AddCheck{Table: "people", Name: "people_v_small", Expression: "v < 100"}
			opts := queueOptions(t, staged{version: "20260727T153000", name: "check_people",
				ops: ir.Operations{check}})
			opts.Planner.Rewriter, opts.Planner.Rewrites = session, driver.RewriteNames()

			_, err := engine.Apply(t.Context(), session, opts)

			var dirty *engine.DirtyError
			if !errors.As(err, &dirty) || dirty.LastStatement != 1 {
				t.Fatalf("Apply returned %v, want the run dirty at statement 1", err)
			}

			if err := session.Exec(t.Context(), driver.Statement{SQL: "DELETE FROM people WHERE v >= 100"}); err != nil {
				t.Fatalf("fixing the rows: %v", err)
			}

			if _, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume}); err != nil {
				t.Fatalf("resuming: %v", err)
			}

			runs := recordedRuns(t, session)
			if last := runs[len(runs)-1]; last.Outcome != history.Applied {
				t.Errorf("after the resume the last row is %s, want applied", last.Outcome)
			}
		})
	}

	t.Log("NOT VALID constrains new rows at once and leaves the old ones to VALIDATE. When VALIDATE")
	t.Log("finds a violation the constraint stays NOT VALID and the run stops dirty after the ADD,")
	t.Log("so fixing the rows and resuming runs VALIDATE alone, which is the point of allowing it.")
}

func TestAConcurrentBuildStoppedPartwayNamesTheIndexItLeftInvalid(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)

			opts := queueOptions(t, staged{version: "20260727T153000", name: "index_people",
				ops: ir.Operations{ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}}}})
			opts.Planner.Rewriter, opts.Planner.Rewrites = session, driver.RewriteNames()
			opts.ConcurrentTimeout = 50 * time.Millisecond

			_, err := engine.Apply(t.Context(), session, opts)

			var invalid *InvalidIndexError
			if !errors.As(err, &invalid) || invalid.Index != "people_v" {
				t.Fatalf("Apply returned %v, want the INVALID index named", err)
			}

			if !strings.Contains(err.Error(), `DROP INDEX CONCURRENTLY "people_v"`) {
				t.Errorf("the failure does not say how to drop what it left: %v", err)
			}

			var stopped *engine.TimeoutError
			if !errors.As(err, &stopped) {
				t.Errorf("the failure no longer says the statement bound stopped it: %v", err)
			}
		})
	}

	t.Log("A concurrent build that is stopped -- by its bound, by Ctrl-C, by a duplicate -- leaves")
	t.Log("its index INVALID, which slows every write and serves no query. With no Repairer, as here,")
	t.Log("the failure names it and the drop; online_index_integration_test.go holds the repair.")
}
