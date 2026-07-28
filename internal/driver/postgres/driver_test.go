package postgres

import (
	"context"
	"errors"
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

func TestEveryOperationIsRefusedWithAReasonNamingItsTask(t *testing.T) {
	d := newDriver(t, driver.Options{})

	for _, op := range drivertest.Operations(Name) {
		stmts, err := d.Compile(op)

		if stmts != nil {
			t.Errorf("%s compiled to statements before the compiler exists", op.Kind())
		}

		var refusal *driver.UnsupportedError
		if !errors.As(err, &refusal) {
			t.Errorf("%s was refused with %v, which is not an UnsupportedError", op.Kind(), err)

			continue
		}

		if refusal.Driver != Name || refusal.Kind != op.Kind() {
			t.Errorf("the refusal names %s/%s, want %s/%s",
				refusal.Driver, refusal.Kind, Name, op.Kind())
		}

		if !strings.Contains(refusal.Reason, compileTask) {
			t.Errorf("reason %q does not name the task that will fix it", refusal.Reason)
			t.Log("Until M3-T2 lands this driver honestly supports nothing. Saying which task")
			t.Log("changes that is the difference between a refusal and a dead end.")
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

func TestTheDeclaredSetIsCompleteAndRefusesEverything(t *testing.T) {
	set, err := capability.NewSet(capability.Facts{Driver: Name, Server: "17.2"}, pendingRules())
	if err != nil {
		t.Fatalf("the declaration is not a complete capability set: %v", err)
	}

	if set.Driver() != Name {
		t.Errorf("the set names driver %q, want %q", set.Driver(), Name)
	}

	for _, op := range drivertest.Operations(Name) {
		verdict := set.Resolve(op)

		if verdict.Allowed() {
			t.Errorf("%s is declared supported by a driver that compiles nothing", op.Kind())
		}

		if !strings.Contains(verdict.Reason(), compileTask) {
			t.Errorf("the %s verdict reads %q, which names no task", op.Kind(), verdict.Reason())
		}
	}

	t.Log("capability.NewSet refuses a set that does not answer for every operation, so this")
	t.Log("driver could not have opened a connection at all with a partial declaration. An")
	t.Log("honest \"nothing yet\" is what lets the conformance suite run from the first commit.")
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
