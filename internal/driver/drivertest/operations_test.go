package drivertest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/ir"
)

func TestThereIsAnOperationForEveryKind(t *testing.T) {
	ops := Operations("postgres")

	offered := make(map[ir.Kind]int, len(ops))
	for _, op := range ops {
		offered[op.Kind()]++
	}

	for _, kind := range ir.Kinds() {
		if offered[kind] != 1 {
			t.Errorf("%s appears %d times, want exactly once", kind, offered[kind])
			t.Log("Every driver's golden suite and M2-T4's conformance walk resolve through this")
			t.Log("set, so a kind missing here is a kind nothing compiles anywhere.")
		}
	}

	if len(ops) != len(ir.Kinds()) {
		t.Errorf("the set holds %d operations for %d kinds", len(ops), len(ir.Kinds()))
	}
}

func TestTheOperationsComeInDeclarationOrder(t *testing.T) {
	ops := Operations("postgres")

	for i, kind := range ir.Kinds() {
		if i < len(ops) && ops[i].Kind() != kind {
			t.Errorf("position %d holds %s, want %s", i, ops[i].Kind(), kind)
			t.Log("Golden files are written in this order, so a stable order keeps an unrelated")
			t.Log("change out of the diff (invariant #8).")
		}
	}
}

func TestEveryFixtureOperationIsWellFormed(t *testing.T) {
	for _, op := range Operations("postgres") {
		if err := op.Validate(); err != nil {
			t.Errorf("the %s fixture is not a well-formed operation: %v", op.Kind(), err)
			t.Log("Logical types are parsed without a place to report failure, so a misspelled")
			t.Log("one becomes the zero Type. This is where that surfaces.")
		}
	}
}

func TestRawTargetsTheDriverBeingTested(t *testing.T) {
	for _, name := range []string{"postgres", "mysql", "sqlite"} {
		raw, ok := operationOfKind(Operations(name), ir.KindRaw).(ir.Raw)
		if !ok {
			t.Fatalf("%s: the set holds no raw operation", name)
		}

		if raw.Driver != name {
			t.Errorf("%s: the raw fixture targets %q", name, raw.Driver)
			t.Log("raw names the engine its SQL is written for, so a fixture hard-coded to one")
			t.Log("driver would compile to a refusal in every other driver's golden file and")
			t.Log("record that refusal as though it were the engine's real answer.")
		}
	}
}

func TestTheRawFixtureCarriesNoEngineSQL(t *testing.T) {
	raw, ok := operationOfKind(Operations("postgres"), ir.KindRaw).(ir.Raw)
	if !ok {
		t.Fatal("the set holds no raw operation")
	}

	if !strings.HasPrefix(strings.TrimSpace(raw.SQL), "--") {
		t.Errorf("the raw fixture is %q, want a SQL comment", raw.SQL)
		t.Log("Invariant #3 keeps engine SQL out of every package above a driver, and this one")
		t.Log("is three levels above. A comment passes through a driver verbatim just as well.")
	}
}

func TestTheGoldenRootIsFoundByWalkingUpFromAnyDepth(t *testing.T) {
	root, err := defaultRoot()
	if err != nil {
		t.Fatalf("locating the golden root from %s: %v", mustGetwd(t), err)
	}

	if filepath.Base(root) != "golden" || filepath.Base(filepath.Dir(root)) != "test" {
		t.Errorf("defaultRoot() = %q, want the repository's test/golden", root)
	}

	if _, err := os.Stat(root); err != nil {
		t.Errorf("the golden root does not exist: %v", err)
		t.Log("A relative hop like ../../.. resolves against the calling package's directory,")
		t.Log("so it is correct only at one depth. Walking up to the go.mod that names the")
		t.Log("root module works from internal/driver/postgres and test/conformance/driver alike.")
	}
}

// operationOfKind returns the one operation of a kind, or nil when the set holds none.
func operationOfKind(ops ir.Operations, kind ir.Kind) ir.Operation {
	for _, op := range ops {
		if op.Kind() == kind {
			return op
		}
	}

	return nil
}

// mustGetwd returns the working directory, for the message when root discovery fails.
func mustGetwd(t *testing.T) string {
	t.Helper()

	dir, err := os.Getwd()
	if err != nil {
		return "(unknown)"
	}

	return dir
}
