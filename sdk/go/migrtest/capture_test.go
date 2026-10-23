package migrtest

import (
	"errors"
	"strings"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

func TestACaptureHoldsTheDocumentsTheEngineWouldHaveReceived(t *testing.T) {
	Up(t, creates{}).Is(t, migr.CreateTable{
		Table: theTable,
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "email", Type: migr.String(255)},
		},
		PrimaryKey: []string{"id"},
	})
}

func TestAnAuthoredDownIsCapturedTheSameWay(t *testing.T) {
	Down(t, creates{}).Is(t, migr.DropTable{
		Table: theTable,
		From: &migr.TableDef{
			Columns: []migr.Column{
				{Name: "id", Type: migr.Int64()},
				{Name: "email", Type: migr.String(255)},
			},
			PrimaryKey: []string{"id"},
		},
	})
}

func TestBothModesEmitTheSameOperations(t *testing.T) {
	applying := Stage{Mode: migr.ModeApply}.Up(t, creates{})
	planning := Stage{Mode: migr.ModePlan}.Up(t, creates{})

	if len(applying.Operations) != len(planning.Operations) {
		t.Fatalf("apply emitted %d operations and plan emitted %d",
			len(applying.Operations), len(planning.Operations))
	}

	for i := range applying.Operations {
		if applying.Operations[i] != planning.Operations[i] {
			t.Errorf("operation %d differs between the modes:\napply %s\n plan %s",
				i+1, applying.Operations[i], planning.Operations[i])
		}
	}

	t.Log("The engine records operations in both modes, so a preview that emitted")
	t.Log("something else would be a preview of a run that never happens.")
}

func TestAModeIsApplyUnlessTheStageSaysOtherwise(t *testing.T) {
	for _, c := range []struct {
		name     string
		mode     migr.Mode
		executed string
	}{
		{"an empty stage runs the half", migr.Mode(""), "true"},
		{"apply runs it", migr.ModeApply, "true"},
		{"plan asks it", migr.ModePlan, "false"},
	} {
		t.Run(c.name, func(t *testing.T) {
			captured := Stage{Mode: c.mode}.Up(t, mixes{})

			if len(captured.Statements) != 1 {
				t.Fatalf("the migration sent %d statements, want 1", len(captured.Statements))
			}

			if executed := captured.Logs[0].Fields["executed"]; executed != c.executed {
				t.Errorf("the migration was told executed=%s, want %s", executed, c.executed)
			}
		})
	}
}

func TestAStatementIsHeldUnderPlanAndReportedPerformedUnderApply(t *testing.T) {
	held := Stage{Mode: migr.ModePlan}.Up(t, mixes{})
	performed := Stage{Mode: migr.ModeApply, Affected: map[string]string{theStatement: "41"}}.
		Up(t, mixes{})

	if held.Statements[0].SQL != theStatement || performed.Statements[0].SQL != theStatement {
		t.Fatalf("the statement was not recorded in both modes: %+v and %+v",
			held.Statements, performed.Statements)
	}

	if rows := held.Logs[0].Fields["rows"]; rows != "0" {
		t.Errorf("plan mode answered %q rows affected, and the engine's host answers \"0\"", rows)
	}

	if rows := performed.Logs[0].Fields["rows"]; rows != "41" {
		t.Errorf("apply mode answered %q rows affected, want the staged 41", rows)
	}

	t.Log("Neither statement reached a database: there is none here. What is copied from the real")
	t.Log("host is the answer, so a migration that branches on Executed takes the branch it would.")
}

func TestAStagedReadIsWhatTheMigrationBranchesOn(t *testing.T) {
	for _, c := range []struct {
		name     string
		rows     migr.Rows
		nullable bool
	}{
		{"an empty table takes the NOT NULL branch", Answer([]string{"count"},
			[]string{"0"}), false},
		{"a loaded table takes the nullable branch", Answer([]string{"count"},
			[]string{"12"}), true},
	} {
		t.Run(c.name, func(t *testing.T) {
			captured := Stage{Reads: map[string]migr.Rows{theRead: c.rows}}.Up(t, branches{})

			captured.Is(t, theColumn(t, c.nullable))

			if len(captured.Reads) != 1 || captured.Reads[0].SQL != theRead {
				t.Errorf("the capture recorded %+v, want the one read the migration made",
					captured.Reads)
			}
		})
	}
}

func TestAnUnstagedReadIsRefusedRatherThanAnswered(t *testing.T) {
	host := &recorder{mode: migr.ModeApply}
	window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

	err := branches{}.Up(t.Context(), window.Schema())

	if !errors.Is(err, ErrUnstaged) {
		t.Fatalf("the read was answered with %v, want a refusal naming what was not staged", err)
	}

	if !strings.Contains(err.Error(), theRead) {
		t.Errorf("the refusal is %q, which does not name the statement the migration read", err)
	}

	if _, err := window.Seal(); err != nil {
		t.Errorf("sealing: %v", err)
	}
}

func TestAMigrationThatSwallowsARefusedReadStillFailsTheCapture(t *testing.T) {
	host := &recorder{mode: migr.ModeApply}
	window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

	if err := (swallows{}).Up(t.Context(), window.Schema()); err != nil {
		t.Fatalf("this fixture ignores the refusal, so it returns nothing: %v", err)
	}

	if _, err := window.Seal(); err != nil {
		t.Fatalf("sealing: %v", err)
	}

	if refused := host.problem(); !errors.Is(refused, ErrUnstaged) {
		t.Errorf("the host remembered %v, want the refusal it answered with", refused)
	}

	t.Log("The capture fails on the host's own record rather than on what the migration returned,")
	t.Log("because a migration that emits from a read that never happened is the case to catch.")
}

func TestHostInfoAndACapabilityAreRefusedUntilTheyAreStaged(t *testing.T) {
	for _, c := range []struct {
		name  string
		stage Stage
		names string
	}{
		{"nothing is staged", Stage{}, "host.info"},
		{"the facts are staged and the verdict is not", Stage{
			Info: &migr.Info{Driver: "postgres"},
		}, "add_index"},
	} {
		t.Run(c.name, func(t *testing.T) {
			host := &recorder{stage: c.stage, mode: migr.ModeApply}
			window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

			err := asks{}.Up(t.Context(), window.Schema())

			if !errors.Is(err, ErrUnstaged) {
				t.Fatalf("the question was answered with %v, want a refusal", err)
			}

			if !strings.Contains(err.Error(), c.names) {
				t.Errorf("the refusal is %q, which does not name %q", err, c.names)
			}
		})
	}
}

func TestAStagedEngineIsWhatTheMigrationIsToldAboutExceptTheMode(t *testing.T) {
	captured := Stage{
		Mode:     migr.ModePlan,
		Info:     &migr.Info{Database: "default", Driver: "postgres", Server: "17.2", Mode: migr.ModeApply},
		Verdicts: map[migr.Kind]migr.Verdict{migr.KindAddIndex: {Support: "native", Lock: "lock_free"}},
	}.Up(t, asks{})

	if driver := captured.Logs[0].Fields["driver"]; driver != "postgres" {
		t.Errorf("the migration was told the driver is %q, want the staged postgres", driver)
	}

	if support := captured.Logs[0].Fields["support"]; support != "native" {
		t.Errorf("the migration was told the verdict is %q, want the staged native", support)
	}

	if mode := captured.Logs[0].Fields["mode"]; mode != string(migr.ModePlan) {
		t.Errorf("the migration was told the mode is %q, and this call is a %s", mode, migr.ModePlan)
	}

	t.Log("A staged Mode is overwritten with the mode this call really is, as the engine's own")
	t.Log("host does: the mode is the call's, never the session's or the stage's.")
}

func TestACaptureFailsOnEverythingTheHalfGotWrong(t *testing.T) {
	for _, c := range []struct {
		name string
		m    migr.Migration
		says string
	}{
		{"it built a change and never applied it", forgetful{}, "never applied"},
		{"it emitted from a read nothing answered", swallows{}, "staged no answer"},
		{"it returned an error of its own", failing{}, errFixture.Error()},
	} {
		t.Run(c.name, func(t *testing.T) {
			stood := newReported(t)
			empty := Stage{}

			empty.up(stood, c.m)

			if !stood.fatal {
				t.Fatalf("the capture reported nothing:\n%s", stood.said())
			}

			if !strings.Contains(stood.said(), c.says) {
				t.Errorf("the capture does not say %q:\n%s", c.says, stood.said())
			}
		})
	}

	t.Log("Each of these is asserted through the capture rather than through a window of the")
	t.Log("test's own, since what a bare window reports is the SDK's and not this helper's.")
}

func TestCapturingADownNothingAuthoredFailsTheCapture(t *testing.T) {
	stood := newReported(t)
	empty := Stage{}

	empty.down(stood, derives{})

	if !stood.fatal {
		t.Fatalf("a down was captured for a migration that authors none:\n%s", stood.said())
	}

	if !strings.Contains(stood.said(), "authors no down") {
		t.Errorf("the refusal does not say which migration shape this is:\n%s", stood.said())
	}
}

func TestAChainBuiltAndNeverAppliedFailsTheCapture(t *testing.T) {
	host := &recorder{mode: migr.ModeApply}
	window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

	if err := (forgetful{}).Up(t.Context(), window.Schema()); err != nil {
		t.Fatalf("this fixture returns nothing: %v", err)
	}

	_, forgotten := window.Seal()

	if !errors.Is(forgotten, migr.ErrNotApplied) {
		t.Errorf("sealing reported %v, want the SDK's own unapplied-changes error", forgotten)
	}
}

func TestAMigrationThatFailsIsReportedWithWhatItReturned(t *testing.T) {
	host := &recorder{mode: migr.ModeApply}
	window := migr.NewWindow(placeholder, migr.Up, migr.ModeApply, host)

	err := failing{}.Up(t.Context(), window.Schema())

	if !errors.Is(err, errFixture) {
		t.Errorf("the migration returned %v, want its own error", err)
	}

	if captured := host.captured(); len(captured.Operations) != 0 {
		t.Errorf("a migration that failed emitted %v", captured.Operations)
	}
}

func TestKindsNamesTheOperationsWithoutDecodingThem(t *testing.T) {
	want := []migr.Kind{migr.KindAddIndex}
	got := Stage{Mode: migr.ModePlan}.Up(t, mixes{}).Kinds()

	if len(got) != 1 || got[0] != want[0] {
		t.Errorf("the capture holds %v, want %v", got, want)
	}
}

func TestDocumentsRendersWhatTheSDKWouldHaveSent(t *testing.T) {
	rendered := Documents(t, theIndex())

	if len(rendered) != 1 {
		t.Fatalf("rendered %d documents, want 1", len(rendered))
	}

	emitted := Stage{Mode: migr.ModePlan}.Up(t, mixes{}).Operations[0]

	if emitted != rendered[0] {
		t.Errorf("the migration emitted\n%s\nand the same operation renders as\n%s",
			emitted, rendered[0])
	}
}

func TestIsComparesTheBytesAndNotTheShape(t *testing.T) {
	captured := Up(t, derives{})

	wrong := Documents(t, migr.AddColumn{
		Table:  theTable,
		Column: migr.Column{Name: "tier", Type: migr.Int64(), Nullable: true},
	})

	if captured.Operations[0] == wrong[0] {
		t.Fatal("these two documents differ only in the column's type, and compared equal")
	}

	if captured.Kinds()[0] != kindOf(wrong[0]) {
		t.Error("they are the same kind, which is what Kinds compares and Is does not")
	}
}

func TestAMigrationThatAuthorsNoDownCannotHaveOneCaptured(t *testing.T) {
	for _, c := range []struct {
		name  string
		m     migr.Migration
		undo  migr.Undo
		names string
	}{
		{"it declares that it cannot be undone", oneWay{}, migr.UndoDeclared, "not recoverable"},
		{"the engine derives its inverse", derives{}, migr.UndoDerived, "Reversal"},
	} {
		t.Run(c.name, func(t *testing.T) {
			undo, _ := migr.Undone(c.m)
			if undo != c.undo {
				t.Fatalf("this fixture is undone by %q, want %q", undo, c.undo)
			}

			if _, writes := c.m.(migr.Downer); writes {
				t.Fatal("this fixture authors a down, and this case is about one that does not")
			}

			if !strings.Contains(undoneBy(c.m), c.names) {
				t.Errorf("the refusal says %q, which does not name %q", undoneBy(c.m), c.names)
			}
		})
	}
}

func TestAnswerRendersEveryValueAsTheServersOwnText(t *testing.T) {
	rows := Answer([]string{"id", "email"}, []string{"1", "a@example.com"}, []string{"2", "b@x"})

	if rows.Len() != 2 {
		t.Fatalf("the answer holds %d rows, want 2", rows.Len())
	}

	if got := *rows.Values[1][1]; got != "b@x" {
		t.Errorf("the second row's second value is %q, want b@x", got)
	}

	if len(rows.Columns) != 2 {
		t.Errorf("the answer names %d columns, want 2", len(rows.Columns))
	}
}
