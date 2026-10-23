//go:build integration

package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// onlineVersion is the staged migration every case here runs.
const onlineVersion = "20260727T153000"

// widened is the transactional operation a case puts ahead of a build, so the build is statement 2 and a stop leaves the run dirty.
func widened(t *testing.T) ir.AddColumn {
	t.Helper()

	return ir.AddColumn{Table: "people", Column: ir.Column{Name: "note", Type: mustType(t, "text"), Nullable: true}}
}

// online stages one migration of the given operations through the session's rewrites and repairs.
func online(t *testing.T, session *pgConn, ops ...ir.Operation) engine.Options {
	t.Helper()

	opts := queueOptions(t, staged{version: onlineVersion, name: "index_people", ops: ops})
	opts.Planner.Rewriter, opts.Planner.Rewrites = session, driver.RewriteNames()
	opts.Repairer = session

	return opts
}

// execBare runs one statement this test composes on the session, outside any transaction, failing the test where it fails.
func execBare(t *testing.T, session *pgConn, sql string) {
	t.Helper()

	if err := session.Exec(t.Context(), driver.Statement{SQL: sql, NoTransaction: true}); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// indexValidity reads whether an index exists and is valid, as two flags.
func indexValidity(t *testing.T, session *pgConn, name string) (bool, bool) {
	t.Helper()

	var valid bool

	err := session.conn.QueryRow(t.Context(), `SELECT x.indisvalid FROM pg_index x JOIN pg_class c ON
		c.oid = x.indexrelid WHERE c.relname = $1 AND c.relnamespace = current_schema()::regnamespace`, name).
		Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false
	}

	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}

	return true, valid
}

// runUnderWriter applies one operation while a writer inserts, failing where the writer was ever blocked.
func runUnderWriter(t *testing.T, at int, setup []string, op ir.Operation) {
	t.Helper()

	session, target := splitSession(t, at, 30*time.Second)
	people(t, session)

	for _, sql := range setup {
		execBare(t, session, sql)
	}

	w := startWriter(t, dsnFor(servers[at].env, servers[at].dsn), target)

	result, err := engine.Apply(t.Context(), session, online(t, session, op))
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	writes, blocked, werr := w.finish()
	if werr != nil {
		t.Fatalf("the writer failed: %v", werr)
	}

	if result.Runs[0].Rewritten != 1 || blocked != 0 {
		t.Errorf("%s ran rewritten %d times and left %d of %d writes waiting past %s",
			op.Kind(), result.Runs[0].Rewritten, blocked, writes+blocked, writerWait)
	}
}

func TestTheConcurrentFormsLetEveryWriteThrough(t *testing.T) {
	for at, server := range servers {
		for name, c := range map[string]struct {
			setup []string
			op    ir.Operation
		}{
			"drop_index": {[]string{"CREATE INDEX CONCURRENTLY people_v ON people (v)"},
				ir.DropIndex{Table: "people", Name: "people_v"}},
			"add_unique": {nil, ir.AddUnique{Table: "people", Name: "people_id_key", Columns: []string{"id"}}},
			"add_primary_key": {[]string{"ALTER TABLE people DROP CONSTRAINT people_pkey"},
				ir.AddPrimaryKey{Table: "people", Name: "people_pkey", Columns: []string{"id"}}},
		} {
			t.Run(server.name+"/"+name, func(t *testing.T) { runUnderWriter(t, at, c.setup, c.op) })
		}
	}

	t.Log("Each form's long statement -- the build, the drop -- runs bare under a lock writes pass,")
	t.Log("and the attach is a catalog change: no write waited past 50ms on a million rows.")
}

func TestAUniqueBuildOverDuplicatesLeavesNoInvalidIndexAndResumesOnceTheRowsAreFixed(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)
			execBare(t, session, "UPDATE people SET v = 1 WHERE id = 2")

			opts := online(t, session, widened(t), ir.AddUnique{Table: "people", Name: "people_v_key", Columns: []string{"v"}})

			_, err := engine.Apply(t.Context(), session, opts)

			var dirty *engine.DirtyError
			if !errors.As(err, &dirty) || !strings.Contains(err.Error(), `dropped the INVALID index "people_v_key"`) {
				t.Fatalf("Apply returned %v, want the run dirty saying it dropped what the build left", err)
			}

			if present, _ := indexValidity(t, session, "people_v_key"); present {
				t.Error("the INVALID index the failed build left is still there")
			}

			if found, err := session.InvalidIndexes(t.Context()); err != nil || len(found) != 0 {
				t.Errorf("the namespace reports INVALID indexes %v (%v)", found, err)
			}

			execBare(t, session, "UPDATE people SET v = 2 WHERE id = 2")

			if _, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume}); err != nil {
				t.Fatalf("resuming: %v", err)
			}

			if present, valid := indexValidity(t, session, "people_v_key"); !present || !valid {
				t.Errorf("after the resume people_v_key is present %v, valid %v", present, valid)
			}
		})
	}
}

// releasingRepairer ends what holds a build back the moment the engine asks to repair the build it stopped.
type releasingRepairer struct {
	driver.Repairer
	// release ends the holder, and is safe to call more than once.
	release func()
}

// Repair releases the holder, then repairs as the session does.
func (r releasingRepairer) Repair(ctx context.Context, op ir.Operation, stmt driver.Statement, when driver.RepairTime,
	cause error) (driver.Repaired, error) {
	r.release()

	return r.Repairer.Repair(ctx, op, stmt, when, cause)
}

func TestABuildThatGivesUpOnALockIsRepairedAndTriedAgain(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			dsn := dsnFor(server.env, server.dsn)
			session, target := splitSession(t, at, time.Second)
			people(t, session)

			holder, err := pgx.Connect(t.Context(), dsn)
			if err != nil {
				t.Fatalf("connecting the holder: %v", err)
			}

			defer func() { _ = holder.Close(context.WithoutCancel(t.Context())) }()

			opts := online(t, session, ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}})
			opts.Backoff = engine.Exponential{Base: 200 * time.Millisecond, Cap: time.Second, Retries: 5,
				Jitter: func(time.Duration) time.Duration { return 0 }}

			for _, stmt := range []string{"BEGIN ISOLATION LEVEL REPEATABLE READ",
				"SELECT count(*) FROM " + quote(target) + ".people"} {
				if _, err := holder.Exec(t.Context(), stmt); err != nil {
					t.Fatalf("holding a snapshot the build must wait out: %v", err)
				}
			}

			opts.Repairer = releasingRepairer{Repairer: session, release: func() {
				_, _ = holder.Exec(context.WithoutCancel(t.Context()), "COMMIT")
			}}

			result, err := engine.Apply(t.Context(), session, opts)
			if err != nil {
				t.Fatalf("Apply: %v", err)
			}

			if run := result.Runs[0]; run.Retries == 0 || len(run.Repairs) == 0 {
				t.Errorf("the build was retried %d times and repaired %v, want both", run.Retries, run.Repairs)
			}

			if present, valid := indexValidity(t, session, "people_v"); !present || !valid {
				t.Errorf("people_v is present %v, valid %v", present, valid)
			}
		})
	}

	t.Log("A concurrent build waits for every transaction older than it, under lock_timeout; one that")
	t.Log("gives up leaves its index INVALID, so the drop is what makes the retry possible at all.")
}

func TestABuildIsNotBoundedByTheStatementTimeout(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)

			opts := online(t, session, ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}})
			opts.StatementTimeout = 100 * time.Millisecond

			if _, err := engine.Apply(t.Context(), session, opts); err != nil {
				t.Fatalf("a build longer than statement_timeout was stopped: %v", err)
			}
		})
	}
}

// strand leaves a concurrent build of people_v stopped and unrepaired, as a killed run would.
func strand(t *testing.T, session *pgConn) engine.Options {
	t.Helper()

	return strandBuild(t, session, "people_v",
		ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}})
}

// strandBuild leaves one concurrent build stopped and unrepaired behind a transactional first statement.
func strandBuild(t *testing.T, session *pgConn, index string, build ir.Operation) engine.Options {
	t.Helper()

	opts := online(t, session, widened(t), build)
	stranded := opts
	stranded.Repairer, stranded.ConcurrentTimeout = nil, 50*time.Millisecond

	var dirty *engine.DirtyError
	if _, err := engine.Apply(t.Context(), session, stranded); !errors.As(err, &dirty) {
		t.Fatalf("the stranded run returned %v, want it dirty", err)
	}

	if present, valid := indexValidity(t, session, index); !present || valid {
		t.Fatalf("the stranded build left %s present %v, valid %v, want it INVALID", index, present, valid)
	}

	return opts
}

func TestAResumeAfterAKilledBuildRebuildsTheIndexItLeftWithReindex(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)
			opts := strand(t, session)

			found, err := session.InvalidIndexes(t.Context())
			if err != nil || len(found) != 1 || found[0].Index != "people_v" || found[0].Table != "people" {
				t.Fatalf("the stranded index was reported as %v (%v)", found, err)
			}

			if index := tableIn(t, introspect(t, session), "people").Indexes; len(index) != 1 ||
				index[0].Beyond == nil || !strings.Contains(index[0].Beyond.Detail, "INVALID") {
				t.Errorf("the snapshot holds the INVALID index unmarked: %+v", index)
			}

			settled, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume})
			if err != nil {
				t.Fatalf("resuming: %v", err)
			}

			repairs := settled.Run.Repairs
			if len(repairs) != 1 || !repairs[0].Done || !strings.Contains(repairs[0].Action, "REINDEX INDEX CONCURRENTLY") {
				t.Errorf("the resume reported %+v, want the index rebuilt by REINDEX and the build skipped", repairs)
			}

			if present, valid := indexValidity(t, session, "people_v"); !present || !valid {
				t.Errorf("after the resume people_v is present %v, valid %v", present, valid)
			}

			if runs := recordedRuns(t, session); runs[len(runs)-1].Outcome != history.Applied {
				t.Errorf("the resume did not record the migration applied")
			}
		})
	}
}

func TestAResumeDropsAnInvalidIndexThatIsNotTheOneItBuilds(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)
			opts := strand(t, session)

			execBare(t, session, "DROP INDEX CONCURRENTLY people_v")
			execBare(t, session, "UPDATE people SET parent = 1 WHERE id = 2")

			if err := session.Exec(t.Context(), driver.Statement{NoTransaction: true,
				SQL: "CREATE UNIQUE INDEX CONCURRENTLY people_v ON people (parent)"}); err == nil {
				t.Fatal("a unique build over a duplicate succeeded")
			}

			settled, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume})
			if err != nil {
				t.Fatalf("resuming: %v", err)
			}

			if repairs := settled.Run.Repairs; len(repairs) != 1 || repairs[0].Done ||
				!strings.Contains(repairs[0].Action, "dropped the INVALID index") {
				t.Errorf("the resume reported %+v, want the stranger dropped and the build run", repairs)
			}

			index := tableIn(t, introspect(t, session), "people").Indexes
			if len(index) != 1 || index[0].Name != "people_v" || index[0].Columns[0] != "v" || index[0].Beyond != nil {
				t.Errorf("after the resume people holds %+v, want people_v over v", index)
			}
		})
	}

	t.Log("REINDEX rebuilds an index as it is defined, so it is used only where the leftover is exactly")
	t.Log("the index the statement builds; an INVALID index of the same name over another column is")
	t.Log("dropped, and the statement builds the one the migration asked for.")
}

func TestAFreshRunAfterAKilledFirstBuildDropsWhatItLeftAndTriesAgainAtOnce(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)

			stopped, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
			defer cancel()

			if err := session.Exec(stopped, driver.Statement{NoTransaction: true,
				SQL: `CREATE INDEX CONCURRENTLY "people_v" ON "people" ("v");`}); err == nil {
				t.Fatal("a build given 50ms over a million rows finished")
			}

			opts := online(t, session, ir.AddIndex{Table: "people", Name: "people_v", Columns: []string{"v"}})
			opts.Backoff = engine.Exponential{Base: time.Millisecond, Cap: time.Millisecond, Retries: 2,
				Jitter: func(time.Duration) time.Duration { return 0 }}

			result, err := engine.Apply(t.Context(), session, opts)
			if err != nil {
				t.Fatalf("Apply after a killed build: %v", err)
			}

			if repairs := result.Runs[0].Repairs; len(repairs) != 1 || !strings.Contains(repairs[0].Action, "dropped") {
				t.Errorf("the run reported %+v, want the leftover dropped", repairs)
			}

			if present, valid := indexValidity(t, session, "people_v"); !present || !valid {
				t.Errorf("people_v is present %v, valid %v", present, valid)
			}
		})
	}

	t.Log("A run killed during its first statement writes no row, so the next one starts fresh and")
	t.Log("meets the INVALID index as a name already taken; that failure is the leftover itself.")
}

func TestTheConcurrentFormsLeaveTheSchemaThePlainStatementsWould(t *testing.T) {
	ops := ir.Operations{
		ir.DropIndex{Table: "people", Name: "people_v"},
		ir.AddUnique{Table: "people", Name: "people_v_key", Columns: []string{"v"}},
		ir.AddPrimaryKey{Table: "people", Name: "people_pkey", Columns: []string{"id"}},
	}

	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			var snapshots [2]string

			for i, rewrites := range [][]driver.RewriteName{nil, driver.RewriteNames()} {
				t.Run([]string{"plain", "rewritten"}[i], func(t *testing.T) {
					session, _ := splitSession(t, at, 30*time.Second)
					peopleOf(t, session, schemaRows)
					execBare(t, session, "ALTER TABLE people DROP CONSTRAINT people_pkey")
					execBare(t, session, "CREATE INDEX people_v ON people (v)")

					opts := online(t, session, ops...)
					opts.Planner.Rewrites = rewrites

					result, err := engine.Apply(t.Context(), session, opts)
					if err != nil {
						t.Fatalf("Apply: %v", err)
					}

					if i == 1 && result.Runs[0].Rewritten != len(ops) {
						t.Errorf("%d of %d operations were rewritten", result.Runs[0].Rewritten, len(ops))
					}

					snapshots[i] = schema.Render(*introspect(t, session))
				})
			}

			if snapshots[0] != snapshots[1] {
				t.Errorf("the concurrent forms left a different schema:\n%s\n--- plain ---\n%s", snapshots[1], snapshots[0])
			}

			if strings.Contains(snapshots[1], notNullSuffix) {
				t.Errorf("a check that proved the key's columns NOT NULL is still there:\n%s", snapshots[1])
			}
		})
	}
}

func TestARebuildThatFailsDropsTheLeftoverAndTheCopyItMadeAndTheNextResumeBuildsAfresh(t *testing.T) {
	for at, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			session, _ := splitSession(t, at, 30*time.Second)
			people(t, session)
			opts := strandBuild(t, session, "people_v_key",
				ir.AddUnique{Table: "people", Name: "people_v_key", Columns: []string{"v"}})

			execBare(t, session, "UPDATE people SET v = 1 WHERE id = 2")

			if _, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume}); err == nil ||
				!strings.Contains(err.Error(), "REINDEX INDEX CONCURRENTLY") {
				t.Fatalf("a resume whose rebuild meets a duplicate returned %v, want the rebuild's failure", err)
			}

			if found, err := session.InvalidIndexes(t.Context()); err != nil || len(found) != 0 {
				t.Fatalf("a failed rebuild left INVALID indexes %v (%v), want the leftover and its copy dropped", found, err)
			}

			execBare(t, session, "UPDATE people SET v = 2 WHERE id = 2")

			if _, err := engine.Resolve(t.Context(), session, opts, engine.Repair{Mode: engine.RepairResume}); err != nil {
				t.Fatalf("the resume after the rows were fixed: %v", err)
			}

			if present, valid := indexValidity(t, session, "people_v_key"); !present || !valid {
				t.Errorf("after the second resume people_v_key is present %v, valid %v", present, valid)
			}
		})
	}

	t.Log("Measured: REINDEX CONCURRENTLY over a duplicate leaves <name>_ccnew INVALID beside the")
	t.Log("original. Both are dropped, so the next resume finds nothing left and builds afresh.")
}
