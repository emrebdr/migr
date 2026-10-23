//go:build zerodowntime

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	zerodowntime "github.com/emrebdr/migr/test/conformance/zerodowntime"
)

// zdPragmas are how an application runs SQLite under concurrent clients: WAL, so readers never wait on a
// writer, and a long busy_timeout, so a client waits on a lock rather than failing. Measured, eight writers
// in the default rollback journal waited 2.0s on each other with nothing else running.
const zdPragmas = "?_pragma=busy_timeout(60000)&_pragma=journal_mode(WAL)"

// zdLoad is the suite's load spelled for SQLite: readers through a pool, writers through one connection.
type zdLoad struct {
	// db is what every read goes through.
	db *sql.DB
	// writer is the one connection every write goes through, as SQLite serializes writers anyway.
	writer *sql.DB
}

// Seed fills a table with keyed rows whose pad is wide enough that a rebuild of them is slow.
func (l zdLoad) Seed(ctx context.Context, table string, rows int) error {
	_, err := l.db.ExecContext(ctx, "INSERT INTO "+quote(table)+" (id, n, label, pad) WITH RECURSIVE "+
		"seq(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM seq WHERE i < ?) SELECT i, i, 'row ' || i, "+
		"printf('%.192c', 'x') FROM seq", rows)

	return err
}

// Read reads one row by its key.
func (l zdLoad) Read(ctx context.Context, table string, key int64) error {
	var n int64

	err := l.db.QueryRowContext(ctx, "SELECT n FROM "+quote(table)+" WHERE id = ?", key).Scan(&n)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}

	return err
}

// Write updates one row's n by its key.
func (l zdLoad) Write(ctx context.Context, table string, key int64) error {
	_, err := l.writer.ExecContext(ctx, "UPDATE "+quote(table)+" SET n = n + 1 WHERE id = ?", key)

	return err
}

// View is a view's body over a table's key and n.
func (zdLoad) View(table string) string {
	return "SELECT id, n FROM " + quote(table)
}

// Raw is a statement that changes nothing.
func (zdLoad) Raw() string {
	return "SELECT 1"
}

// Drop drops each named view or table where it exists.
func (l zdLoad) Drop(ctx context.Context, tables ...string) error {
	for _, table := range tables {
		for _, kind := range []string{"VIEW", "TABLE"} {
			if _, err := l.db.ExecContext(ctx, "DROP "+kind+" IF EXISTS "+quote(table)); err != nil &&
				!strings.Contains(err.Error(), "use DROP") {
				return err
			}
		}
	}

	return nil
}

// Sum returns the sum of a table's n.
func (l zdLoad) Sum(ctx context.Context, table string) (int64, error) {
	var sum int64

	err := l.db.QueryRowContext(ctx, "SELECT CAST(COALESCE(SUM(n), 0) AS INTEGER) FROM "+quote(table)).Scan(&sum)

	return sum, err
}

// Gone reports a query that named a table or column that is no longer there.
func (zdLoad) Gone(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no such table") || strings.Contains(err.Error(), "no such column"))
}

func TestEveryOperationKeepsItsLockPromiseUnderLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.db")

	db, err := sql.Open(library, path+zdPragmas)
	if err != nil {
		t.Fatal(err)
	}

	db.SetMaxIdleConns(20)
	t.Cleanup(func() { _ = db.Close() })

	writer, err := sql.Open(library, path+zdPragmas)
	if err != nil {
		t.Fatal(err)
	}

	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = writer.Close() })

	d, err := New(driver.Options{})
	if err != nil {
		t.Fatal(err)
	}

	zerodowntime.Suite{Driver: d, Load: zdLoad{db: db, writer: writer}, Prefix: "zd",
		Open: func(t *testing.T, historyTable string) driver.Conn {
			t.Helper()

			opened, err := New(driver.Options{HistoryTable: historyTable})
			if err != nil {
				t.Fatal(err)
			}

			conn, err := opened.Open(t.Context(), path)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}

			t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })

			return conn
		}}.Run(t)
}
