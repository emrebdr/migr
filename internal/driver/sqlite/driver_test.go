package sqlite

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
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

// needsRebuild lists the operations SQLite performs only by rebuilding the whole table.
var needsRebuild = map[ir.Kind]bool{
	ir.KindAlterColumn:    true,
	ir.KindAddForeignKey:  true,
	ir.KindDropForeignKey: true,
	ir.KindAddCheck:       true,
	ir.KindDropCheck:      true,
	ir.KindAddPrimaryKey:  true,
	ir.KindDropPrimaryKey: true,
}

func TestEveryOperationIsCompiledOrRefusedByBothTheCompilerAndTheDeclaration(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	for _, kind := range ir.Kinds() {
		op := representative(t, kind)
		stmts, err := d.Compile(op)

		compilable := err == nil || errors.Is(err, driver.ErrNeedsSession)
		if allowed := linked(t).verdict(op).Allowed(); allowed != compilable {
			t.Errorf("the declaration allows %s (%t) and the compiler %v", kind, allowed, err)
		}

		if !needsRebuild[kind] {
			checkCompiled(t, kind, stmts, err)

			continue
		}

		checkEmulated(t, kind, stmts, err, linked(t).verdict(op))
	}

	t.Log("The declaration is derived from the compiler, so the biconditional the conformance")
	t.Log("suite proves holds by construction rather than by coincidence. Seven operations are")
	t.Log("compiled against the session instead, and say so rather than refusing.")
}

// checkCompiled reports an operation this driver was meant to compile and did not.
func checkCompiled(t *testing.T, kind ir.Kind, stmts []driver.Statement, err error) {
	t.Helper()

	if err != nil {
		t.Errorf("Compile(%s) refused: %v", kind, err)

		return
	}

	if len(stmts) == 0 {
		t.Errorf("Compile(%s) reported success and produced no statement", kind)
	}
}

// checkEmulated reports a rebuild operation that compiled, or one the declaration disagrees about.
func checkEmulated(t *testing.T, kind ir.Kind, stmts []driver.Statement,
	err error, verdict capability.Verdict,
) {
	t.Helper()

	if !errors.Is(err, driver.ErrNeedsSession) {
		t.Errorf("Compile(%s) returned %v, want %v", kind, err, driver.ErrNeedsSession)

		return
	}

	if len(stmts) != 0 {
		t.Errorf("Compile(%s) returned %d statements as well as asking for a session",
			kind, len(stmts))
	}

	if !strings.Contains(err.Error(), rebuildStrategy) {
		t.Errorf("Compile(%s) does not name the strategy: %v", kind, err)
	}

	if verdict.Support() != capability.SupportEmulated || verdict.Strategy() != rebuildStrategy {
		t.Errorf("the declaration of %s is %q, want an emulation by %q", kind, verdict,
			rebuildStrategy)
	}

	if verdict.Lock() != rebuildImpact {
		t.Errorf("the declaration of %s costs %s, want %s", kind, verdict.Lock(), rebuildImpact)
	}
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
	t.Log("until then -- a driver that silently substituted its own would be the surprise the lock")
	t.Log("strategy contract refuses.")
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

	_, err := New(driver.Options{HistoryTable: "migr\x00migrations"})
	if !errors.Is(err, driver.ErrUnsupported) {
		t.Errorf("the refusal is %v, and not one the compiler produced", err)
	}

	t.Log("Compiling history.Schema(table) is the whole check here, where PostgreSQL also measures")
	t.Log("the name: SQLite has no identifier length, so there is nothing about a name this driver")
	t.Log("refuses that the operations carrying it do not. It is also what keeps M8-T6 honest --")
	t.Log("the history table is itself driver-compiled IR (invariant #3), so a schema this driver")
	t.Log("could not build is refused at New rather than at the first Ensure.")
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
