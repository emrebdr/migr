//go:build integration

package mysql

import (
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestAValueTheServerClampedIsReportedRatherThanBelieved(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		err := pinNumber(t.Context(), c, varLockWait, "99999999999")
		if !errors.Is(err, ErrVariable) {
			t.Fatalf("pinning a wait past what MySQL keeps returned %v, want %v", err, ErrVariable)
		}

		for _, part := range []string{varLockWait, "99999999999", "31536000"} {
			if !strings.Contains(err.Error(), part) {
				t.Errorf("the refusal does not carry %q: %v", part, err)
			}
		}

		if err := pinNumber(t.Context(), c, varLockWait, "3"); err != nil {
			t.Fatalf("putting the wait back: %v", err)
		}
	})
}

func TestAValueWrittenIntoAStatementIsCheckedBeforeTheServerSeesIt(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		err := pinNumber(t.Context(), c, varLockWait, "3, @x = (SELECT count(*) FROM "+quote(table)+")")
		if !errors.Is(err, ErrConnect) {
			t.Fatalf("a value that is not a number returned %v, want %v", err, ErrConnect)
		}

		if got := scalar(t, c, "SELECT @@session."+varLockWait); got != "3" {
			t.Errorf("%s is %q, so something reached the server", varLockWait, got)
		}

		t.Log("A numeric system variable takes no bound parameter, so the digits are written into the")
		t.Log("statement. checkDigits is what stops that being one statement's worth of trust in")
		t.Log("whoever called it, and this is the test that makes it load-bearing rather than tidy.")
	})
}

func TestAConnectionThatWouldTranscodeValuesIsRefusedAtOpen(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			d, err := New(driver.Options{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			_, err = d.Open(t.Context(), s.dsnFor()+"?charset=latin1")
			if !errors.Is(err, ErrCharacterSet) {
				t.Fatalf("Open over a latin1 connection returned %v, want %v", err, ErrCharacterSet)
			}

			if !strings.Contains(err.Error(), "latin1") {
				t.Errorf("the refusal does not name what the connection had: %v", err)
			}

			t.Log("charset is the operator's own parameter and is not one this driver owns, so it is")
			t.Log("not refused as a parameter -- the connection it produces is checked instead, and")
			t.Log("the refusal names the value that fixes it.")
		})
	}
}

func TestTheReadOnlyTransactionRefusesAWriteOnItsOwn(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		table := seeded(t, c)

		if got := scalar(t, c, "SELECT @@session."+varReadOnly); got != variableOff {
			t.Fatalf("the session already refuses writes, so this test proves nothing: %s", got)
		}

		tx, err := c.conn.BeginTx(t.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatalf("BeginTx: %v", err)
		}

		_, err = tx.ExecContext(t.Context(), "INSERT INTO "+quote(table)+" VALUES (9, 9)")

		if rollback := tx.Rollback(); rollback != nil {
			t.Errorf("Rollback: %v", rollback)
		}

		if !hasNumber(err, readOnlyRefused) {
			t.Fatalf("an INSERT in a read-only transaction returned %v, want error %d", err,
				readOnlyRefused)
		}

		t.Log("Query pins transaction_read_only as well as opening this, and the session variable is")
		t.Log("the half that stops DDL -- the transaction alone lets CREATE TABLE through, which is")
		t.Log("what the read-only test in query_integration_test.go drives. This is the other half on")
		t.Log("its own: with the session variable off, the transaction still refuses a write. Neither")
		t.Log("is decoration, and removing either leaves a hole the other does not cover.")
	})
}

func TestTheCatalogAnswersAboutTheTableThatWasAskedAbout(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		held := store(t, c)

		folded := strings.ToLower(held.table)
		if folded == held.table {
			t.Skipf("the history table %q is already lowercase, so there is no second spelling",
				held.table)
		}

		if err := held.Ensure(t.Context()); err != nil {
			t.Fatalf("Ensure: %v", err)
		}

		dropLater(t, c, folded)
		drop(t.Context(), c, folded)
		exec(t, c, "CREATE VIEW "+quote(folded)+" AS SELECT 1 AS batch")

		kind, found, err := held.kind(t.Context())
		if err != nil {
			t.Fatalf("looking for the table: %v", err)
		}

		if !found || kind != ordinaryTable {
			t.Errorf("the lookup reported %q (found %v) with a view holding the folded spelling",
				kind, found)
		}

		t.Log("information_schema.TABLES.TABLE_NAME is utf8mb3_bin on 8.0.46 and utf8mb3_general_ci")
		t.Log("on 11.4.13 -- measured -- so what the comparison resolves to is not this driver's to")
		t.Log("choose. Comparing the stored name in Go is what makes the answer independent of it.")
		t.Log("Neither container reproduces a folding comparison, so this documents the hazard")
		t.Log("rather than reproducing it: a case-insensitive match here would answer about the view.")
	})
}
