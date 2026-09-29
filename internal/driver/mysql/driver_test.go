package mysql

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// newDriver builds this driver from options, failing rather than returning a nil to be dereferenced.
func newDriver(t *testing.T, opts driver.Options) driver.Driver {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New(%+v): %v", opts, err)
	}

	return d
}

func TestTheDriverAnswersToTheNameItIsRegisteredUnder(t *testing.T) {
	if got := newDriver(t, driver.Options{}).Name(); got != Name {
		t.Errorf("the driver reports its name as %q, want %q", got, Name)
	}
}

func TestOnlyTheOneLockStrategyThisEngineHasIsAccepted(t *testing.T) {
	for _, requested := range []string{"", strategy} {
		if _, err := New(driver.Options{LockStrategy: requested}); err != nil {
			t.Errorf("New refused lock.strategy %q: %v", requested, err)
		}
	}

	for _, requested := range []string{"file", "table", "flock"} {
		_, err := New(driver.Options{LockStrategy: requested})
		if !errors.Is(err, ErrLockStrategy) {
			t.Errorf("New(%q) returned %v, want %v", requested, err, ErrLockStrategy)
		}
	}

	t.Log("GET_LOCK is a server-side named lock, which is lock.strategy's advisory half. Nothing")
	t.Log("here implements a lock table or a lock file, and ignoring the request would take a lock")
	t.Log("the operator did not ask for.")
}

func TestAPerStatementLockWaitMySQLWouldSilentlyChangeIsRefused(t *testing.T) {
	for name, wait := range map[string]time.Duration{
		"half a second":        500 * time.Millisecond,
		"a single millisecond": time.Millisecond,
		"longer than a year":   (maxWaitSeconds + 1) * time.Second,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := New(driver.Options{StatementLockWait: wait})
			if !errors.Is(err, ErrWait) {
				t.Fatalf("New returned %v, want %v", err, ErrWait)
			}

			if !strings.Contains(err.Error(), "safety.lock_timeout") {
				t.Errorf("the refusal does not name the key that set it: %v", err)
			}
		})
	}

	for _, wait := range []time.Duration{time.Second, 3 * time.Second, maxWaitSeconds * time.Second} {
		if _, err := New(driver.Options{StatementLockWait: wait}); err != nil {
			t.Errorf("New refused safety.lock_timeout %s: %v", wait, err)
		}
	}

	t.Log("lock_wait_timeout takes whole seconds: MySQL refuses 0.5 outright (1232) and turns 0 into")
	t.Log("1 while reporting success, and clamps anything past a year. All measured. A bound nobody")
	t.Log("asked for is worse than a refusal naming the key.")
}

func TestTheDefaultWaitsAreAcceptedSoABareProjectOpens(t *testing.T) {
	if _, err := New(driver.Options{}); err != nil {
		t.Fatalf("New with no waits set: %v", err)
	}

	lock, statement := driver.Options{}.Waits()
	if statement < time.Second {
		t.Errorf("the contract's default statement wait is %s, which this driver refuses", statement)
	}

	t.Logf("lock.timeout defaults to %s and is M9-T4's to bound; GET_LOCK takes a decimal number of "+
		"seconds, so a sub-second wait is expressible there and is not checked here.", lock)
}

func TestAHistoryTableNameThisDriverCannotCompileForIsRefusedAtConstruction(t *testing.T) {
	long := strings.Repeat("a", identifierLimit-len("_version_idx")+1)

	_, err := New(driver.Options{HistoryTable: long})
	if !errors.Is(err, ErrHistory) {
		t.Fatalf("New returned %v, want %v", err, ErrHistory)
	}

	if !strings.Contains(err.Error(), long+"_version_idx") {
		t.Errorf("the refusal does not name the index whose name overflowed: %v", err)
	}

	if _, err := New(driver.Options{HistoryTable: ""}); err != nil {
		t.Errorf("an empty history.table is defaulted by Options.Namespace, not refused: %v", err)
	}

	t.Log("The history table is created through compiled operations, so compiling its schema is the")
	t.Log("whole check -- and the index name is twelve characters longer than the table's, which is")
	t.Log("what overflows MySQL's 64 first.")
}

func TestCompileGoesThroughTheSameFunctionTheDeclarationReads(t *testing.T) {
	d := newDriver(t, driver.Options{})

	stmts, err := d.Compile(ir.CreateTable{
		Table:   "users",
		Columns: []ir.Column{{Name: "id", Type: mustType(t, "int64")}},
	})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}

	if len(stmts) != 1 || !strings.HasPrefix(stmts[0].SQL, "CREATE TABLE `users`") {
		t.Errorf("Compile returned %v", stmts)
	}
}
