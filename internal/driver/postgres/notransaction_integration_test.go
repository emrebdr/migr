//go:build integration

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// activeSQLTransaction is the SQLSTATE PostgreSQL refuses CREATE INDEX CONCURRENTLY with inside a transaction.
const activeSQLTransaction = "25001"

// splitSession opens a migration session in a schema of its own, its statements bounded to wait.
func splitSession(t *testing.T, server int, wait time.Duration) (*pgConn, string) {
	t.Helper()

	dsn := dsnFor(servers[server].env, servers[server].dsn)
	target := scratchSchema(t, dsn)

	conn := open(t, dsn, driver.Options{Schema: target, HistoryTable: historyTable,
		StatementLockWait: wait})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T", conn)
	}

	return session, target
}

// raw addresses a statement to this engine, declaring whether it can run inside a transaction.
func raw(sql string, standalone bool) ir.Raw {
	return ir.Raw{Driver: Name, SQL: sql, NoTransaction: standalone}
}

// indexPeople is a migration that fills a table and builds an index over it concurrently.
func indexPeople(unique, standalone bool) staged {
	build := "CREATE INDEX CONCURRENTLY people_email ON people (email)"
	if unique {
		build = "CREATE UNIQUE INDEX CONCURRENTLY people_email ON people (email)"
	}

	return staged{
		version: "20260727T153000",
		name:    "index_people",
		ops: ir.Operations{
			raw("CREATE TABLE people (id int PRIMARY KEY, email text)", false),
			raw("INSERT INTO people SELECT i, 'p' || (i % 50) FROM generate_series(1, 200) i", false),
			raw(build, standalone),
			raw("ANALYZE people", false),
		},
		down: ir.Operations{
			raw("DROP INDEX people_email", false),
			raw("DROP TABLE people", false),
		},
	}
}

// indexValid reports whether the index exists, and whether PostgreSQL considers it usable.
func indexValid(t *testing.T, session *pgConn) (bool, bool) {
	t.Helper()

	rows, err := session.Query(t.Context(), driver.Query{SQL: "SELECT i.indisvalid::text FROM pg_index i " +
		"JOIN pg_class c ON c.oid = i.indexrelid WHERE c.relname = 'people_email' " +
		"AND c.relnamespace = current_schema()::regnamespace"})
	if err != nil {
		t.Fatalf("reading the index: %v", err)
	}

	if len(rows.Values) == 0 {
		return false, false
	}

	return true, *rows.Values[0][0] == "true"
}

func TestCreateIndexConcurrentlyRunsEndToEndInAMigrationSplitAroundIt(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, retryLockWait)

			result, err := engine.Apply(t.Context(), session, queueOptions(t, indexPeople(false, true)))
			if err != nil {
				t.Fatalf("Apply returned %v, want the migration to run in three parts", err)
			}

			if exists, valid := indexValid(t, session); !exists || !valid {
				t.Fatalf("after the run the index exists %v and is valid %v, want both", exists, valid)
			}

			var outcomes []history.Outcome
			for _, row := range recordedRuns(t, session) {
				outcomes = append(outcomes, row.Outcome)
			}

			want := []history.Outcome{history.Dirty, history.Dirty, history.Applied}
			if len(outcomes) != len(want) || outcomes[0] != want[0] || outcomes[2] != want[2] {
				t.Errorf("the history holds %v, want %v: a row per part that committed", outcomes, want)
			}

			if result.Runs[0].Ordinal != 3 {
				t.Errorf("the applied row is at ordinal %d, want 3", result.Runs[0].Ordinal)
			}

			if _, err := engine.Rollback(t.Context(), session, queueOptions(t, indexPeople(false, true))); err != nil {
				t.Fatalf("Rollback: %v", err)
			}

			if exists, _ := indexValid(t, session); exists {
				t.Error("the rollback left the index in place")
			}
		})
	}

	t.Log("CREATE INDEX CONCURRENTLY builds without blocking writes, and PostgreSQL refuses it in a")
	t.Log("transaction block. The table and its rows commit first, the index is built bare, and")
	t.Log("ANALYZE commits with the applied row. Its DROP INDEX inverse runs in one transaction.")
}

func TestWithoutTheDeclarationCreateIndexConcurrentlyIsRefusedByTheServer(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, retryLockWait)

			_, err := engine.Apply(t.Context(), session, queueOptions(t, indexPeople(false, false)))

			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != activeSQLTransaction {
				t.Fatalf("Apply returned %v, want SQLSTATE %s", err, activeSQLTransaction)
			}

			if exists, _ := indexValid(t, session); exists {
				t.Error("a statement the server refused left an index behind")
			}
		})
	}

	t.Log("The control: the same migration with no_transaction unset runs in one transaction, and")
	t.Log("the server refuses it -- so the declaration is what made the other test pass.")
}

func TestAConcurrentBuildThatFailsLeavesTheMigrationDirtyAtTheStatementBeforeIt(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, retryLockWait)

			_, err := engine.Apply(t.Context(), session, queueOptions(t, indexPeople(true, true)))

			var dirty *engine.DirtyError
			if !errors.As(err, &dirty) || dirty.LastStatement != 2 || !dirty.Split {
				t.Fatalf("Apply returned %v, want a split run dirty at statement 2", err)
			}

			runs := recordedRuns(t, session)
			if last := runs[len(runs)-1]; last.Outcome != history.Dirty || last.LastStatement != 2 {
				t.Errorf("the last row is %s at statement %d, want dirty at 2", last.Outcome, last.LastStatement)
			}

			if exists, valid := indexValid(t, session); exists && !valid {
				t.Log("The failed build left people_email INVALID, as PostgreSQL documents: it degrades")
				t.Log("writes and is used by no query. Detecting and dropping it is M13-T11's.")
			}

			if _, err := engine.Apply(t.Context(), session, queueOptions(t, indexPeople(true, true))); !errors.Is(err, engine.ErrDirty) {
				t.Errorf("the next run returned %v, want it refused over the dirty state", err)
			}
		})
	}
}

func TestAConcurrentBuildBlockedByALockGivesUpWithinItsBoundAndIsNotRetried(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, target := splitSession(t, at, 300*time.Millisecond)

			created := indexPeople(false, true)
			created.ops = created.ops[:2]

			if _, err := engine.Apply(t.Context(), session, queueOptions(t, created)); err != nil {
				t.Fatalf("creating the table: %v", err)
			}

			holder, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatalf("connecting the holder: %v", err)
			}

			defer func() { _ = holder.Close(context.WithoutCancel(t.Context())) }()

			tx, err := holder.Begin(t.Context())
			if err != nil {
				t.Fatalf("beginning the holder: %v", err)
			}

			defer func() { _ = tx.Rollback(context.WithoutCancel(t.Context())) }()

			if _, err := tx.Exec(t.Context(), "LOCK TABLE "+quote(target)+".people IN SHARE MODE"); err != nil {
				t.Fatalf("taking the holder's lock: %v", err)
			}

			build := staged{version: "20260727T153100", name: "build_index", ops: ir.Operations{
				raw("CREATE INDEX CONCURRENTLY people_email ON people (email)", true)}}
			opts := queueOptions(t, created, build)
			opts.Backoff = engine.Exponential{Base: 50 * time.Millisecond, Cap: time.Second, Retries: 3}

			retries := 0
			opts.Retried = func(engine.Retry) { retries++ }

			late, err := timedRead(5*time.Second, func() error {
				_, err := engine.Apply(t.Context(), session, opts)

				return err
			})

			if !errors.Is(err, driver.ErrStatementLockTimeout) {
				t.Fatalf("Apply returned %v, want the lock wait reported", err)
			}

			if late || retries != 0 {
				t.Errorf("the build came back late %v after %d retries, want one bounded wait and none",
					late, retries)
			}
		})
	}

	t.Log("Invariant #12 holds outside a transaction too: lock_timeout is the session's, pinned at")
	t.Log("connect, so the bare statement gives up at its bound. It is not retried, because a")
	t.Log("concurrent build that gave up may have left an index behind that a retry would trip over.")
}
