//go:build zerodowntime

package mysql

import (
	"context"
	"database/sql"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	zerodowntime "github.com/emrebdr/migr/test/conformance/zerodowntime"
)

// zdServers are the servers the zero-downtime suite holds this driver to; three of them permit capture.
//
// The two that exist for their change stream run the shadow-table cases alone: a GTID set or a binary
// log changes how a shadow is captured, and no lock any other operation takes, which the server of the
// same engine beside each already holds to its verdict.
var zdServers = []struct {
	name, env, dsn string
	variant        Variant
	shadowOnly     bool
}{
	{"mysql80", "MIGR_TEST_MYSQL80_DSN", "migr:migr@tcp(127.0.0.1:55480)/migr", MySQL, false},
	{"mysql80gtid", "MIGR_TEST_MYSQL80GTID_DSN", "migr:migr@tcp(127.0.0.1:55481)/migr", MySQL, true},
	{"mariadb114", "MIGR_TEST_MARIADB114_DSN", "migr:migr@tcp(127.0.0.1:55411)/migr", MariaDB, false},
	{"mariadb114binlog", "MIGR_TEST_MARIADB114BINLOG_DSN", "migr:migr@tcp(127.0.0.1:55412)/migr", MariaDB, true},
}

// zdLockWait is the suite's lock wait on this driver: lock_wait_timeout holds whole seconds, so one rather than half the budget.
const zdLockWait = time.Second

// Error numbers a query meets once the table or column it names is gone.
const (
	zdNoSuchTable  = 1146
	zdNoSuchColumn = 1054
	zdNotAView     = 1347
)

// zdLoad is the suite's load spelled for MySQL and MariaDB, through a pool of its own.
type zdLoad struct {
	// db is what every client query goes through.
	db *sql.DB
	// variant decides how a recursive seed's depth is lifted.
	variant Variant
}

// Seed fills a table with keyed rows whose pad is wide enough that a rewrite of them is slow.
func (l zdLoad) Seed(ctx context.Context, table string, rows int) error {
	conn, err := l.db.Conn(ctx)
	if err != nil {
		return err
	}

	defer func() { _ = conn.Close() }()

	depth := "cte_max_recursion_depth"
	if l.variant == MariaDB {
		depth = "max_recursive_iterations"
	}

	if _, err := conn.ExecContext(ctx, "SET SESSION "+depth+" = "+strconv.Itoa(rows+1)); err != nil {
		return err
	}

	_, err = conn.ExecContext(ctx, "INSERT INTO "+quote(table)+" (id, n, label, pad) WITH RECURSIVE seq(i) AS "+
		"(SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?) SELECT i, i, CONCAT('row ', i), "+
		"REPEAT(MD5(i), 6) FROM seq", rows)

	return err
}

// Read reads one row by its key.
func (l zdLoad) Read(ctx context.Context, table string, key int64) error {
	var n int64

	err := l.db.QueryRowContext(ctx, "SELECT n FROM "+quote(table)+" WHERE id = ?", key).Scan(&n)
	if err == sql.ErrNoRows {
		return nil
	}

	return err
}

// Write updates one row's n by its key.
func (l zdLoad) Write(ctx context.Context, table string, key int64) error {
	_, err := l.db.ExecContext(ctx, "UPDATE "+quote(table)+" SET n = n + 1 WHERE id = ?", key)

	return err
}

// View is a view's body over a table's key and n.
func (zdLoad) View(table string) string {
	return "SELECT id, n FROM " + quote(table)
}

// Raw is a statement that changes nothing.
func (zdLoad) Raw() string {
	return "DO 1"
}

// Drop drops each named view or table where it exists.
func (l zdLoad) Drop(ctx context.Context, tables ...string) error {
	for _, table := range tables {
		if _, err := l.db.ExecContext(ctx, "DROP VIEW IF EXISTS "+quote(table)); err != nil &&
			!hasNumber(err, zdNotAView) {
			return err
		}

		if _, err := l.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+quote(table)); err != nil {
			return err
		}
	}

	return nil
}

// Sum returns the sum of a table's n.
func (l zdLoad) Sum(ctx context.Context, table string) (int64, error) {
	var sum int64

	err := l.db.QueryRowContext(ctx, "SELECT CAST(COALESCE(SUM(n), 0) AS SIGNED) FROM "+quote(table)).Scan(&sum)

	return sum, err
}

// Gone reports a query that named a table or column that is no longer there.
func (zdLoad) Gone(err error) bool {
	return hasNumber(err, zdNoSuchTable) || hasNumber(err, zdNoSuchColumn)
}

func TestEveryOperationKeepsItsLockPromiseUnderLoad(t *testing.T) {
	for _, s := range zdServers {
		t.Run(s.name, func(t *testing.T) {
			dsn := s.dsn
			if override := os.Getenv(s.env); override != "" {
				dsn = override
			}

			suite := zdSuite(t, dsn, s.variant)
			suite.ShadowOnly = s.shadowOnly
			suite.Run(t)
		})
	}
}

// zdSuite builds the suite against one server, with capture where the server permits it.
func zdSuite(t *testing.T, dsn string, variant Variant) zerodowntime.Suite {
	t.Helper()

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(20)
	t.Cleanup(func() { _ = db.Close() })

	d, err := New(driver.Options{})
	if err != nil {
		t.Fatal(err)
	}

	s := zerodowntime.Suite{Driver: d, Load: zdLoad{db: db, variant: variant}, Prefix: "zd",
		Open: func(t *testing.T, historyTable string) driver.Conn { return zdOpen(t, dsn, historyTable) }}

	if zdOpen(t, dsn, "zd_probe").Capabilities().Capture().Permitted {
		s.Capture = func(ctx context.Context, req capture.Request) (capture.Stream, error) {
			return d.Capture(ctx, dsn, req)
		}
	}

	return s
}

// zdOpen opens a session recording its runs in a history table, closed when the test ends.
func zdOpen(t *testing.T, dsn, historyTable string) driver.Conn {
	t.Helper()

	d, err := New(driver.Options{HistoryTable: historyTable, StatementLockWait: zdLockWait})
	if err != nil {
		t.Fatal(err)
	}

	conn, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-zerodowntime`, which brings the servers up first.", err)
	}

	t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

	return conn
}
