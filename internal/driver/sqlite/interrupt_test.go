package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// endless is a write that would run for minutes, so only an interrupt can end it in a test.
const endless = `CREATE TABLE "big" AS WITH RECURSIVE "c"("x") AS (SELECT 1 UNION ALL ` +
	`SELECT "x" + 1 FROM "c") SELECT "x" FROM "c" LIMIT 1000000000`

func TestAStatementWhoseContextEndsIsInterruptedAndTheSessionCarriesOn(t *testing.T) {
	c := openFile(t)

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()

	stopped := make(chan error, 1)

	go func() {
		stopped <- c.Transact(ctx, func(ctx context.Context) error {
			return c.Exec(ctx, driver.Statement{SQL: endless})
		})
	}()

	var err error

	select {
	case err = <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the statement was still running 10s after its context ended")
	}

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Transact returned %v, want the interrupted statement's failure", err)
	}

	exec(t, c, `CREATE TABLE "after" ("id" integer)`)

	if got := read(t, c, `SELECT count(*) FROM sqlite_schema WHERE name = 'big'`); *got.Values[0][0] != "0" {
		t.Errorf("the interrupted CREATE TABLE ... AS left its table behind")
	}

	t.Logf("the interrupted transaction reported: %v", err)
	t.Log("SQLite is in this process, so the database/sql cancel reaches sqlite3_interrupt and the")
	t.Log("statement stops inside the engine itself: there is no server to tell. The session is")
	t.Log("then idle and usable, and the rollback took what the statement had written.")
}
