//go:build integration

package mysql

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestATransactionCommitsWhenTheWorkReturnsNilAndDiscardsWhenItDoesNot(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		if err := c.Transact(t.Context(), func(ctx context.Context) error {
			return c.Exec(ctx, driver.Statement{
				SQL: "INSERT INTO " + quote(table) + " VALUES (3, 3)",
			})
		}); err != nil {
			t.Fatalf("Transact: %v", err)
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "3" {
			t.Errorf("after a committed transaction the table holds %s rows, want 3", got)
		}

		stop := errors.New("the migration failed")

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			if err := c.Exec(ctx, driver.Statement{
				SQL: "INSERT INTO " + quote(table) + " VALUES (4, 4)",
			}); err != nil {
				return err
			}

			return stop
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Transact returned %v, want %v", err, stop)
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "3" {
			t.Errorf("after a discarded transaction the table holds %s rows, want 3", got)
		}
	})
}

func TestASecondTransactionOnOneSessionIsRefused(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		err := c.Transact(t.Context(), func(ctx context.Context) error {
			return c.Transact(ctx, func(context.Context) error { return nil })
		})

		if !errors.Is(err, ErrTransactionOpen) {
			t.Fatalf("a nested Transact returned %v, want %v", err, ErrTransactionOpen)
		}
	})
}

// unmade is never made, so writing to it faults the way an ordinary bug in a migration would.
//
// The fault has to come from a real one: forbidigo forbids calling panic anywhere outside cmd/ and
// internal/cli/, test files included, and rightly -- a rollback path that only survives a panic this
// file raised on purpose would prove less than one that survives the kind that actually happens.
var unmade map[string]string

// faultPartway runs a migration that faults after its first statement, returning what the fault was.
func faultPartway(t *testing.T, c *mysqlConn, table string) (fault any) {
	t.Helper()

	defer func() { fault = recover() }()

	_ = c.Transact(t.Context(), func(ctx context.Context) error {
		if err := c.Exec(ctx, driver.Statement{
			SQL: "INSERT INTO " + quote(table) + " VALUES (5, 5)",
		}); err != nil {
			return err
		}

		unmade[table] = "inserted just above"

		return nil
	})

	return fault
}

func TestAMigrationThatFaultsPartwayIsDiscardedRatherThanCommitted(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		if fault := faultPartway(t, c, table); fault == nil {
			t.Fatal("the work returned instead of faulting, so the rollback path this test is about " +
				"was never taken and the assertion below would pass for the wrong reason")
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "2" {
			t.Errorf("a faulted migration left %s rows behind, want 2", got)
		}

		if c.tx != nil {
			t.Error("the session still holds a transaction after the fault")
		}
	})
}

func TestDDLCommitsThroughATransactionThisDriverOpened(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)
		created := uniqueName(t) + "_ddl"

		dropLater(t, c, created)

		stop := errors.New("the migration failed")

		err := c.Transact(t.Context(), func(ctx context.Context) error {
			if err := c.Exec(ctx, driver.Statement{
				SQL: "INSERT INTO " + quote(table) + " VALUES (6, 6)",
			}); err != nil {
				return err
			}

			if err := c.Exec(ctx, driver.Statement{
				SQL: "CREATE TABLE " + quote(created) + " (a int)",
			}); err != nil {
				return err
			}

			return stop
		})
		if !errors.Is(err, stop) {
			t.Fatalf("Transact returned %v, want %v", err, stop)
		}

		if got := existing(t, c, created); got != "1" {
			t.Errorf("the table created inside the discarded transaction is gone, and MySQL has no "+
				"transactional DDL: found %s", got)
		}

		if got := scalar(t, c, "SELECT count(*) FROM "+quote(table)); got != "3" {
			t.Errorf("the row inserted before the DDL was rolled back, and the DDL committed it: "+
				"the table holds %s rows, want 3", got)
		}

		if c.Capabilities().TransactionalDDL() {
			t.Error("this session declares transactional DDL")
		}

		t.Log("This is M9's premise, shown rather than cited. The rollback reports success, the")
		t.Log("table it was meant to undo is still there, and the row inserted before it -- which")
		t.Log("the caller would expect to be discarded -- was committed by the DDL that followed.")
		t.Log("M9-T5 is where the engine's answer to it is built; declaring it here is what makes")
		t.Log("host.info and engine.DirtyError correct from this commit.")
	})
}

// existing reports how many tables of one name this database has, which is one or zero.
func existing(t *testing.T, c *mysqlConn, table string) string {
	t.Helper()

	return scalar(t, c, "SELECT count(*) FROM information_schema.TABLES "+
		"WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME = ?", table)
}

func TestAStatementThatCannotRunInATransactionIsRefusedInsideOne(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		err := c.Transact(t.Context(), func(ctx context.Context) error {
			return c.Exec(ctx, driver.Statement{SQL: "SELECT 1", NoTransaction: true})
		})

		if !errors.Is(err, ErrStatementNeedsNoTransaction) {
			t.Fatalf("Exec returned %v, want %v", err, ErrStatementNeedsNoTransaction)
		}
	})
}

func TestTransactWithNoWorkIsARefusalRatherThanAnEmptyCommit(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		if err := c.Transact(t.Context(), nil); !errors.Is(err, ErrTransaction) {
			t.Fatalf("Transact(nil) returned %v, want %v", err, ErrTransaction)
		}
	})
}
