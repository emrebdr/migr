package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/plan"
)

// costing is a plan of one add_index per verdict given, each step carrying it as a session would.
func costing(t *testing.T, verdicts ...capability.Verdict) plan.Plan {
	t.Helper()

	steps := make([]plan.Step, 0, len(verdicts))
	for i, v := range verdicts {
		steps = append(steps, plan.Step{Index: int32(i + 1), Op: addIndex("by_" + string(rune('a'+i))),
			Verdict: v})
	}

	return plan.Plan{Migration: migrationAt(t, 0), Direction: ir.Up, Steps: steps}
}

func TestEveryVerdictIsJudgedAgainstEveryCeiling(t *testing.T) {
	impacts := append(capability.Impacts(), capability.LockImpact("bogus"))

	verdicts := make([]capability.Verdict, 0, len(impacts))
	for _, impact := range impacts {
		verdicts = append(verdicts, capability.Native(impact))
	}

	compiled := costing(t, verdicts...)

	for ceiling, want := range map[capability.LockImpact]int{
		capability.LockFree:       5,
		capability.BriefExclusive: 4,
		capability.LongExclusive:  3,
		capability.TableRewrite:   1,
	} {
		if found := Exceeding(compiled, ceiling); len(found) != want {
			t.Errorf("under %s, %d verdicts exceed, want %d: %+v", ceiling, len(found), want, found)
		}
	}

	t.Log("unknown ranks as table_rewrite, so a raw statement passes only the loosest ceiling, and a")
	t.Log("verdict this build does not recognize satisfies none -- a bug earns no permission.")
}

func TestAStepWithNoVerdictExceedsEveryCeiling(t *testing.T) {
	if found := Exceeding(costing(t, capability.Verdict{}), capability.TableRewrite); len(found) != 1 {
		t.Error("a step nothing resolved a verdict for passed the gate, which would pass a plan whose " +
			"planner was never given the session's capabilities")
	}
}

func TestARewrittenStepIsJudgedAtItsRewritesCost(t *testing.T) {
	compiled := costing(t, capability.Native(capability.LongExclusive))
	compiled.Steps[0].Rewrite = &plan.Rewritten{Name: driver.RewriteConcurrentIndex,
		Lock: capability.LockFree}

	if found := Exceeding(compiled, capability.BriefExclusive); len(found) != 0 {
		t.Errorf("a rewritten step was judged at its declared cost: %+v", found)
	}

	compiled.Steps[0].Rewrite.Lock = capability.LongExclusive

	found := Exceeding(compiled, capability.BriefExclusive)
	if len(found) != 1 || found[0].Rewrite != driver.RewriteConcurrentIndex {
		t.Errorf("a rewrite still over the ceiling was reported as %+v", found)
	}
}

func TestAnOperationThisServerRefusesIsLeftToTheCapabilityCheck(t *testing.T) {
	if found := Exceeding(costing(t, capability.Unsupported(serverReason)), capability.LockFree); len(found) != 0 {
		t.Errorf("a refused operation was also reported over the ceiling: %+v", found)
	}
}

func TestALockImpactErrorNamesEveryMigrationAndOperation(t *testing.T) {
	compiled := costing(t, capability.Native(capability.LockFree), capability.Native(capability.TableRewrite))
	err := error(&LockImpactError{Ceiling: capability.BriefExclusive, Exceeded: []Exceeded{{
		Migration: compiled.Migration, Direction: ir.Up, Found: Exceeding(compiled, capability.BriefExclusive),
	}}})

	if !errors.Is(err, ErrLockImpact) {
		t.Error("the failure does not unwrap to ErrLockImpact, so exit 5 cannot be resolved from it")
	}

	for _, want := range []string{"1 operation exceeds safety.max_lock_impact (brief_exclusive)",
		compiled.Migration.Filename(), "operation 2 (add_index)", "table_rewrite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the failure does not say %q:\n%v", want, err)
		}
	}
}

// assessing is a session that reads the table for every conditional verdict it is asked about.
type assessing struct {
	*recorder
	// asked lists the operations it read a table for.
	asked []string
	// err is what reading reports, nil where it succeeds.
	err error
}

// Assess confirms the cheaper cost, as a session that found the table met the condition would.
func (a *assessing) Assess(_ context.Context, op ir.Operation, v capability.Verdict) (capability.Verdict, error) {
	a.asked = append(a.asked, Subject(op))
	if a.err != nil {
		return v, a.err
	}

	return v.Confirmed(), nil
}

// conditional declares add_index a rewrite unless the session finds the table small.
func conditional(t *testing.T, conn *recorder) *capability.Set {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = capability.Fixed(capability.Native(capability.LockFree))
	}

	rules[ir.KindAddIndex] = capability.Fixed(capability.Native(capability.TableRewrite).
		Unless(capability.BriefExclusive, "the table is small"))

	set, err := capability.NewSet(capability.Facts{Driver: "recorder", Server: "1.0",
		TransactionalDDL: true}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	conn.caps = set

	return &set
}

// assessedPreview previews two staged migrations of one add_index each over a session that assesses.
func assessedPreview(t *testing.T, session *assessing) Previewed {
	t.Helper()

	opts, src := staged(t, "a", "b")
	opts.Planner.Capabilities = conditional(t, session.recorder)
	src.ops[versionAt(0)] = ir.Operations{addIndex("first")}
	src.ops[versionAt(1)] = ir.Operations{addIndex("second")}

	previewed, err := Preview(t.Context(), session, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	return previewed
}

func TestAPreviewAssessesTheFirstQueuedMigrationAndNoOther(t *testing.T) {
	session := &assessing{recorder: newRecorder(t, true)}
	previewed := assessedPreview(t, session)

	first, second := previewed.Steps[0].Plan.Steps[0], previewed.Steps[1].Plan.Steps[0]

	if first.Lock() != capability.BriefExclusive || !first.Assessed {
		t.Errorf("the first migration's step reads %s (assessed %v), want it lowered", first.Lock(),
			first.Assessed)
	}

	if second.Lock() != capability.TableRewrite || second.Assessed {
		t.Errorf("the second migration's step reads %s (assessed %v), want its declared worst",
			second.Lock(), second.Assessed)
	}

	if len(session.asked) != 1 {
		t.Errorf("the session read %d tables, want only the first migration's", len(session.asked))
	}

	t.Log("A preview applies nothing, so a table the first migration changes is not yet what the")
	t.Log("second will meet. Only the first is assessed, judgedFirst's own rule.")
}

func TestAPreviewKeepsTheWorstWhereTheTableCannotBeRead(t *testing.T) {
	session := &assessing{recorder: newRecorder(t, true), err: errors.New("no PROCESS grant")}
	previewed := assessedPreview(t, session)

	if step := previewed.Steps[0].Plan.Steps[0]; step.Lock() != capability.TableRewrite || step.Assessed {
		t.Errorf("an unread table lowered the verdict to %s (assessed %v)", step.Lock(), step.Assessed)
	}

	if unread := previewed.Steps[0].Unassessed; unread == nil || !strings.Contains(unread.Error(), "PROCESS") {
		t.Errorf("the preview did not say why the verdict stayed at its worst: %v", unread)
	}
}

func TestAPreviewNeverAssessesARewrittenStep(t *testing.T) {
	session := &assessing{recorder: newRecorder(t, true)}

	opts, src := staged(t, "a")
	opts.Planner.Capabilities = conditional(t, session.recorder)
	opts.Planner.Rewriter, opts.Planner.Rewrites = concurrent{}, driver.RewriteNames()
	src.ops[versionAt(0)] = ir.Operations{addIndex("first")}

	previewed, err := Preview(t.Context(), session, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if len(session.asked) != 0 || previewed.Steps[0].Plan.Steps[0].Lock() != capability.LockFree {
		t.Errorf("a rewritten step was assessed (%v), or judged at %s", session.asked,
			previewed.Steps[0].Plan.Steps[0].Lock())
	}
}

// concurrent rewrites every add_index into one bare statement that blocks nothing.
type concurrent struct{}

// Rewrite answers for add_index alone.
func (concurrent) Rewrite(op ir.Operation) (driver.Rewrite, bool) {
	index, ok := op.(ir.AddIndex)
	if !ok {
		return driver.Rewrite{}, false
	}

	return driver.Rewrite{Name: driver.RewriteConcurrentIndex, Reason: "it blocks nothing",
		Lock:       capability.LockFree,
		Statements: []driver.Statement{{SQL: "concurrently " + index.Name, NoTransaction: true}}}, true
}
