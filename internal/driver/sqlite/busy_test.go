package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// busyWait is safety.lock_timeout for these cases, which busy_timeout spends before SQLITE_BUSY.
const busyWait = 100 * time.Millisecond

// contendedBy opens a database, gives it a table, and has a second connection hold a lock on it.
func contendedBy(t *testing.T, begin string) (*sqliteConn, *sql.Conn) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "app.db")
	c := open(t, path, driver.Options{StatementLockWait: busyWait})
	exec(t, c, `CREATE TABLE "t" ("id" integer PRIMARY KEY)`)
	exec(t, c, `INSERT INTO "t" VALUES (1)`)

	db, err := sql.Open(Name, path)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	t.Cleanup(func() { _ = db.Close() })

	holder, err := db.Conn(t.Context())
	if err != nil {
		t.Fatalf("connecting the holder: %v", err)
	}

	t.Cleanup(func() { _ = holder.Close() })

	for _, sql := range []string{"BEGIN", begin} {
		if _, err := holder.ExecContext(t.Context(), sql); err != nil {
			t.Fatalf("holding the database with %q: %v", sql, err)
		}
	}

	return c, holder
}

// addColumn runs the migration these cases contend over, as the engine runs one.
func addColumn(c *sqliteConn) error {
	return c.Transact(context.Background(), func(ctx context.Context) error {
		return c.Exec(ctx, driver.Statement{SQL: `ALTER TABLE "t" ADD COLUMN "c" integer`})
	})
}

func TestAWaitForTheWriteLockThatSpendsItsBoundIsALockTimeout(t *testing.T) {
	for name, begin := range map[string]string{
		"another writer, refused at the statement": `INSERT INTO "t" VALUES (2)`,
		"a reader, refused at the commit":          `SELECT count(*) FROM "t"`,
	} {
		t.Run(name, func(t *testing.T) {
			c, holder := contendedBy(t, begin)

			err := addColumn(c)
			if !errors.Is(err, driver.ErrStatementLockTimeout) {
				t.Fatalf("Transact returned %v, want a statement lock timeout", err)
			}

			if _, err := holder.ExecContext(t.Context(), "ROLLBACK"); err != nil {
				t.Fatalf("releasing the holder: %v", err)
			}

			if err := addColumn(c); err != nil {
				t.Fatalf("the retry after the holder let go: %v", err)
			}

			got := read(t, c, `SELECT count(*) FROM pragma_table_info('t') WHERE name = 'c'`)
			if len(got.Values) != 1 || got.Values[0][0] == nil || *got.Values[0][0] != "1" {
				t.Errorf("the column is there %v times, want once", got.Values)
			}
		})
	}

	t.Log("Measured at M13-T2: in the default rollback journal another writer makes the statement")
	t.Log("busy, and a reader makes the COMMIT busy. Either way SQLite's own transaction is over")
	t.Log("afterwards -- an explicit ROLLBACK then answers that none is active -- so the session is")
	t.Log("clean and running the whole transaction again is sound, which is what the engine does.")
}
