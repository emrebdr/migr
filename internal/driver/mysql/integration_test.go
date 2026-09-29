//go:build integration

package mysql

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// server is one engine this driver is held to, and the variables that override its connection string.
type server struct {
	name    string
	env     string
	dsn     string
	variant Variant
	major   int
	minor   int
}

// servers are the two engines this driver must detect apart as well as speak to.
//
// MariaDB is here for the reason two PostgreSQL majors are: a driver that detects a variant and only
// ever meets one of them has not had that tested at all. The DSNs match test/docker-compose.yml.
var servers = []server{
	{
		name: "mysql80", env: "MIGR_TEST_MYSQL80_DSN",
		dsn:     "migr:migr@tcp(127.0.0.1:55480)/migr",
		variant: MySQL, major: 8, minor: 0,
	},
	{
		name: "mariadb114", env: "MIGR_TEST_MARIADB114_DSN",
		dsn:     "migr:migr@tcp(127.0.0.1:55411)/migr",
		variant: MariaDB, major: 11, minor: 4,
	},
}

// dsnFor returns the connection string for a server, preferring the environment override.
func (s server) dsnFor() string {
	if override := os.Getenv(s.env); override != "" {
		return override
	}

	return s.dsn
}

// eachServer runs one case against every engine, so a difference between them is a named subtest.
func eachServer(t *testing.T, run func(t *testing.T, s server, c *mysqlConn)) {
	t.Helper()

	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			run(t, s, openServer(t, s, driver.Options{HistoryTable: uniqueName(t)}))
		})
	}
}

// openServer connects to one engine, failing rather than skipping when it is not there.
func openServer(t *testing.T, s server, opts driver.Options) *mysqlConn {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), s.dsnFor())
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-integration`, which brings the servers up first.", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	session, ok := conn.(*mysqlConn)
	if !ok {
		t.Fatalf("Open returned %T", conn)
	}

	return session
}

// uniqueName derives a table name from the running test, since every case shares one database.
func uniqueName(t *testing.T) string {
	t.Helper()

	safe := strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}

		return '_'
	}, t.Name())

	if len(safe) > identifierLimit-len("_version_idx") {
		safe = safe[len(safe)-(identifierLimit-len("_version_idx")):]
	}

	return safe
}

// exec runs a statement this test wrote itself, failing the test rather than returning.
func exec(t *testing.T, c *mysqlConn, sql string) {
	t.Helper()

	if err := c.Exec(t.Context(), driver.Statement{SQL: sql}); err != nil {
		t.Fatalf("running %q: %v", sql, err)
	}
}

// dropLater removes what a test created, on a context that outlives whatever ended the test.
func dropLater(t *testing.T, c *mysqlConn, objects ...string) {
	t.Helper()

	t.Cleanup(func() {
		drop(context.WithoutCancel(t.Context()), c, objects...)
	})
}

// drop removes a name whatever holds it, since a case that left a view behind would wedge the next.
func drop(ctx context.Context, c *mysqlConn, objects ...string) {
	for _, object := range objects {
		_ = c.Exec(ctx, driver.Statement{SQL: "DROP VIEW IF EXISTS " + quote(object)})
		_ = c.Exec(ctx, driver.Statement{SQL: "DROP TABLE IF EXISTS " + quote(object)})
	}
}

// read runs a query on the session directly, outside the read-only path Query takes.
func read(t *testing.T, c *mysqlConn, sql string, args ...any) []string {
	t.Helper()

	rows, err := c.runner().QueryContext(t.Context(), sql, args...)
	if err != nil {
		t.Fatalf("reading %q: %v", sql, err)
	}

	defer rows.Close()

	var out []string

	for rows.Next() {
		var value *string

		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scanning %q: %v", sql, err)
		}

		out = append(out, recordedText(value))
	}

	if err := rows.Err(); err != nil {
		t.Fatalf("reading %q: %v", sql, err)
	}

	return out
}

// scalar reads a single value, which is what most of these assertions compare against.
func scalar(t *testing.T, c *mysqlConn, sql string, args ...any) string {
	t.Helper()

	values := read(t, c, sql, args...)
	if len(values) != 1 {
		t.Fatalf("%q returned %d rows, want 1", sql, len(values))
	}

	return values[0]
}
