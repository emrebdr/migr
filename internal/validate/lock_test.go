package validate

import (
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// costed declares one kind at a cost and every other lock-free, as a connected server would.
func costed(t *testing.T, kind ir.Kind, lock capability.LockImpact) *capability.Set {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, k := range ir.Kinds() {
		rules[k] = capability.Fixed(capability.Native(capability.LockFree))
	}

	rules[kind] = capability.Fixed(capability.Native(lock))

	set, err := capability.NewSet(capability.Facts{Driver: driverName, Server: "1.0"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return &set
}

func TestAnUpOperationOverTheCeilingIsALockProblem(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{raw("ALTER TABLE orders ADD CHECK (id > 0)")},
		down: ir.Operations{raw("ALTER TABLE orders DROP CONSTRAINT x")},
	}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: costed(t, ir.KindRaw, capability.LockUnknown),
		Ceiling:      capability.LongExclusive,
	}, src, compiler{}, m)

	problems := report.Problems()
	if len(problems) != 1 || problems[0].Check != CheckLock || problems[0].Direction != ir.Up ||
		problems[0].Operation != 1 {
		t.Fatalf("the report holds %+v, want one lock problem in the up half", problems)
	}

	if !strings.Contains(problems[0].Detail, "unknown") ||
		!strings.Contains(problems[0].Detail, "safety.max_lock_impact (long_exclusive)") {
		t.Errorf("the detail %q does not name the cost and the ceiling", problems[0].Detail)
	}

	t.Log("A raw statement is unknown, which ranks as table_rewrite: a .sql project passes this")
	t.Log("check only under a table_rewrite ceiling, which is the honest answer about opaque SQL.")
}

func TestTheLockCheckJudgesTheUpHalfAlone(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{table(t, "orders")},
		down: ir.Operations{ir.DropTable{Table: "orders"}},
	}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: costed(t, ir.KindDropTable, capability.LongExclusive),
		Ceiling:      capability.BriefExclusive,
	}, src, compiler{}, m)

	if !report.Clean() {
		t.Errorf("a costly down was judged: %+v", report.Problems())
	}
}

func TestNoCeilingRunsNoLockCheck(t *testing.T) {
	m := migrationAt(0, "add_orders")
	src := source{m.Version: {
		up:   ir.Operations{raw("ALTER TABLE orders ADD CHECK (id > 0)")},
		down: ir.Operations{raw("ALTER TABLE orders DROP CONSTRAINT x")},
	}}

	report := runOnline(t, Options{
		Allow:        engine.Allowance{All: true},
		Capabilities: costed(t, ir.KindRaw, capability.LockUnknown),
	}, src, compiler{}, m)

	if !report.Clean() {
		t.Errorf("a run given no ceiling judged the lock axis: %+v", report.Problems())
	}
}

func TestACeilingWithNoDeclarationIsRefused(t *testing.T) {
	_, err := Run(t.Context(), Options{
		Migrations: nil,
		Source:     source{},
		Planner:    plan.Planner{Compiler: compiler{}},
		Ceiling:    capability.BriefExclusive,
	})

	if !errors.Is(err, ErrCeilingOffline) {
		t.Errorf("Run returned %v, want the ceiling refused for want of a declaration", err)
		t.Log("Without one every operation's cost would be zero, which satisfies no ceiling, so the")
		t.Log("check would report every operation of every migration rather than refuse to run.")
	}
}
