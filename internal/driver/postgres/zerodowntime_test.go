//go:build zerodowntime

package postgres

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/driver"
	zerodowntime "github.com/emrebdr/migr/test/conformance/zerodowntime"
)

// zdServers are the servers the zero-downtime suite holds this driver to; 17 alone permits capture.
var zdServers = []struct{ name, env, dsn string }{
	{"postgres16", "MIGR_TEST_PG16_DSN", "postgres://migr:migr@127.0.0.1:55416/migr?sslmode=disable"},
	{"postgres17", "MIGR_TEST_PG17_DSN", "postgres://migr:migr@127.0.0.1:55417/migr?sslmode=disable"},
}

// zdLoad is the suite's load spelled for PostgreSQL, through connections of its own as an application's would be.
type zdLoad struct {
	// pool is what every client query goes through.
	pool *zdPool
}

// zdPool is a fixed set of connections, each lent to one query at a time.
type zdPool struct {
	// free holds every connection not lent out.
	free chan *pgx.Conn
}

// zdNewPool connects size connections, closed when the test ends.
func zdNewPool(t *testing.T, dsn string, size int) *zdPool {
	t.Helper()

	p := &zdPool{free: make(chan *pgx.Conn, size)}

	for range size {
		conn, err := pgx.Connect(t.Context(), dsn)
		if err != nil {
			t.Fatalf("connecting the load: %v", err)
		}

		t.Cleanup(func() { _ = conn.Close(context.WithoutCancel(t.Context())) })
		p.free <- conn
	}

	return p
}

// with lends one connection to work and takes it back.
func (p *zdPool) with(ctx context.Context, work func(*pgx.Conn) error) error {
	var conn *pgx.Conn

	select {
	case conn = <-p.free:
	case <-ctx.Done():
		return ctx.Err()
	}

	defer func() { p.free <- conn }()

	return work(conn)
}

// exec runs one statement on a lent connection.
func (p *zdPool) exec(ctx context.Context, sql string, args ...any) error {
	return p.with(ctx, func(conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, sql, args...)

		return err
	})
}

// Seed fills a table with keyed rows whose pad is wide enough that a rewrite of them is slow.
func (l zdLoad) Seed(ctx context.Context, table string, rows int) error {
	return l.pool.exec(ctx, "INSERT INTO "+quote(table)+" (id, n, label, pad) SELECT i, i, 'row ' || i, "+
		"repeat(md5(i::text), 6) FROM generate_series(1, $1::int) AS i", rows)
}

// Read reads one row by its key.
func (l zdLoad) Read(ctx context.Context, table string, key int64) error {
	return l.pool.with(ctx, func(conn *pgx.Conn) error {
		var n int64

		err := conn.QueryRow(ctx, "SELECT n FROM "+quote(table)+" WHERE id = $1", key).Scan(&n)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}

		return err
	})
}

// Write updates one row's n by its key.
func (l zdLoad) Write(ctx context.Context, table string, key int64) error {
	return l.pool.exec(ctx, "UPDATE "+quote(table)+" SET n = n + 1 WHERE id = $1", key)
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
		if err := l.pool.exec(ctx, "DROP VIEW IF EXISTS "+quote(table)+" CASCADE"); err != nil &&
			!zdHasCode(err, "42809") {
			return err
		}

		if err := l.pool.exec(ctx, "DROP TABLE IF EXISTS "+quote(table)+" CASCADE"); err != nil {
			return err
		}
	}

	return nil
}

// Sum returns the sum of a table's n.
func (l zdLoad) Sum(ctx context.Context, table string) (int64, error) {
	var sum int64

	err := l.pool.with(ctx, func(conn *pgx.Conn) error {
		return conn.QueryRow(ctx, "SELECT coalesce(sum(n), 0)::bigint FROM "+quote(table)).Scan(&sum)
	})

	return sum, err
}

// Gone reports a query that named a table or column that is no longer there.
func (zdLoad) Gone(err error) bool {
	return zdHasCode(err, "42P01") || zdHasCode(err, "42703")
}

// zdHasCode reports a server error carrying one SQLSTATE.
func zdHasCode(err error, code string) bool {
	var server *pgconn.PgError

	return errors.As(err, &server) && server.Code == code
}

func TestEveryOperationKeepsItsLockPromiseUnderLoad(t *testing.T) {
	for _, s := range zdServers {
		t.Run(s.name, func(t *testing.T) {
			dsn := s.dsn
			if override := os.Getenv(s.env); override != "" {
				dsn = override
			}

			zdSuite(t, dsn).Run(t)
		})
	}
}

// zdSuite builds the suite against one server, with capture where the server permits it.
func zdSuite(t *testing.T, dsn string) zerodowntime.Suite {
	t.Helper()

	pool := zdNewPool(t, dsn, 16)

	d, err := New(driver.Options{})
	if err != nil {
		t.Fatal(err)
	}

	s := zerodowntime.Suite{Driver: d, Load: zdLoad{pool: pool}, Prefix: "zd",
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

	wait, err := zerodowntime.LockWait()
	if err != nil {
		t.Fatal(err)
	}

	d, err := New(driver.Options{HistoryTable: historyTable, StatementLockWait: wait})
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
