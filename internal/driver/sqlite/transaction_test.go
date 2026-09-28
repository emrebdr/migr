package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

// errWork is what a test's work returns when it wants the transaction discarded.
var errWork = errors.New("the work failed")

func TestATransactionCommitsOnlyWhenTheWorkReturnedNothing(t *testing.T) {
	c := openMemory(t)
	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		return c.Exec(ctx, driver.Statement{SQL: `INSERT INTO users (id) VALUES (1)`})
	}); err != nil {
		t.Fatalf("Transact: %v", err)
	}

	if got := value(t, read(t, c, `SELECT count(*) AS n FROM users`)); got != "1" {
		t.Fatalf("the committed row is not there: %s rows", got)
	}

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		if err := c.Exec(ctx, driver.Statement{SQL: `INSERT INTO users (id) VALUES (2)`}); err != nil {
			return err
		}

		return errWork
	})

	if !errors.Is(err, errWork) {
		t.Fatalf("Transact returned %v, want the work's own error", err)
	}

	if got := value(t, read(t, c, `SELECT count(*) AS n FROM users`)); got != "1" {
		t.Errorf("the discarded row survived: %s rows", got)
		t.Log("SQLite's DDL and its data share one transaction, which is what the capability set")
		t.Log("declares as TransactionalDDL and what M8-T6's round trip proves against a file.")
	}
}

// unmade is never made, so writing to it faults the way an ordinary bug in a migration would.
//
// The fault has to come from a real one: forbidigo forbids calling panic anywhere outside cmd/ and
// internal/cli/, test files included, and rightly -- a rollback path that only survives a panic this
// file raised on purpose would prove less than one that survives the kind that actually happens.
var unmade map[string]string

// faultPartway runs a migration that faults after its first statement, returning what the fault was.
func faultPartway(t *testing.T, c *sqliteConn) (fault any) {
	t.Helper()

	defer func() { fault = recover() }()

	_ = c.Transact(t.Context(), func(ctx context.Context) error {
		if err := c.Exec(ctx, driver.Statement{SQL: `INSERT INTO users (id) VALUES (1)`}); err != nil {
			return err
		}

		unmade["users"] = "inserted just above"

		return nil
	})

	return fault
}

func TestAMigrationThatFaultsPartwayIsDiscardedRatherThanCommitted(t *testing.T) {
	c := openMemory(t)
	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

	if fault := faultPartway(t, c); fault == nil {
		t.Fatal("the work returned instead of faulting, so the rollback path this test is about " +
			"was never taken and the assertions below would pass for the wrong reason")
	}

	if got := value(t, read(t, c, `SELECT count(*) AS n FROM users`)); got != "0" {
		t.Errorf("a faulted migration left %s rows behind, want none", got)
		t.Log("A closure that never returns leaves the deferred end with no error to see, so the")
		t.Log("commit decision rests on whether the work returned at all rather than on its error.")
	}

	if c.tx != nil {
		t.Error("the session still holds a transaction after the fault")
	}

	if err := c.Exec(t.Context(), driver.Statement{SQL: `INSERT INTO users (id) VALUES (2)`}); err != nil {
		t.Errorf("the session did not survive the fault: %v", err)
	}
}

func TestASecondTransactionOnOneSessionIsRefused(t *testing.T) {
	c := openMemory(t)

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		return c.Transact(ctx, func(context.Context) error { return nil })
	})

	if !errors.Is(err, ErrTransactionOpen) {
		t.Fatalf("a nested transaction returned %v, want %v", err, ErrTransactionOpen)
	}

	if err := c.Transact(t.Context(), func(context.Context) error { return nil }); err != nil {
		t.Errorf("the session is unusable after the refusal: %v", err)
	}
}

func TestTransactWithNoWorkIsRefusedBeforeAnythingBegins(t *testing.T) {
	c := openMemory(t)

	if err := c.Transact(t.Context(), nil); !errors.Is(err, ErrTransaction) {
		t.Fatalf("Transact(nil) returned %v, want %v", err, ErrTransaction)
	}

	if c.tx != nil {
		t.Error("a refused transaction left one open")
	}
}

func TestAStatementThatCannotRunInATransactionIsRefusedInsideOne(t *testing.T) {
	c := openMemory(t)

	err := c.Transact(t.Context(), func(ctx context.Context) error {
		return c.Exec(ctx, driver.Statement{SQL: `SELECT 1`, NoTransaction: true})
	})

	if !errors.Is(err, ErrStatementNeedsNoTransaction) {
		t.Fatalf("Exec returned %v, want %v", err, ErrStatementNeedsNoTransaction)
	}

	if err := c.Exec(t.Context(), driver.Statement{SQL: `SELECT 1`, NoTransaction: true}); err != nil {
		t.Errorf("the same statement outside a transaction: %v", err)
	}
}

func TestEveryStatementInATransactionGoesThroughIt(t *testing.T) {
	c := openMemory(t)
	exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)

	if err := c.Transact(t.Context(), func(ctx context.Context) error {
		if c.runner() != c.tx {
			t.Error("the session runs statements outside the transaction it opened")
		}

		return c.Exec(ctx, driver.Statement{SQL: `INSERT INTO users (id) VALUES (1)`})
	}); err != nil {
		t.Fatalf("Transact: %v", err)
	}

	if c.runner() != c.conn {
		t.Error("the session still routes through a transaction that has ended")
	}
}
