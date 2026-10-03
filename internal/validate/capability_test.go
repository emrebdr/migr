package validate

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
)

// serverReason is a refusal only a connected server could have reached, which a compiler cannot.
const serverReason = "this release accepts the statement and discards it, so refusing is the only " +
	"answer that is not a lie"

func TestAnOperationThisServerRefusesIsAProblemCarryingItsReason(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{table(t, "orders"), raw("INSERT INTO orders")},
		down: ir.Operations{ir.DropTable{Table: "orders"}},
	}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: declaring(t, ir.KindRaw),
	}, src, compiler{}, m)

	problems := report.Problems()
	if len(problems) != 1 {
		t.Fatalf("the report holds %d problems, want the one the server refuses: %+v",
			len(problems), problems)
	}

	found := problems[0]
	if found.Check != CheckCapability || found.Operation != 2 || found.Kind != ir.KindRaw {
		t.Errorf("the problem is %+v, want the capability check at operation 2 (raw)", found)
	}

	if found.Detail != serverReason {
		t.Errorf("the detail is %q, want the driver's own stated reason", found.Detail)
		t.Log("The done-when asks for the driver's stated reason, and every driver's names the")
		t.Log("release, the variable or the extension to install. Rephrasing it here would lose")
		t.Log("the one thing an operator can act on.")
	}
}

func TestNothingIsCheckedAgainstAServerWhenNoneWasGiven(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{raw("INSERT INTO orders")},
		down: ir.Operations{raw("DELETE FROM orders")},
	}}

	if report := run(t, src, compiler{}, m); !report.Clean() {
		t.Errorf("an offline run reported %+v", report.Problems())
		t.Log("A capability verdict is resolved inside Open, so a run with no connection has")
		t.Log("nothing to ask -- and driver.Offline's own set refuses every operation, which is")
		t.Log("why nil means the check does not run rather than the check refusing everything.")
	}
}

func TestACompilerRefusalIsReportedOnceRatherThanTwice(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{raw("CREATE EXTENSION whatever")},
		down: ir.Operations{raw("DROP EXTENSION whatever")},
	}}

	c := compiler{refuse: "CREATE EXTENSION whatever"}

	offline := run(t, src, c, m)

	online := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: declaring(t, ir.KindRaw),
	}, src, c, m)

	if !slices.Equal(checksFound(offline), []Check{CheckCompile}) {
		t.Fatalf("the offline run reported %v, want the compile check alone", checksFound(offline))
	}

	if !slices.Equal(checksFound(online), []Check{CheckCompile, CheckCapability}) {
		t.Errorf("the connected run reported %v, want compile for the refused operation and "+
			"capability for the down half's", checksFound(online))
		t.Log("Every driver builds its verdict from Compile's own refusal first, so reporting both")
		t.Log("for one operation is the same sentence twice -- and worse, under a name that")
		t.Log("changed with the flag. The capability check adds exactly what compiling cannot see.")
	}
}

func TestBothHalvesAreJudgedAgainstTheServerIncludingADerivedDown(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {up: ir.Operations{table(t, "orders")}}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: declaring(t, ir.KindDropTable),
	}, src, compiler{}, m)

	problems := report.Problems()
	if len(problems) != 1 {
		t.Fatalf("the report holds %d problems, want the derived inverse's: %+v",
			len(problems), problems)
	}

	if problems[0].Direction != ir.Down || problems[0].Kind != ir.KindDropTable {
		t.Errorf("the problem is %+v, want the down half's drop_table", problems[0])
		t.Log("A rollback that cannot run is what this command exists to find before anyone tries")
		t.Log("it, and ir.CreateTable inverts to DropTable -- so the half nobody authored is")
		t.Log("exactly the one a verdict has to be asked about.")
	}
}

func TestAnOperationCompiledAgainstASessionIsStillJudgedAgainstTheServer(t *testing.T) {
	m := migrationAt(0, "alter_orders")
	src := source{m.Version: {up: ir.Operations{retype(t)}, down: ir.Operations{retype(t)}}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: declaring(t, ir.KindAlterColumn),
	}, src, emulating{}, m)

	if len(report.Unchecked()) != 2 {
		t.Errorf("the report holds %d unchecked halves, want one per direction: %+v",
			len(report.Unchecked()), report.Unchecked())
	}

	if !slices.Equal(checksFound(report), []Check{CheckCapability, CheckCapability}) {
		t.Errorf("the report holds %v, want the capability check for both halves",
			checksFound(report))
		t.Log("An emulation is compiled against the database and still declared against the")
		t.Log("server: MySQL's column redefinition is both. Unchecked says this run could not")
		t.Log("compile it, which is a different thing from the server refusing it.")
	}
}

func TestADeclarationNoDriverBuiltIsRefusedRatherThanUsed(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {up: ir.Operations{table(t, "orders")}}}

	_, err := Run(t.Context(), Options{
		Migrations:   []migration.Migration{m},
		Source:       src,
		Planner:      plan.Planner{Compiler: compiler{}},
		Capabilities: &capability.Set{},
	})

	if !errors.Is(err, ErrUnbuiltCapabilities) {
		t.Fatalf("Run returned %v, want ErrUnbuiltCapabilities", err)
	}

	t.Log("An unbuilt set answers every operation with a refusal of its own, so a caller that")
	t.Log("filled the field with one would turn this check into twenty problems per migration.")
	t.Log("Nil is how a run says it opened nothing; a zero Set is a bug and says so here.")
}

func TestAServerThatRefusesNothingReportsNothing(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{table(t, "orders"), raw("INSERT INTO orders")},
		down: ir.Operations{raw("DROP TABLE orders")},
	}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: declaring(t),
	}, src, compiler{}, m)

	if !report.Clean() {
		t.Errorf("a server that performs everything produced %+v", report.Problems())
	}
}

func TestTheCapabilityCheckIsOneOfTheChecksARunPerforms(t *testing.T) {
	if !CheckCapability.Valid() || !slices.Contains(Checks(), CheckCapability) {
		t.Error("the capability check is not one of the checks this package reports")
	}

	if strings.Contains(CheckCapability.String(), " ") {
		t.Errorf("the check's wire name is %q", CheckCapability)
	}
}
