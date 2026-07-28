package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
)

// newDriver builds the driver under test, failing the test if construction ever grows a failure.
func newDriver(t *testing.T, opts driver.Options) driver.Driver {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	return d
}

func TestTheDriverAnswersToTheNameItRegistersUnder(t *testing.T) {
	registry := driver.NewRegistry()
	if err := registry.Register(Name, New); err != nil {
		t.Fatalf("Register: %v", err)
	}

	d, err := registry.New(Name, driver.Options{})
	if err != nil {
		t.Fatalf("registry.New: %v", err)
	}

	if d.Name() != Name {
		t.Errorf("Name() = %q, want %q", d.Name(), Name)
		t.Log("Registry.New rejects a driver whose Name disagrees with its registered key, so")
		t.Log("this failing means the registry would have refused to build it at all.")
	}
}

func TestEveryOperationCompilesToStatementsThatCarrySQL(t *testing.T) {
	d := newDriver(t, driver.Options{})

	for _, op := range drivertest.Operations(Name) {
		stmts, err := d.Compile(op)
		if err != nil {
			t.Errorf("%s: %v", op.Kind(), err)

			continue
		}

		if len(stmts) == 0 {
			t.Errorf("%s compiled to no statements at all, and reported no refusal", op.Kind())
		}

		for i, stmt := range stmts {
			if strings.TrimSpace(stmt.SQL) == "" {
				t.Errorf("%s: statement %d carries no SQL, so it changes nothing while reporting "+
					"success", op.Kind(), i+1)
			}

			if stmt.NoTransaction {
				t.Errorf("%s: statement %d opts out of the transaction, which nothing this driver "+
					"emits needs until CREATE INDEX CONCURRENTLY arrives (M13-T11)", op.Kind(), i+1)
			}
		}
	}
}

func TestCompilingNoOperationIsAnErrorRatherThanAPanic(t *testing.T) {
	d := newDriver(t, driver.Options{})

	if _, err := d.Compile(nil); err == nil {
		t.Error("compiling a nil operation succeeded")
		t.Log("forbidigo bans panic in this package, so every unreachable-looking case has to")
		t.Log("be an answer rather than a crash.")
	}
}

func TestTheDeclaredSetIsCompleteAndAllowsWhatCompiles(t *testing.T) {
	d := newDriver(t, driver.Options{})

	set, err := declaredSet(t)
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	if set.Driver() != Name {
		t.Errorf("the set names driver %q, want %q", set.Driver(), Name)
	}

	for _, op := range drivertest.Operations(Name) {
		_, compileErr := d.Compile(op)

		if verdict := set.Resolve(op); verdict.Allowed() != (compileErr == nil) {
			t.Errorf("%s: the declaration says %q and the compiler says %v", op.Kind(),
				verdict, compileErr)
		}

	}

	t.Log("This holds the support axis only. The lock axis moved to lock_test.go when M3-T3")
	t.Log("classified it, and the assertion that used to live here -- everything is unknown --")
	t.Log("is now the opposite of what is true.")

	t.Log("capability.NewSet refuses a set that does not answer for every operation, so this")
	t.Log("driver could not open a connection at all with a partial declaration. The set is pure,")
	t.Log("so the agreement the conformance suite exists to prove is checkable with no server.")
}

// declaredSet builds the set Open builds, for a server this test declares rather than connects to.
func declaredSet(t *testing.T) (capability.Set, error) {
	t.Helper()

	return capability.NewSet(modernServer.facts(), modernServer.rules())
}

func TestTheHistoryStoreRefusesRatherThanReturningNil(t *testing.T) {
	store := pendingHistory{}

	if err := store.Ensure(context.Background()); err == nil {
		t.Error("the pending history store reported success")
	}

	if _, err := store.List(context.Background()); !strings.Contains(err.Error(), historyTask) {
		t.Errorf("List failed with %v, which does not name the task that will fix it", err)
		t.Log("History() returns no error, so a driver with no store yet must hand back one")
		t.Log("that refuses. A nil would panic at the first call with nothing to read.")
	}
}
