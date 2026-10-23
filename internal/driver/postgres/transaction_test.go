package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/emrebdr/migr/internal/driver"
)

// openTx stands in for a transaction this session opened, so the guards can be tested with no server.
type openTx struct {
	pgx.Tx
}

func TestASecondTransactionOnOneSessionIsRefused(t *testing.T) {
	session := &pgConn{tx: openTx{}}

	err := session.Transact(t.Context(), func(context.Context) error {
		t.Error("the work ran inside a session that already had a transaction open")

		return nil
	})

	if !errors.Is(err, ErrTransactionOpen) {
		t.Fatalf("Transact returned %v, want ErrTransactionOpen", err)
	}

	if !errors.Is(err, ErrTransaction) {
		t.Errorf("Transact returned %v, which does not report ErrTransaction", err)
	}

	t.Log("PostgreSQL answers a second BEGIN with a warning and does nothing, so the driver's")
	t.Log("commit would then commit the outer transaction -- somebody else's, whose failure this")
	t.Log("one would silently make permanent. The guard is checked before the connection is used.")
}

func TestTransactingNoWorkAtAllIsRefused(t *testing.T) {
	session := &pgConn{}

	if err := session.Transact(t.Context(), nil); !errors.Is(err, ErrTransaction) {
		t.Fatalf("Transact(nil) returned %v, want a refusal reporting ErrTransaction", err)
	}

	t.Log("Opening a transaction to run nothing and committing it would report a migration applied")
	t.Log("that has no statements, which is the caller's bug and not something to absorb.")
}

func TestAStatementThatCannotBeInATransactionIsRefusedInsideOne(t *testing.T) {
	session := &pgConn{tx: openTx{}}
	stmt := driver.Statement{SQL: "CREATE INDEX CONCURRENTLY i ON t (c)", NoTransaction: true}

	err := session.Exec(t.Context(), stmt)
	if !errors.Is(err, ErrStatementNeedsNoTransaction) {
		t.Fatalf("Exec returned %v, want ErrStatementNeedsNoTransaction", err)
	}

	t.Log("PostgreSQL would refuse this itself, with a message about the statement rather than about")
	t.Log("the caller that put it in the wrong place. The flag exists so the executor splits the")
	t.Log("migration around it (M13-T4); this is the only place that can tell it did not.")
}

func TestAnOrdinaryStatementIsNotRefusedByThatGuard(t *testing.T) {
	session := &pgConn{tx: openTx{}}
	stmt := driver.Statement{SQL: `ALTER TABLE "users" ADD COLUMN "email" text;`}

	if err := session.checkStatementFits(stmt); err != nil {
		t.Fatalf("an ordinary statement was refused inside a transaction: %v", err)
	}
}

func TestAStandaloneStatementIsFineWhenNoTransactionIsOpen(t *testing.T) {
	session := &pgConn{}
	stmt := driver.Statement{SQL: "CREATE INDEX CONCURRENTLY i ON t (c)", NoTransaction: true}

	if err := session.checkStatementFits(stmt); err != nil {
		t.Fatalf("a standalone statement was refused outside any transaction: %v", err)
	}

	t.Log("The refusal is about where the statement runs, not about the statement. Outside a")
	t.Log("transaction is exactly where this one belongs.")
}

func TestAQueryGoesThroughTheTransactionWhenOneIsOpen(t *testing.T) {
	tx := openTx{}
	session := &pgConn{tx: tx}

	if session.runner() != runner(tx) {
		t.Error("a statement would have bypassed the open transaction and run in autocommit")
		t.Log("M3-T6's history store writes through this session, and the contract requires the record")
		t.Log("to be inside the migration's transaction so a crashed run leaves no row at all.")
	}
}

func TestEachStatementIsBoundedByTheWaitTheContractDefaultsTo(t *testing.T) {
	built, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	d, ok := built.(*pgDriver)
	if !ok {
		t.Fatalf("New returned %T, not this package's driver", built)
	}

	if d.statementLockWait != driver.DefaultStatementLockWait {
		t.Errorf("a statement's lock wait defaults to %s, want %s",
			d.statementLockWait, driver.DefaultStatementLockWait)
		t.Log("internal/config applies the same constant to safety.lock_timeout, so an operator")
		t.Log("who configures nothing gets one bound rather than two that can drift apart.")
	}
}

func TestAQueryGoesThroughTheSessionWhenNoTransactionIsOpen(t *testing.T) {
	session := &pgConn{}

	if session.runner() != runner(session.conn) {
		t.Error("a statement outside a transaction did not go through the session itself")
	}
}
