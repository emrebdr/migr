package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
)

func TestTheDriverAnswersToTheNameItIsRegisteredUnder(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got := d.Name(); got != Name {
		t.Errorf("Name = %q, want %q", got, Name)
	}

	registry := driver.NewRegistry()
	if err := registry.Register(Name, New); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := registry.New(Name, driver.Options{}); err != nil {
		t.Errorf("the registry could not build it: %v", err)
	}
}

func TestEveryOperationIsRefusedByBothTheCompilerAndTheDeclaration(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, kind := range ir.Kinds() {
		op := representative(t, kind)

		stmts, err := d.Compile(op)
		if !errors.Is(err, driver.ErrUnsupported) {
			t.Errorf("Compile(%s) returned %v, want %v", kind, err, driver.ErrUnsupported)
		}

		if len(stmts) != 0 {
			t.Errorf("Compile(%s) returned %d statements as well as a refusal", kind, len(stmts))
		}

		if verdict := verdict(op); verdict.Allowed() {
			t.Errorf("the declaration allows %s while the compiler refuses it", kind)
		}

		if reason := verdict(op).Reason(); !strings.Contains(reason, "M8-T2") {
			t.Errorf("the refusal of %s does not name the task that lifts it: %q", kind, reason)
		}
	}

	t.Log("The declaration is derived from the compiler, so the biconditional the conformance")
	t.Log("suite proves holds by construction rather than by coincidence -- and M8-T2 arrives as")
	t.Log("a diff turning each refusal into SQL (M3-T1's shape).")
}

func TestAConfiguredSchemaIsRefusedRatherThanIgnored(t *testing.T) {
	_, err := New(driver.Options{Schema: "public"})
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("New returned %v, want %v", err, ErrSchema)
	}

	if !strings.Contains(err.Error(), "public") {
		t.Errorf("the refusal does not name what was configured: %v", err)
	}

	if _, err := New(driver.Options{Schema: "  "}); err != nil {
		t.Errorf("a blank schema is not a configured one: %v", err)
	}
}

func TestALockStrategyThisDriverDoesNotImplementIsRefused(t *testing.T) {
	_, err := New(driver.Options{LockStrategy: "advisory"})
	if !errors.Is(err, ErrLockStrategy) {
		t.Fatalf("New returned %v, want %v", err, ErrLockStrategy)
	}

	for _, accepted := range []string{"", strategy} {
		if _, err := New(driver.Options{LockStrategy: accepted}); err != nil {
			t.Errorf("New refused lock.strategy %q: %v", accepted, err)
		}
	}

	t.Log("file is the strategy M8-T5 implements, so it is accepted here and Lock refuses by name")
	t.Log("until then -- a driver that silently substituted its own would be the surprise ADR 0020")
	t.Log("refuses.")
}

func TestAHistoryTableNameThisDriverCannotCarryIsRefused(t *testing.T) {
	for name, table := range map[string]string{
		"blank":  "   ",
		"a NUL":  "migr\x00migrations",
		"nested": "schema\x00.migrations",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(driver.Options{HistoryTable: table}); err == nil {
				t.Fatalf("New accepted history.table %q", table)
			}
		})
	}

	if _, err := New(driver.Options{HistoryTable: "migr_migrations"}); err != nil {
		t.Errorf("New refused an ordinary table name: %v", err)
	}
}

func TestAWaitLongerThanSQLiteCanHoldIsRefused(t *testing.T) {
	for key, opts := range map[string]driver.Options{
		"lock.timeout":        {LockWait: maxBusyTimeout + time.Millisecond},
		"safety.lock_timeout": {StatementLockWait: maxBusyTimeout + time.Millisecond},
	} {
		t.Run(key, func(t *testing.T) {
			_, err := New(opts)
			if !errors.Is(err, ErrWaitTooLong) {
				t.Fatalf("New returned %v, want %v", err, ErrWaitTooLong)
			}

			if !strings.Contains(err.Error(), key) {
				t.Errorf("the refusal does not name the key that set it: %v", err)
			}
		})
	}

	if _, err := New(driver.Options{StatementLockWait: maxBusyTimeout}); err != nil {
		t.Errorf("New refused the longest wait SQLite holds: %v", err)
	}
}

// representative returns one operation of a kind, for a test that needs a payload rather than a name.
func representative(t *testing.T, kind ir.Kind) ir.Operation {
	t.Helper()

	for _, op := range drivertest.Operations(Name) {
		if op.Kind() == kind {
			return op
		}
	}

	t.Fatalf("no representative operation for %s", kind)

	return nil
}
