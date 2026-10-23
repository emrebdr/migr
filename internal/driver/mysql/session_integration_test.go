//go:build integration

package mysql

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

func TestTheVariantAndTheReleaseAreDetectedFromTheServerItself(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		if c.version.Variant() != s.variant {
			t.Errorf("detected %s, want %s", c.version.Variant(), s.variant)
		}

		if !c.version.AtLeast(s.variant, s.major, s.minor) {
			t.Errorf("detected %s, want at least %s %d.%d", c.version, s.variant, s.major, s.minor)
		}

		if !strings.HasPrefix(c.ServerVersion(), s.variant.String()) {
			t.Errorf("ServerVersion is %q and does not begin with the variant", c.ServerVersion())
		}

		t.Logf("%s reports itself as %q", s.name, scalar(t, c, "SELECT VERSION()"))
	})
}

func TestEverySessionVariableThisDriverOwnsReadsBackAsWhatItWasSetTo(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		if !hasMode(scalar(t, c, "SELECT @@session."+varSQLMode), noBackslashEscapes) {
			t.Errorf("sql_mode is %q and does not carry %s",
				scalar(t, c, "SELECT @@session."+varSQLMode), noBackslashEscapes)
		}

		for name, want := range map[string]string{
			varTimeZone: utcOffset,
			varReadOnly: variableOff,
		} {
			if got := scalar(t, c, "SELECT @@session."+name); got != want {
				t.Errorf("%s is %q, want %q", name, got, want)
			}
		}

		_, statement := driver.Options{}.Waits()

		for _, name := range []string{varLockWait, varRowLockWait} {
			if got := scalar(t, c, "SELECT @@session."+name); got != waitSeconds(statement) {
				t.Errorf("%s is %q, want %q", name, got, waitSeconds(statement))
			}
		}

		if got := scalar(t, c, "SELECT @@session."+varCharacterSet); got != requiredCharacterSet {
			t.Errorf("%s is %q, want %q", varCharacterSet, got, requiredCharacterSet)
		}
	})
}

func TestTheStringLiteralGrammarIsTheOneTheCompilerQuotesFor(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		if got := scalar(t, c, `SELECT length('a\nb')`); got != "4" {
			t.Errorf(`'a\nb' is %s characters, want 4`, got)
		}

		if got := scalar(t, c, `SELECT 'a''b'`); got != "a'b" {
			t.Errorf("a doubled quote reads back as %q", got)
		}

		t.Log("Four characters, not three: with NO_BACKSLASH_ESCAPES the backslash is a backslash, so")
		t.Log("doubling the quote is the whole defense -- which is what quoteLiteral is written for,")
		t.Log("and what Compile being pure means it cannot check for itself.")
	})
}

func TestAStatementLockWaitIsWhatTheOperatorConfiguredRatherThanTheServersDefault(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			c := openServer(t, s, driver.Options{
				HistoryTable:      uniqueName(t),
				StatementLockWait: 7 * time.Second,
			})

			for _, name := range []string{varLockWait, varRowLockWait} {
				if got := scalar(t, c, "SELECT @@session."+name); got != "7" {
					t.Errorf("%s is %q, want 7", name, got)
				}
			}

			t.Log("The two are separate queues: lock_wait_timeout bounds the metadata lock every DDL")
			t.Log("statement takes, innodb_lock_wait_timeout bounds a row lock. Invariant #12 needs")
			t.Log("both, and the server defaults are a year and fifty seconds.")
		})
	}
}

func TestAConnectionStringThisDriverRefusesNeverReachesTheServer(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			d, err := New(driver.Options{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if _, err := d.Open(t.Context(), s.dsnFor()+"?parseTime=true"); !errors.Is(err, ErrDSN) {
				t.Errorf("Open returned %v, want %v", err, ErrDSN)
			}
		})
	}
}

func TestASchemaThatIsNotTheConnectionStringsDatabaseIsRefusedAtOpen(t *testing.T) {
	for _, s := range servers {
		t.Run(s.name, func(t *testing.T) {
			d, err := New(driver.Options{Schema: "somewhere_else"})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if _, err := d.Open(t.Context(), s.dsnFor()); !errors.Is(err, ErrSchema) {
				t.Errorf("Open returned %v, want %v", err, ErrSchema)
			}
		})
	}
}

func TestTheSessionHoldsTheOneConnectionThePoolWillHandOut(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		ctx, cancel := context.WithTimeout(t.Context(), 300*time.Millisecond)
		defer cancel()

		if _, err := c.db.Conn(ctx); err == nil {
			t.Error("the pool handed out a second connection while the session held one")
		}

		t.Log("The contract requires one pinned session, and M9-T4's GET_LOCK is connection-scoped:")
		t.Log("acquiring on one pooled connection and releasing on another silently fails to")
		t.Log("release. SetMaxOpenConns(1) is what makes that structural rather than remembered.")
	})
}

func TestTheConformanceSuitePassesOverALiveSession(t *testing.T) {
	eachServer(t, func(t *testing.T, s server, c *mysqlConn) {
		d, err := New(driver.Options{})
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		conformance.Suite{Driver: d, Conn: c}.Run(t)

		t.Log("The untagged run proves the biconditional over a declaration built with no server.")
		t.Log("This one proves it over the declaration a real server produced, which is a different")
		t.Log("set and a different claim.")
	})
}
