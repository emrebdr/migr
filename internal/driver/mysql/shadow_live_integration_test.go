//go:build integration

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// A shadow-table migration is held to the done-when here, on the three servers compose runs with a
// row-format binary log: mysql80, whose position is a file and an offset, mysql80gtid, whose position is
// a GTID set, and mariadb114binlog, whose position is a MariaDB GTID (M13-T17). Eight writers
// keep a table and an unchanged control table equal, one row of each per transaction, from before the
// copy starts until after the cutover; the migration changes a column's type and widens another; and
// the two tables are compared by row count and checksum once it has cut over. A writer that fails is a
// failure of the migration, however it failed.

// shadowServers are the servers a shadow-table migration runs against: a MySQL recording GTIDs, one not, and a MariaDB.
var shadowServers = []server{
	servers[0],
	{name: "mysql80gtid", env: "MIGR_TEST_MYSQL80GTID_DSN", dsn: "migr:migr@tcp(127.0.0.1:55481)/migr",
		variant: MySQL, major: 8, minor: 0},
	mariadbBinlog,
}

// mariadbBinlog is the MariaDB writing a row-format binary log, whose positions are domain-server-seq.
var mariadbBinlog = server{name: "mariadb114binlog", env: "MIGR_TEST_MARIADB114BINLOG_DSN",
	dsn: "migr:migr@tcp(127.0.0.1:55412)/migr", variant: MariaDB, major: 11, minor: 4}

// The staged migration's version, which every derived name carries, and the rows the table starts with.
const (
	stagedVersion = "20260927T153000"
	seededRows    = 20000
	writerCount   = 8
	hiddenID      = 9000000
)

// shadowCase is one table changed through a shadow on one server, with everything that watches it.
type shadowCase struct {
	// s is the server.
	s server
	// d is the driver a stream is opened through.
	d driver.Driver
	// c is the session the migration runs on.
	c *mysqlConn
	// watch is a second session, which reads and confirms the migration's state as an operator would.
	watch *mysqlConn
	// table is the table the migration changes, and control the one the writers keep equal to it.
	table, control string
	// pool is what the writers write through, which is no migr session.
	pool *sql.DB
}

// eachShadowServer runs one case against every shadow server, each with a table and a history of its own.
func eachShadowServer(t *testing.T, run func(t *testing.T, sc *shadowCase)) {
	t.Helper()

	for _, s := range shadowServers {
		t.Run(s.name, func(t *testing.T) {
			run(t, newShadowCase(t, s))
		})
	}
}

// newShadowCase opens both sessions and the writers' pool, and seeds the table and its control.
func newShadowCase(t *testing.T, s server) *shadowCase {
	t.Helper()

	opts := driver.Options{HistoryTable: uniqueName(t)}

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	sc := &shadowCase{s: s, d: d, c: openServer(t, s, opts), watch: openServer(t, s, opts)}
	exec(t, sc.c, "SET time_zone = '+05:00'")
	sc.table = "sh" + digestOf(t.Name())
	sc.control = sc.table + "_control"

	pool, err := sql.Open("mysql", s.dsnFor())
	if err != nil {
		t.Fatalf("opening the writers' pool: %v", err)
	}

	pool.SetMaxOpenConns(writerCount + 2)
	t.Cleanup(func() { _ = pool.Close() })
	sc.pool = pool

	held := history.ShadowTable(opts.HistoryTable)
	objects := []string{sc.table, sc.control, engine.ShadowName(sc.table, stagedVersion),
		engine.RetiredName(sc.table, stagedVersion), opts.HistoryTable, held,
		history.ProgressTable(opts.HistoryTable), history.ReleaseTable(opts.HistoryTable)}
	drop(t.Context(), sc.c, objects...)
	dropLater(t, sc.c, objects...)

	sc.seed(t)

	return sc
}

// digestOf shortens a test's name to a stable suffix, so the derived names stay well inside the limit.
func digestOf(name string) string {
	var h uint32 = 2166136261
	for i := range len(name) {
		h = (h ^ uint32(name[i])) * 16777619
	}

	return fmt.Sprintf("%08x", h)
}

// seed creates the table and its control with the same rows, and leaves the table's counter past every id.
func (sc *shadowCase) seed(t *testing.T) {
	t.Helper()

	for _, table := range []string{sc.table, sc.control} {
		exec(t, sc.c, "CREATE TABLE "+quote(table)+" (id bigint NOT NULL AUTO_INCREMENT PRIMARY KEY, "+
			"n int NOT NULL, label varchar(40) NOT NULL, at datetime(6) NOT NULL, price decimal(10,2), "+
			"flag enum('a','b','c') NOT NULL DEFAULT 'a', note text, seen timestamp(6) NULL) DEFAULT CHARSET="+characterSet+" COLLATE="+collation)
	}

	exec(t, sc.c, "SET SESSION "+recursionLimit(sc.s)+" = 100000")
	exec(t, sc.c, "INSERT INTO "+quote(sc.table)+" (n, label, at, price, flag, note) WITH RECURSIVE seq(i) AS "+
		"(SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < "+fmt.Sprint(seededRows)+") SELECT i, "+
		"CONCAT('row ', i), TIMESTAMP('2026-01-01') + INTERVAL i SECOND + INTERVAL i MICROSECOND, "+
		"IF(i % 7 = 0, NULL, i / 100), ELT(1 + i % 3, 'a', 'b', 'c'), IF(i % 5 = 0, NULL, REPEAT('é', i % 9)) FROM seq")
	exec(t, sc.c, "INSERT INTO "+quote(sc.table)+" (id, n, label, at) VALUES ("+fmt.Sprint(hiddenID)+", 0, 'gone', NOW())")
	exec(t, sc.c, "DELETE FROM "+quote(sc.table)+" WHERE id = "+fmt.Sprint(hiddenID))
	exec(t, sc.c, "INSERT INTO "+quote(sc.control)+" SELECT * FROM "+quote(sc.table))
}

// recursionLimit is the variable bounding a recursive CTE, which each variant names its own way.
func recursionLimit(s server) string {
	if s.variant == MariaDB {
		return "max_recursive_iterations"
	}

	return "cte_max_recursion_depth"
}

// changes are the migration's operations: a column's type changed, and another widened.
func (sc *shadowCase) changes(t *testing.T) ir.Operations {
	t.Helper()

	wide, narrow := mustType(t, "int64"), mustType(t, "string(80)")

	return ir.Operations{
		ir.AlterColumn{Table: sc.table, Column: "n", Type: &wide},
		ir.AlterColumn{Table: sc.table, Column: "label", Type: &narrow},
	}
}

// options is a run of the staged migration through a shadow table, telling watch about every moment.
func (sc *shadowCase) options(t *testing.T, watch func(engine.ShadowProgress)) engine.Options {
	t.Helper()

	opts := staged(t, sc.changes(t))
	opts.Online = migration.Online{stagedVersion: migration.ShadowTable}
	opts.Shadower = sc.c
	opts.Planner.Expander = sc.c
	dsn := sc.s.dsnFor()
	opts.Capture = func(ctx context.Context, req capture.Request) (capture.Stream, error) {
		return sc.d.Capture(ctx, dsn, req)
	}
	opts.Pacer = engine.Steady{Rows: 500, Pause: 5 * time.Millisecond}
	opts.CaptureLag = 5 * time.Second
	opts.Shadowed = watch

	return opts
}

// writers is the load: each writer owns its keys, and writes one row of the table and its control per transaction.
type writers struct {
	// failed counts every write that did not commit.
	failed atomic.Int64
	// written counts every write that did.
	written atomic.Int64
	// first is the first failure, for the report.
	first atomic.Value
	// stop ends the load.
	stop chan struct{}
	// done is closed once every writer has returned.
	done sync.WaitGroup
}

// startWriters begins the load against a case's table and control.
func (sc *shadowCase) startWriters(t *testing.T) *writers {
	t.Helper()

	w := &writers{stop: make(chan struct{})}

	for i := range writerCount {
		w.done.Add(1)

		go func() {
			defer w.done.Done()
			sc.write(w, i)
		}()
	}

	t.Cleanup(w.halt)

	return w
}

// halt ends the load and waits for every writer, once.
func (w *writers) halt() {
	select {
	case <-w.stop:
	default:
		close(w.stop)
	}

	w.done.Wait()
}

// write is one writer: inserts, updates, deletes and key moves over its own rows until the load stops.
func (sc *shadowCase) write(w *writers, writer int) {
	var live []int64
	for id := int64(writer + 1); id <= seededRows; id += writerCount {
		live = append(live, id)
	}

	next := int64(1000000 + writer*100000)

	for step := 0; ; step++ {
		select {
		case <-w.stop:
			return
		default:
		}

		statement, args, apply := sc.oneWrite(writer, &live, &next, step)
		if err := sc.writeBoth(statement, args); err != nil {
			w.failed.Add(1)
			w.first.CompareAndSwap(nil, err.Error())

			continue
		}

		apply()
		w.written.Add(1)
	}
}

// oneWrite picks the writer's next write by its step, returning it and what committing it does to its own rows.
func (sc *shadowCase) oneWrite(writer int, live *[]int64, next *int64, step int) (string, []any, func()) {
	pick := (step*7919 + writer*104729) % len(*live)
	id := (*live)[pick]

	switch r := (step*31 + writer*17) % 20; {
	case r < 8:
		seen := time.Date(2026, 3, 29, 0, 30, 0, 0, time.UTC).Add(time.Duration(step) * 7 * time.Minute)

		return "UPDATE %s SET n = n + 1, label = ?, price = ?, flag = ?, note = ?, seen = ? WHERE id = ?",
			[]any{fmt.Sprintf("upd %d", step), fmt.Sprintf("%d.%02d", step%1000, step%100),
				[]string{"a", "b", "c"}[step%3], strings.Repeat("ü", step%7),
				seen.Format("2006-01-02 15:04:05.000000"), id}, func() {}
	case r < 11:
		return "DELETE FROM %s WHERE id = ?", []any{id}, func() {
			*live = append((*live)[:pick], (*live)[pick+1:]...)
		}
	case r < 12:
		*next++
		moved := *next

		return "UPDATE %s SET id = ? WHERE id = ?", []any{moved, id}, func() { (*live)[pick] = moved }
	default:
		*next++
		created := *next

		at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC).Add(time.Duration(step) * time.Microsecond * 1001)

		return "INSERT INTO %s (id, n, label, at, price, flag, note) VALUES (?, ?, ?, ?, ?, 'b', ?)",
			[]any{created, step, fmt.Sprintf("new %d", step), at.Format("2006-01-02 15:04:05.000000"), nil, "ok"},
			func() { *live = append(*live, created) }
	}
}

// writeBoth makes one write to the table and the same write to its control, in one transaction.
func (sc *shadowCase) writeBoth(statement string, args []any) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()

	tx, err := sc.pool.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	for _, table := range []string{sc.table, sc.control} {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(statement, quote(table)), args...); err != nil {
			return errors.Join(err, tx.Rollback())
		}
	}

	return tx.Commit()
}

// confirmOnce confirms the cutover once the copy has been held in sync for a while, as an operator would.
func (sc *shadowCase) confirmOnce(t *testing.T, held time.Duration) chan struct{} {
	t.Helper()

	done := make(chan struct{})
	store, ok := sc.watch.History().(history.Shadows)
	if !ok {
		t.Fatalf("the history %T keeps no shadow state", sc.watch.History())
	}

	go func() {
		defer close(done)

		const poll = 100 * time.Millisecond

		var syncing time.Duration

		for range 2400 {
			time.Sleep(poll)

			states, err := store.ShadowStates(context.Background())
			state, found := history.FindShadow(states, stagedVersion)

			switch {
			case err != nil || !found || state.Stage != history.StageSyncing:
				syncing = 0
			case syncing < held:
				syncing += poll
			default:
				_ = store.ConfirmShadow(context.Background(), stagedVersion)

				return
			}
		}
	}()

	return done
}

// sums reads a table's row count and a checksum over every column, each rendered as text.
func (sc *shadowCase) sums(t *testing.T, table string) string {
	t.Helper()

	var count, sum string

	err := sc.c.runner().QueryRowContext(t.Context(), "SELECT COUNT(*), COALESCE(BIT_XOR(CRC32("+rowText+")), 0) FROM "+
		quote(table)).Scan(&count, &sum)
	if err != nil {
		t.Fatalf("summing %s: %v", table, err)
	}

	return count + " rows, checksum " + sum
}

// checkEqual holds the table to its control by row count and checksum.
func (sc *shadowCase) checkEqual(t *testing.T) {
	t.Helper()

	if got, want := sc.sums(t, sc.table), sc.sums(t, sc.control); got != want {
		t.Errorf("after the cutover %s holds %s, and the control the writers kept beside it holds %s, "+
			"differing at %v", sc.table, got, want, sc.differing(t))
	}
}

// rowText renders every column of a row as one string, the way sums checks it.
const rowText = "CONCAT_WS('#', id, n, label, DATE_FORMAT(at, '%Y-%m-%d %H:%i:%s.%f'), COALESCE(price, 'null'), " +
	"flag, COALESCE(HEX(note), 'null'), COALESCE(UNIX_TIMESTAMP(seen), 'null'))"

// differing lists a few rows the table and its control disagree on, each side as text.
func (sc *shadowCase) differing(t *testing.T) []string {
	t.Helper()

	query := "SELECT CONCAT(COALESCE(a.r, '-'), ' | ', COALESCE(b.r, '-')) FROM (SELECT id, " + rowText +
		" AS r FROM " + quote(sc.table) + ") a LEFT JOIN (SELECT id, " + rowText + " AS r FROM " +
		quote(sc.control) + ") b USING (id) WHERE NOT (a.r <=> b.r) LIMIT 5"

	return read(t, sc.c, query)
}

// checkLoad fails a case where any write failed, and one where the load wrote nothing.
func (sc *shadowCase) checkLoad(t *testing.T, w *writers) {
	t.Helper()

	w.halt()

	if failed := w.failed.Load(); failed > 0 {
		t.Errorf("%d of %d writes failed under the migration, the first: %v", failed, failed+w.written.Load(),
			w.first.Load())
	}

	if w.written.Load() < 100 {
		t.Errorf("the load wrote %d rows, which is no load", w.written.Load())
	}
}

// checkCutOver holds the table to the migration having changed it, kept the original, and left no copy.
func (sc *shadowCase) checkCutOver(t *testing.T) {
	t.Helper()

	if got := scalar(t, sc.c, "SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
		"DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'n'", sc.table); got != "bigint" {
		t.Errorf("n is %s after the cutover, want bigint", got)
	}

	if got := scalar(t, sc.c, "SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
		"DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'label'", sc.table); got != "varchar(80)" {
		t.Errorf("label is %s after the cutover, want varchar(80)", got)
	}

	for name, want := range map[string]string{
		engine.ShadowName(sc.table, stagedVersion):  "0",
		engine.RetiredName(sc.table, stagedVersion): "1",
	} {
		if got := scalar(t, sc.c, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
			"DATABASE() AND TABLE_NAME = ?", name); got != want {
			t.Errorf("%s is held by %s tables after the cutover, want %s", name, got, want)
		}
	}
}

// checkCounter holds the swapped-in table to handing out ids past every one the original ever did.
func (sc *shadowCase) checkCounter(t *testing.T) {
	t.Helper()

	result, err := sc.pool.ExecContext(t.Context(), "INSERT INTO "+quote(sc.table)+" (n, label, at) VALUES (1, 'auto', NOW())")
	if err != nil {
		t.Fatalf("inserting with a generated id: %v", err)
	}

	if id, _ := result.LastInsertId(); id <= hiddenID {
		t.Errorf("the table handed out id %d after the cutover, which the original had already passed (%d)", id, hiddenID)
	}
}

// positionKind names the form a server's recorded position takes.
func positionKind(s server) string {
	if s.name == "mysql80gtid" || s.variant == MariaDB {
		return gtidPrefix
	}

	return filePrefix
}

func TestAColumnTypeChangesThroughAShadowUnderLoadWithNoFailedWriteAndNoLostRow(t *testing.T) {
	eachShadowServer(t, func(t *testing.T, sc *shadowCase) {
		load := sc.startWriters(t)
		confirmed := sc.confirmOnce(t, 1500*time.Millisecond)

		var positions []string

		opts := sc.options(t, func(p engine.ShadowProgress) { positions = append(positions, p.Position) })

		result, err := engine.Apply(t.Context(), sc.c, opts)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		<-confirmed
		time.Sleep(time.Second)
		sc.checkLoad(t, load)
		sc.checkEqual(t)
		sc.checkCutOver(t)
		sc.checkCounter(t)

		run := result.Runs[0].Shadow
		if run == nil || run.Copied < seededRows/2 || run.Applied == 0 {
			t.Errorf("the run reported %+v, want the copy and the replay both to have done work", run)
		}

		for _, position := range positions {
			if !strings.HasPrefix(position, positionKind(sc.s)) {
				t.Errorf("a position was recorded as %q on %s, want %s", position, sc.s.name, positionKind(sc.s))

				break
			}
		}

		if _, err := engine.Retire(t.Context(), sc.c, opts, stagedVersion); err != nil {
			t.Fatalf("Retire: %v", err)
		}

		if got := scalar(t, sc.c, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
			"DATABASE() AND TABLE_NAME = ?", engine.RetiredName(sc.table, stagedVersion)); got != "0" {
			t.Errorf("Retire left the original behind")
		}
	})
}

func TestAShadowMigrationStoppedMidCopyOrMidHoldCarriesOnFromItsRecordedPosition(t *testing.T) {
	for name, stop := range map[string]func(engine.ShadowProgress) bool{
		"mid-copy": func(p engine.ShadowProgress) bool { return p.Stage == history.StageCopying && p.Copied >= 4000 },
		"mid-hold": func(p engine.ShadowProgress) bool { return p.Holding },
	} {
		t.Run(name, func(t *testing.T) {
			eachShadowServer(t, func(t *testing.T, sc *shadowCase) {
				load := sc.startWriters(t)

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				var stoppedAt string

				_, err := engine.Apply(ctx, sc.c, sc.options(t, func(p engine.ShadowProgress) {
					if stoppedAt == "" && stop(p) {
						stoppedAt = p.Position
						cancel()
					}
				}))
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("the stopped run returned %v, want it canceled", err)
				}

				states, err := sc.watch.History().(history.Shadows).ShadowStates(t.Context())
				state, found := history.FindShadow(states, stagedVersion)

				if err != nil || !found || !strings.HasPrefix(state.Position, positionKind(sc.s)) {
					t.Fatalf("the stopped run recorded %+v (%v), want a %s position", state, err, positionKind(sc.s))
				}

				confirmed := sc.confirmOnce(t, time.Second)

				var resumed bool

				if _, err := engine.Apply(t.Context(), sc.c, sc.options(t, func(p engine.ShadowProgress) {
					resumed = resumed || p.Resumed
				})); err != nil {
					t.Fatalf("the resumed Apply: %v", err)
				}

				<-confirmed
				sc.checkLoad(t, load)
				sc.checkEqual(t)
				sc.checkCutOver(t)

				if !resumed {
					t.Error("the second run did not report carrying the first on")
				}
			})
		})
	}
}

// abandon settles a stopped shadow-table migration as rolled back, as migr resolve --rolled-back does.
func (sc *shadowCase) abandon(t *testing.T) {
	t.Helper()

	opts := sc.options(t, nil)

	if _, err := engine.Resolve(t.Context(), sc.c, opts, engine.Repair{Mode: engine.RepairRolledBack,
		Reason: "abandoned by the test"}); err != nil {
		t.Fatalf("Resolve --rolled-back: %v", err)
	}
}

// checkAbandoned holds an abandoned migration to leaving the table as it was and nothing of its own behind.
func (sc *shadowCase) checkAbandoned(t *testing.T) {
	t.Helper()

	if got := scalar(t, sc.c, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
		"DATABASE() AND TABLE_NAME IN (?, ?)", engine.ShadowName(sc.table, stagedVersion),
		engine.RetiredName(sc.table, stagedVersion)); got != "0" {
		t.Errorf("abandoning left %s of the copy and the retired name behind", got)
	}

	if got := scalar(t, sc.c, "SELECT DATA_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
		"DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'n'", sc.table); got != "int" {
		t.Errorf("abandoning left n as %s, want the original int", got)
	}

	states, err := sc.watch.History().(history.Shadows).ShadowStates(t.Context())
	if _, found := history.FindShadow(states, stagedVersion); err != nil || found {
		t.Errorf("abandoning left the state row behind (%v)", err)
	}

	id := freeID(engine.CaptureName(sc.table, stagedVersion), nil)

	listing := "SHOW REPLICAS"
	if sc.s.variant == MariaDB {
		listing = "SHOW SLAVE HOSTS"
	}

	rows, err := sc.c.runner().QueryContext(t.Context(), listing)
	if err != nil {
		t.Fatalf("%s: %v", listing, err)
	}

	defer func() { _ = rows.Close() }()

	taken, err := replicaIDs(rows)

	if err != nil || taken[id] {
		t.Errorf("a reader registered as %d is still connected after abandoning (%v)", id, err)
	}
}

func TestAbandoningAShadowMigrationAtAnyStageLeavesNoCopyNoReaderAndNoState(t *testing.T) {
	for name, stop := range map[string]func(engine.ShadowProgress) bool{
		"copying": func(p engine.ShadowProgress) bool { return p.Stage == history.StageCopying && p.Copied > 0 },
		"syncing": func(p engine.ShadowProgress) bool { return p.Holding },
	} {
		t.Run(name, func(t *testing.T) {
			eachShadowServer(t, func(t *testing.T, sc *shadowCase) {
				load := sc.startWriters(t)

				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()

				_, err := engine.Apply(ctx, sc.c, sc.options(t, func(p engine.ShadowProgress) {
					if stop(p) {
						cancel()
					}
				}))
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("the stopped run returned %v, want it canceled", err)
				}

				sc.abandon(t)
				sc.checkLoad(t, load)
				sc.checkAbandoned(t)
				sc.checkEqual(t)
			})
		})
	}
}

func TestATableChangedUnderAHeldShadowStopsTheMigrationNamingTheStatement(t *testing.T) {
	eachShadowServer(t, func(t *testing.T, sc *shadowCase) {

		altered := false

		_, err := engine.Apply(t.Context(), sc.c, sc.options(t, func(p engine.ShadowProgress) {
			if p.Holding && !altered {
				altered = true

				if _, err := sc.pool.ExecContext(context.Background(), "ALTER TABLE "+quote(sc.table)+
					" ADD COLUMN extra int"); err != nil {
					t.Errorf("altering the table under the hold: %v", err)
				}
			}
		}))
		if err == nil || !strings.Contains(err.Error(), "changed under this shadow-table migration") {
			t.Fatalf("Apply returned %v, want the stream to stop naming the ALTER", err)
		}

		sc.abandon(t)
	})
}

func TestAPositionTheServerPurgedIsRefusedNamingTheRetentionSetting(t *testing.T) {
	for _, s := range shadowServers[1:] {
		t.Run(s.name, func(t *testing.T) {
			purgedAfterAStop(t, newShadowCase(t, s))
		})
	}
}

// purgedAfterAStop stops a migration mid-copy, purges every log it read, and resumes it.
func purgedAfterAStop(t *testing.T, sc *shadowCase) {
	t.Helper()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	_, err := engine.Apply(ctx, sc.c, sc.options(t, func(p engine.ShadowProgress) {
		if p.Copied > 0 {
			cancel()
		}
	}))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("the stopped run returned %v, want it canceled", err)
	}

	root, err := sql.Open("mysql", rootOf(sc.s))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = root.Close() }()

	for _, stmt := range []string{"FLUSH BINARY LOGS", "FLUSH BINARY LOGS"} {
		if _, err := root.ExecContext(t.Context(), stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	purgeTo(t, root, firstColumn(t, root, "SHOW MASTER STATUS"))

	_, err = engine.Apply(t.Context(), sc.c, sc.options(t, nil))
	if err == nil || !strings.Contains(err.Error(), "binlog_expire_logs_seconds") {
		t.Errorf("a resume from a purged position returned %v, want the retention named", err)
	}

	sc.abandon(t)
}

// rootOf is a server's root, which flushing and purging its log needs and migr never does.
func rootOf(s server) string {
	return strings.Replace(strings.TrimSuffix(s.dsnFor(), "migr"), "migr:migr@", "root:migr@", 1)
}

// purgeTo purges every log before one file, again until it is gone: MariaDB keeps a file its checkpoint still needs.
func purgeTo(t *testing.T, root *sql.DB, file string) {
	t.Helper()

	for range 100 {
		if _, err := root.ExecContext(t.Context(), "PURGE BINARY LOGS TO '"+file+"'"); err != nil {
			t.Fatalf("purging: %v", err)
		}

		if firstColumn(t, root, "SHOW BINARY LOGS") == file {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("the server still keeps the logs before %s", file)
}

// firstColumn reads the first column of a statement's first row, however many columns this server's answer has.
func firstColumn(t *testing.T, root *sql.DB, query string) string {
	t.Helper()

	rows, err := root.QueryContext(t.Context(), query)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = rows.Close() }()

	columns, err := rows.Columns()
	if err != nil || !rows.Next() {
		t.Fatalf("%s: %v", query, err)
	}

	values := make([]sql.NullString, len(columns))
	targets := make([]any, len(values))

	for i := range values {
		targets[i] = &values[i]
	}

	if err := rows.Scan(targets...); err != nil {
		t.Fatal(err)
	}

	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	return values[0].String
}

func TestANarrowingChangeThatWouldTruncateFailsTheCopyRatherThanTruncating(t *testing.T) {
	sc := newShadowCase(t, shadowServers[0])

	narrow := mustType(t, "string(5)")
	opts := sc.options(t, nil)
	opts.Source = staging{ops: ir.Operations{ir.AlterColumn{Table: sc.table, Column: "label", Type: &narrow}}}

	_, err := engine.Apply(t.Context(), sc.c, opts)
	if err == nil || !strings.Contains(err.Error(), "Data truncated for column") {
		t.Fatalf("a change narrowing label below what its rows hold returned %v, want the copy refused", err)
	}

	if got := scalar(t, sc.c, "SELECT COLUMN_TYPE FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = "+
		"DATABASE() AND TABLE_NAME = ? AND COLUMN_NAME = 'label'", sc.table); got != "varchar(40)" {
		t.Errorf("label is %s after a refused copy, want it untouched", got)
	}

	sc.abandon(t)
	sc.checkEqual(t)
}

func TestACatchUpThatOverrunsTheLockTimeoutReleasesTheLockAndSwapsNothing(t *testing.T) {
	eachShadowServer(t, func(t *testing.T, sc *shadowCase) {
		shadow := driver.Shadow{Table: sc.table, Shadow: engine.ShadowName(sc.table, stagedVersion),
			Retired: engine.RetiredName(sc.table, stagedVersion), Key: []string{"id"}}

		if err := sc.c.History().Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		exec(t, sc.c, "CREATE TABLE "+quote(shadow.Shadow)+" LIKE "+quote(sc.table))
		sc.c.statementWait = 300 * time.Millisecond

		err := sc.c.Cutover(t.Context(), shadow, func(ctx context.Context) error {
			<-ctx.Done()

			return ctx.Err()
		})
		if !errors.Is(err, driver.ErrStatementLockTimeout) {
			t.Fatalf("an overrunning catch-up returned %v, want a lock wait the engine retries", err)
		}

		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		defer cancel()

		if _, err := sc.pool.ExecContext(ctx, "UPDATE "+quote(sc.table)+" SET n = n WHERE id = 1"); err != nil {
			t.Errorf("a writer could not reach the table after the overrun: %v", err)
		}

		if got := scalar(t, sc.c, "SELECT COUNT(*) FROM information_schema.TABLES WHERE TABLE_SCHEMA = "+
			"DATABASE() AND TABLE_NAME = ?", shadow.Retired); got != "0" {
			t.Error("an overrunning catch-up swapped the tables anyway")
		}
	})
}
