package engine

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// The version, copy and retired original of the one staged shadow-table migration.
var (
	shadowVersion = versionAt(0)
	shadowCopy    = ShadowName(peopleTable, versionAt(0))
	shadowRetired = RetiredName(peopleTable, versionAt(0))
	shadowCapture = CaptureName(peopleTable, versionAt(0))
)

// busyApp writes to people while the copy and the hold run, and confirms after polls of the hold.
type busyApp struct {
	// w is the server it writes to.
	w *world
	// copies counts the copy chunks it has seen.
	copies int
	// polls counts the reads of the log it has seen while the copy is held in sync.
	polls int
	// confirmAfter is how many polls of the hold pass before it confirms, zero for never.
	confirmAfter int
	// kill names the point and occurrence at which it kills the process, empty for never.
	kill string
	// seen counts every point reached, by name.
	seen map[string]int
}

// at is the app's reaction to each point the run reaches.
func (a *busyApp) at(point string) {
	a.seen[point]++

	if a.kill != "" && a.kill == point+"#"+strconv.Itoa(a.seen[point]) {
		a.w.killed = true

		return
	}

	switch point {
	case "copy":
		a.copies++
		a.w.write(capture.Update, a.copies, "changed-during-copy")
		a.w.write(capture.Insert, 100+a.copies, "inserted-during-copy")
	case "next":
		a.poll()
	}
}

// poll writes and confirms while the copy is held in sync.
func (a *busyApp) poll() {
	state, found := a.w.states[shadowVersion]
	if !found || state.Stage != history.StageSyncing {
		return
	}

	a.polls++

	if a.polls == 2 {
		a.w.write(capture.Delete, 2, "")
	}

	if a.confirmAfter > 0 && a.polls == a.confirmAfter {
		a.w.confirm()
	}
}

// newApp attaches a busy app to a world, confirming after the given number of held polls.
func newApp(w *world, confirmAfter int, kill string) *busyApp {
	a := &busyApp{w: w, confirmAfter: confirmAfter, kill: kill, seen: map[string]int{}}
	w.at = a.at

	return a
}

// checkCutOver holds a world to a finished cutover: the table is the truth, the copy gone, the capture released.
func checkCutOver(t *testing.T, w *world) {
	t.Helper()

	if !maps.Equal(w.tables[peopleTable], w.truth) {
		t.Errorf("people holds %v after the cutover, want what the app wrote: %v", w.tables[peopleTable], w.truth)
	}

	if _, left := w.tables[shadowCopy]; left {
		t.Errorf("the copy %s is still there after the cutover", shadowCopy)
	}

	if _, kept := w.tables[shadowRetired]; !kept {
		t.Errorf("the original was not retained as %s", shadowRetired)
	}

	if len(w.slots) != 0 {
		t.Errorf("the capture resources %v outlived the cutover", w.slots)
	}

	if state := w.states[shadowVersion]; state.Stage != history.StageCutOver {
		t.Errorf("the state reads %q, want %q", state.Stage, history.StageCutOver)
	}
}

// rowOutcomes renders a world's history as outcomes in order.
func rowOutcomes(w *world) []string {
	found := make([]string, 0, len(w.runs))
	for _, r := range w.runs {
		found = append(found, string(r.Outcome))
	}

	return found
}

func TestAShadowTableMigrationCopiesHoldsInSyncAndCutsOverOnlyOnceConfirmed(t *testing.T) {
	w := newWorld(7)
	app := newApp(w, 6, "")
	c := connect(t, w)

	var held []ShadowProgress

	opts := shadowRun(t, c)
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding {
			held = append(held, p)
		}
	}

	result, err := Apply(t.Context(), c, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	checkCutOver(t, w)

	if app.polls < 6 || len(held) < 6 {
		t.Errorf("the copy was held for %d polls (%d reported), and the cutover was confirmed at the 6th",
			app.polls, len(held))
	}

	if got := rowOutcomes(w); !slices.Equal(got, []string{"dirty", "applied"}) {
		t.Errorf("the history reads %v, want the dirty row the start writes and the applied row the cutover does", got)
	}

	run := result.Runs[0]
	if run.Shadow == nil || run.Shadow.Retired != shadowRetired || run.Shadow.Copied == 0 || run.Shadow.Applied == 0 {
		t.Errorf("the run reported %+v", run.Shadow)
	}

	retired, err := Retire(t.Context(), c, opts, shadowVersion)
	if err != nil {
		t.Fatalf("Retire: %v", err)
	}

	if _, kept := w.tables[shadowRetired]; kept || retired.Dropped != shadowRetired {
		t.Errorf("Retire left %s behind, reporting %+v", shadowRetired, retired)
	}

	if _, kept := w.states[shadowVersion]; kept {
		t.Error("Retire left the shadow-table state behind")
	}
}

func TestTheChangeStreamOpensBeforeTheCopyReadsARow(t *testing.T) {
	w := newWorld(5)
	newApp(w, 1, "")
	c := connect(t, w)

	if _, err := Apply(t.Context(), c, shadowRun(t, c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	opened, copied := slices.Index(w.calls, "capture"), slices.Index(w.calls, "copy")
	if opened < 0 || copied < 0 || opened > copied {
		t.Errorf("the stream opened at call %d and the copy read at %d, in %v", opened, copied, w.calls)
	}
}

func TestEveryStageResumesFromWhatItRecordedAfterAKill(t *testing.T) {
	kills := []string{"exec create " + shadowCopy + "#1", "copy#1", "copy#3", "cutover#1", "swap#1"}
	for n := 1; n <= 8; n++ {
		kills = append(kills, "apply#"+strconv.Itoa(n), "save#"+strconv.Itoa(n), "next#"+strconv.Itoa(n))
	}

	for _, kill := range kills {
		t.Run(kill, func(t *testing.T) {
			w := newWorld(7)
			newApp(w, 3, kill)
			c := connect(t, w)

			if _, err := Apply(t.Context(), c, shadowRun(t, c)); !errors.Is(err, errKilled) {
				t.Fatalf("the killed run returned %v", err)
			}

			before := w.states[shadowVersion]

			w.restart()
			newApp(w, 3, "")

			result, err := Apply(t.Context(), c, shadowRun(t, c))
			if err != nil {
				t.Fatalf("the next run: %v", err)
			}

			checkCutOver(t, w)

			if marked := before.Stage != ""; result.Runs[0].Resumed != marked {
				t.Errorf("the next run reported resumed %v over a recorded stage %q", result.Runs[0].Resumed,
					before.Stage)
			}
		})
	}
}

func TestAResumeReadsTheLogFromTheRecordedPositionAndNoLater(t *testing.T) {
	w := newWorld(7)
	newApp(w, 3, "next#5")
	c := connect(t, w)

	if _, err := Apply(t.Context(), c, shadowRun(t, c)); !errors.Is(err, errKilled) {
		t.Fatalf("the killed run returned %v", err)
	}

	recorded := w.states[shadowVersion].Position
	if recorded == "" || recorded == "0" {
		t.Fatalf("the killed run recorded the position %q, and it had applied changes past the start", recorded)
	}

	w.restart()
	newApp(w, 3, "")

	var first string

	opts := shadowRun(t, c)
	opts.Capture = func(ctx context.Context, req capture.Request) (capture.Stream, error) {
		if first == "" {
			first = string(req.From)
		}

		return c.capturer(ctx, req)
	}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("the next run: %v", err)
	}

	if first == "" || first != recorded {
		t.Errorf("the next run opened its stream from %q, and the killed run recorded %q", first, recorded)
	}

	checkCutOver(t, w)
}

func TestAFreshStartDropsTheCopyAStartThatRecordedNothingLeft(t *testing.T) {
	w := newWorld(4)
	newApp(w, 1, "")
	w.tables[shadowCopy] = map[string]string{"999": "left by a killed start"}
	c := connect(t, w)

	if _, err := Apply(t.Context(), c, shadowRun(t, c)); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if _, stale := w.tables[peopleTable]["999"]; stale {
		t.Error("the cutover swapped in a copy holding a row an earlier start left")
	}

	checkCutOver(t, w)
}

func TestResolveRolledBackAbandonsAStoppedMigrationLeavingNoCopyAndNoCapture(t *testing.T) {
	w := newWorld(7)
	newApp(w, 0, "next#4")
	c := connect(t, w)
	opts := shadowRun(t, c)

	if _, err := Apply(t.Context(), c, opts); !errors.Is(err, errKilled) {
		t.Fatalf("the killed run returned %v", err)
	}

	w.restart()

	if len(w.slots) != 1 {
		t.Fatalf("the killed run left the slots %v, want its own", w.slots)
	}

	settled, err := Resolve(t.Context(), c, opts, Repair{Mode: RepairRolledBack, Reason: "abandoned"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if settled.Abandons == nil || settled.Abandons.Shadow != shadowCopy {
		t.Errorf("the resolution reported abandoning %+v", settled.Abandons)
	}

	if _, left := w.tables[shadowCopy]; left || len(w.slots) != 0 || len(w.states) != 0 {
		t.Errorf("abandoning left the tables %v, the slots %v and the states %v", slices.Sorted(maps.Keys(w.tables)),
			w.slots, w.states)
	}

	if !maps.Equal(w.tables[peopleTable], w.truth) {
		t.Error("abandoning changed the table the app writes to")
	}
}

func TestAStoppedShadowMigrationCannotBeAssertedAppliedOrResumedByResolve(t *testing.T) {
	for _, mode := range []RepairMode{RepairApplied, RepairResume} {
		t.Run(string(mode), func(t *testing.T) {
			w := newWorld(5)
			newApp(w, 0, "next#3")
			c := connect(t, w)
			opts := shadowRun(t, c)

			if _, err := Apply(t.Context(), c, opts); !errors.Is(err, errKilled) {
				t.Fatalf("the killed run returned %v", err)
			}

			w.restart()

			reason := ""
			if mode.Asserts() {
				reason = "by hand"
			}

			_, err := Resolve(t.Context(), c, opts, Repair{Mode: mode, Reason: reason})
			if !errors.Is(err, ErrNotResumable) {
				t.Errorf("resolve --%s returned %v", mode, err)
			}

			if len(w.slots) != 1 || w.states[shadowVersion].Stage == "" {
				t.Error("the refused repair changed what the stopped run left")
			}
		})
	}
}

func TestAShadowTableMigrationIsRefusedBeforeAnythingIsCreated(t *testing.T) {
	cases := []struct {
		name  string
		stage func(w *world)
		ops   ir.Operations
		says  string
	}{
		{name: "capture not permitted", stage: func(w *world) { w.permits = false }, says: "binlog_format = ROW"},
		{name: "no strategy", stage: func(w *world) { w.shadower = false }, says: "does not perform shadow-table"},
		{name: "two tables", stage: func(w *world) { w.tables["pets"] = map[string]string{} },
			ops:  ir.Operations{indexPeople(), ir.AddIndex{Table: "pets", Name: "pets_label", Columns: []string{"label"}}},
			says: "changes 2 tables"},
		{name: "a new table", ops: ir.Operations{ir.CreateTable{Table: "pets",
			Columns: []ir.Column{{Name: "id", Type: mustType(t, "int64")}}, PrimaryKey: []string{"id"}}},
			says: "not a change to one existing table"},
		{name: "a dropped key", ops: ir.Operations{ir.DropPrimaryKey{Table: peopleTable,
			From: &ir.ColumnsDef{Columns: []string{"id"}}}}, says: "not a change to one existing table"},
		{name: "the retired name held", stage: func(w *world) { w.tables[shadowRetired] = map[string]string{} },
			says: "already exists"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(3)
			if tc.stage != nil {
				tc.stage(w)
			}

			tables := slices.Sorted(maps.Keys(w.tables))
			c := connect(t, w)

			_, err := Apply(t.Context(), c, shadowRun(t, c, tc.ops...))
			if !errors.Is(err, ErrCannotShadow) || !strings.Contains(err.Error(), tc.says) {
				t.Fatalf("Apply returned %v, want a refusal saying %q", err, tc.says)
			}

			if slices.Contains(w.calls, "capture") || len(w.slots) != 0 || len(w.states) != 0 ||
				!slices.Equal(slices.Sorted(maps.Keys(w.tables)), tables) {
				t.Errorf("the refusal came after something was created: calls %v, slots %v", w.calls, w.slots)
			}
		})
	}
}

func TestCtrlCDuringTheHoldDetachesKeepingTheCopyAndTheCapture(t *testing.T) {
	w := newWorld(5)
	app := newApp(w, 0, "")
	c := connect(t, w)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	opts := shadowRun(t, c)
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding && app.polls == 3 {
			cancel()
		}
	}

	if _, err := Apply(ctx, c, opts); !errors.Is(err, context.Canceled) {
		t.Fatalf("the canceled run returned %v", err)
	}

	if !w.slots[shadowCapture] || w.tables[shadowCopy] == nil || w.states[shadowVersion].Stage != history.StageSyncing {
		t.Fatalf("Ctrl-C left the slots %v, the state %+v", w.slots, w.states[shadowVersion])
	}

	if slices.Contains(w.calls, "drop-stream "+shadowCapture) || !slices.Contains(w.calls, "close "+shadowCapture) {
		t.Errorf("Ctrl-C released the capture rather than detaching from it: %v", w.calls)
	}

	w.restart()
	newApp(w, 2, "")

	if _, err := Apply(t.Context(), c, shadowRun(t, c)); err != nil {
		t.Fatalf("the next run: %v", err)
	}

	checkCutOver(t, w)
}

func TestMigrationTimeoutBoundsTheCopyAndNeverTheHold(t *testing.T) {
	w := newWorld(3)
	app := newApp(w, 6, "")
	c := connect(t, w)

	opts := shadowRun(t, c)
	opts.MigrationTimeout = 200 * time.Millisecond
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }
	opts.Shadowed = func(p ShadowProgress) {
		if p.Holding && app.polls == 2 {
			time.Sleep(300 * time.Millisecond)
		}
	}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("a hold longer than migration_timeout ended the run: %v", err)
	}

	checkCutOver(t, w)
}

func TestAPreviewNamesWhatAShadowTableMigrationCreatesAndCostsTheRenameAlone(t *testing.T) {
	w := newWorld(3)
	c := connect(t, w)

	previewed, err := Preview(t.Context(), c, shadowRun(t, c), ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	planned := previewed.Steps[0]
	if planned.Shadow == nil || planned.Shadow.Shadow != shadowCopy || planned.Shadow.Retired != shadowRetired ||
		planned.Shadow.Capture != shadowCapture || planned.Shadow.Refused != nil {
		t.Fatalf("the preview reported %+v", planned.Shadow)
	}

	if lock := planned.Plan.Steps[0].Lock(); lock != capability.BriefExclusive {
		t.Errorf("a shadowed step costs %s, and its one lock is the cutover's rename", lock)
	}

	if slices.Contains(w.calls, "capture") || len(w.tables) != 1 {
		t.Errorf("the preview created something: %v", w.calls)
	}
}

func TestAPreviewSaysWhyAServerCannotCapture(t *testing.T) {
	w := newWorld(3)
	w.permits = false
	c := connect(t, w)

	previewed, err := Preview(t.Context(), c, shadowRun(t, c), ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if refused := previewed.Steps[0].Shadow.Refused; !errors.Is(refused, ErrCannotShadow) ||
		!strings.Contains(refused.Error(), "binlog_format = ROW") {
		t.Errorf("the preview reported the refusal as %v", refused)
	}
}

func TestDerivedNamesFitEveryEngineAndAreTheMigrationsOwn(t *testing.T) {
	long := strings.Repeat("t", 60)

	for _, name := range []string{ShadowName(long, shadowVersion), RetiredName(long, shadowVersion),
		CaptureName(long, shadowVersion)} {
		if len(name) > identifierLimit {
			t.Errorf("%q is %d bytes, beyond every engine's %d", name, len(name), identifierLimit)
		}
	}

	if ShadowName(peopleTable, versionAt(0)) == ShadowName(peopleTable, versionAt(1)) ||
		RetiredName(peopleTable, versionAt(0)) == RetiredName(peopleTable, versionAt(1)) {
		t.Error("two migrations of one table derive one name, so a leftover could not be told whose it is")
	}

	if ShadowName(long, shadowVersion) == RetiredName(long, shadowVersion) {
		t.Error("a shortened copy and retired name collide")
	}
}

func TestARollbackOfACutOverMigrationRunsItsInverseRatherThanASecondShadow(t *testing.T) {
	w := newWorld(3)
	newApp(w, 1, "")
	c := connect(t, w)
	opts := shadowRun(t, c)
	opts.Source.(*source).down[shadowVersion] = ir.Operations{ir.Raw{Driver: "world", SQL: "drop people_label"}}

	if _, err := Apply(t.Context(), c, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	before := len(w.calls)

	if _, err := Rollback(t.Context(), c, opts); err != nil {
		t.Fatalf("Rollback: %v", err)
	}

	for _, call := range w.calls[before:] {
		if call == "capture" || strings.HasPrefix(call, "exec create") {
			t.Errorf("the rollback reached for a shadow table: %v", w.calls[before:])
		}
	}

	if got := rowOutcomes(w); got[len(got)-1] != "applied" || w.runs[len(w.runs)-1].Direction != ir.Down {
		t.Errorf("the history reads %v, want the rollback recorded", got)
	}
}

func TestTheRecordedPositionIsExactlyWhatWasAppliedWhenARunIsKilled(t *testing.T) {
	for n := 2; n <= 9; n++ {
		t.Run("next#"+strconv.Itoa(n), func(t *testing.T) {
			w := newWorld(7)
			newApp(w, 0, "next#"+strconv.Itoa(n))
			c := connect(t, w)

			if _, err := Apply(t.Context(), c, shadowRun(t, c)); !errors.Is(err, errKilled) {
				t.Fatalf("the killed run returned %v", err)
			}

			if got := w.states[shadowVersion].Position; got != strconv.Itoa(w.read) {
				t.Errorf("the run applied the log through %d and recorded the position %q", w.read, got)
			}
		})
	}
}

func TestTheRowsCopiedAreCountedOnceWhereverTheCopyIsKilled(t *testing.T) {
	for n := 1; n <= 4; n++ {
		t.Run("save#"+strconv.Itoa(n), func(t *testing.T) {
			w := newWorld(7)
			w.at = func(point string) {}
			kills := 0
			w.at = func(point string) {
				if point == "save" {
					kills++
					if kills == n {
						w.killed = true
					}
				}
			}

			c := connect(t, w)

			if _, err := Apply(t.Context(), c, shadowRun(t, c)); !errors.Is(err, errKilled) {
				t.Fatalf("the killed run returned %v", err)
			}

			w.restart()
			w.at = func(string) { w.confirm() }

			result, err := Apply(t.Context(), c, shadowRun(t, c))
			if err != nil {
				t.Fatalf("the next run: %v", err)
			}

			if copied := result.Runs[0].Shadow.Copied; copied != 7 {
				t.Errorf("the copy of 7 rows reports %d copied after a kill at save %d", copied, n)
			}
		})
	}
}
