package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// memory is the connection string for a database that lives in the connection that opened it.
const memory = ":memory:"

// open connects to a database and gives the session back at the end of the test.
func open(t *testing.T, dsn string, opts driver.Options) *sqliteConn {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open(%q): %v", dsn, err)
	}

	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	session, ok := conn.(*sqliteConn)
	if !ok {
		t.Fatalf("Open returned %T, want a SQLite session", conn)
	}

	return session
}

// openMemory connects to an in-memory database, which belongs to its one connection.
func openMemory(t *testing.T) *sqliteConn {
	t.Helper()

	return open(t, memory, driver.Options{})
}

// openFile connects to a database file in a directory this test owns.
func openFile(t *testing.T) *sqliteConn {
	t.Helper()

	return open(t, filepath.Join(t.TempDir(), "app.db"), driver.Options{})
}

// exec runs one statement on a session, failing the test rather than the driver.
func exec(t *testing.T, c *sqliteConn, statement string) {
	t.Helper()

	if err := c.Exec(t.Context(), driver.Statement{SQL: statement}); err != nil {
		t.Fatalf("Exec(%s): %v", statement, err)
	}
}

// pragma reads one session setting back, failing the test rather than returning a reason.
func pragma(t *testing.T, c *sqliteConn, name string) string {
	t.Helper()

	value, err := readPragma(t.Context(), c, name)
	if err != nil {
		t.Fatalf("reading PRAGMA %s: %v", name, err)
	}

	return value
}

func TestAFileDatabaseAndAnInMemoryOneBothOpen(t *testing.T) {
	for name, session := range map[string]func(*testing.T) *sqliteConn{
		"file":   openFile,
		"memory": openMemory,
	} {
		t.Run(name, func(t *testing.T) {
			c := session(t)

			exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

			if got := c.ServerVersion(); got == "" {
				t.Error("the session reports no version, and one round trip was the health check")
			}
		})
	}
}

func TestTheSessionIsOnePinnedConnectionAndNotAPool(t *testing.T) {
	c := openMemory(t)

	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

	rows, err := c.Query(t.Context(), driver.Query{
		SQL: `SELECT count(*) AS n FROM sqlite_master WHERE name = 'users'`,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}

	if got := value(t, rows); got != "1" {
		t.Errorf("the table created on this session is found %s times, want 1", got)
		t.Log("An in-memory database belongs to the connection that opened it, so a pooled session")
		t.Log("would answer from a second, empty database -- and report success while doing it.")
		t.Log("That is why Open pins one *sql.Conn for the session's life (ADR 0050).")
	}
}

func TestNoSecondConnectionToTheDatabaseIsAvailableWhileASessionHoldsOne(t *testing.T) {
	c := openMemory(t)

	ctx, stop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer stop()

	second, err := c.db.Conn(ctx)
	if err == nil {
		t.Error("a second connection was handed out while the session holds one")
		t.Log("The pool is capped at one so nothing can reach this database except through the")
		t.Log("pinned connection -- which for :memory: would be a different database, and for the")
		t.Log("three pragmas above would be a session carrying SQLite's defaults (ADR 0050).")

		_ = second.Close()
	}
}

func TestTheThreePragmasThisDriverOwnsReadBackAsSet(t *testing.T) {
	c := open(t, memory, driver.Options{StatementLockWait: 2500 * time.Millisecond})

	for name, want := range map[string]string{
		pragmaForeignKeys: pragmaOn,
		pragmaBusyTimeout: "2500",
		pragmaQueryOnly:   pragmaOff,
	} {
		if got := pragma(t, c, name); got != want {
			t.Errorf("PRAGMA %s reads back as %q, want %q", name, got, want)
		}
	}

	t.Log("foreign_keys defaults to 0 for backwards compatibility, so a migration that breaks")
	t.Log("referential integrity would otherwise succeed silently; busy_timeout defaults to 0,")
	t.Log("which is invariant #12's unbounded wait. Both measured on SQLite 3.53.")
}

func TestAPragmaThatDoesNotTakeIsAFailureRatherThanASurprise(t *testing.T) {
	c := openMemory(t)

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		return pinPragma(ctx, c, pragmaForeignKeys, pragmaOff)
	}); !errors.Is(err, ErrPragma) {
		t.Fatalf("setting foreign_keys inside a transaction returned %v, want %v", err, ErrPragma)
	}

	if got := pragma(t, c, pragmaForeignKeys); got != pragmaOn {
		t.Errorf("foreign_keys is %q after the refusal, want %q", got, pragmaOn)
	}

	t.Log("SQLite reports success for PRAGMA foreign_keys inside a transaction and ignores it.")
	t.Log("That is the whole reason every pragma here is read back rather than assumed -- and it")
	t.Log("is why M8-T4's rebuild must use defer_foreign_keys, which does take inside one.")
}

func TestClosingTheSessionTwiceIsNotAFailure(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), memory)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	if err := conn.Close(t.Context()); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if err := conn.Close(t.Context()); err != nil {
		t.Errorf("closing a second time returned %v", err)
		t.Log("cli's close path is deferred and joined, and pgx's Close is idempotent, so a driver")
		t.Log("that reported sql.ErrConnDone would make one command's exit code depend on which")
		t.Log("engine it ran against.")
	}
}

func TestAnUnusableDatabaseIsRefusedAtOpen(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), filepath.Join(t.TempDir(), "missing", "app.db"))
	if err == nil {
		t.Fatal("Open reported success for a path whose directory does not exist")
	}

	if conn != nil {
		t.Error("Open returned a session as well as an error")
	}

	if !errors.Is(err, ErrConnect) {
		t.Errorf("Open returned %v, want it to wrap %v", err, ErrConnect)
	}
}

func TestTheVersionIsDetectedRatherThanAssumed(t *testing.T) {
	c := openMemory(t)

	reported := pragma(t, c, "user_version")
	if reported != "0" {
		t.Fatalf("a fresh database reports user_version %q", reported)
	}

	var asked string
	if err := c.conn.QueryRowContext(t.Context(), versionQuery).Scan(&asked); err != nil {
		t.Fatalf("asking for the version: %v", err)
	}

	if got := c.ServerVersion(); got != asked {
		t.Errorf("the session reports %q and the library reports %q", got, asked)
	}

	if !c.version.AtLeast(minimumMajor, minimumMinor) {
		t.Errorf("the linked library is %s, older than this driver supports", c.version)
	}

	t.Log("The version names the SQLite this binary links, not the file it opened -- modernc.org/")
	t.Log("sqlite is a transpiled library -- so it is a fact about the build. Invariant #13 still")
	t.Log("holds the way it reads: it is asked for, never hardcoded (ADR 0050).")
}
