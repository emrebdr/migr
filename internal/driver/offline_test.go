package driver_test

import (
	"context"
	"errors"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// offlineFor builds an offline session for a test, failing the test rather than the code under it.
func offlineFor(t *testing.T) *driver.OfflineConn {
	t.Helper()

	conn, err := driver.Offline("postgres")
	if err != nil {
		t.Fatalf("Offline: %v", err)
	}

	return conn
}

func TestAnOfflineSessionRefusesEveryCallThatNeedsAServer(t *testing.T) {
	for _, call := range []struct {
		what string
		make func(*driver.OfflineConn) error
	}{
		{what: "Exec", make: func(c *driver.OfflineConn) error {
			return c.Exec(t.Context(), driver.Statement{SQL: "SELECT 1"})
		}},
		{what: "Query", make: func(c *driver.OfflineConn) error {
			_, err := c.Query(t.Context(), driver.Query{SQL: "SELECT 1"})

			return err
		}},
		{what: "Perform", make: func(c *driver.OfflineConn) error {
			_, err := c.Perform(t.Context(), driver.Query{SQL: "DELETE FROM users"})

			return err
		}},
		{what: "Lock", make: func(c *driver.OfflineConn) error {
			_, err := c.Lock(t.Context())

			return err
		}},
		{what: "Transact", make: func(c *driver.OfflineConn) error {
			return c.Transact(t.Context(), func(context.Context) error { return nil })
		}},
	} {
		t.Run(call.what, func(t *testing.T) {
			conn := offlineFor(t)

			err := call.make(conn)
			if !errors.Is(err, driver.ErrNoDatabase) {
				t.Errorf("%s reported %v, want %v", call.what, err, driver.ErrNoDatabase)
			}

			if !conn.Reached() {
				t.Errorf("%s did not record that a server was needed", call.what)
				t.Log("The caller cannot learn this from the error: the migration runs in another")
				t.Log("process, so the sentinel is a string by the time it comes back (ADR 0040).")
			}
		})
	}
}

func TestAnOfflineSessionStartsHavingBeenReachedByNothing(t *testing.T) {
	if offlineFor(t).Reached() {
		t.Error("a session nothing has asked reports having been reached")
	}
}

func TestForgettingClearsWhatOneQuestionReached(t *testing.T) {
	conn := offlineFor(t)

	if _, err := conn.Query(t.Context(), driver.Query{SQL: "SELECT 1"}); err == nil {
		t.Fatal("the offline session answered a read")
	}

	conn.Forget()

	t.Log("What it remembers is per question, not per run. Forgetting nothing would spread one")
	t.Log("migration's read over every migration checked after it.")

	if conn.Reached() {
		t.Error("a forgotten session still reports the read before it")
	}
}

func TestFactsAboutTheSessionAreNotAServerBeingReached(t *testing.T) {
	conn := offlineFor(t)

	_, _ = conn.Capabilities(), conn.ServerVersion()

	t.Log("host.info reaches both on every migration that asks for it, so counting them would")
	t.Log("make every such migration unverifiable for a reason that is not about a database.")

	if conn.Reached() {
		t.Error("asking what the session is counted as reaching a server")
	}
}

func TestAnOfflineSessionRefusesEveryOperationWithAStatedReason(t *testing.T) {
	caps := offlineFor(t).Capabilities()

	if caps.Driver() != "postgres" {
		t.Errorf("the capability set names %q, want the driver migr.yaml chose", caps.Driver())
	}

	verdict := caps.Resolve(ir.Raw{Driver: "postgres", SQL: "SELECT 1"})

	t.Log("Offline builds its rules from ir.Kinds() and capability.NewSet refuses a set that omits")
	t.Log("one, so a kind going undeclared fails at construction rather than here. This package")
	t.Log("may not import drivertest, so there is no representative operation to walk.")

	if verdict.Allowed() {
		t.Error("an operation is allowed by a session that reached no server")
	}

	if verdict.Reason() == "" {
		t.Error("the refusal carries no reason, which is a bug rather than a verdict")
	}
}

func TestAnOfflineSessionHoldsNothingToGiveBack(t *testing.T) {
	conn := offlineFor(t)

	if conn.History() != nil {
		t.Error("a session that opened no database offered a history store")
	}

	if conn.ServerVersion() != "" {
		t.Errorf("ServerVersion is %q, and no server was reached to detect one",
			conn.ServerVersion())
	}

	if err := conn.Close(t.Context()); err != nil {
		t.Errorf("Close: %v", err)
	}
}
