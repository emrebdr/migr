package zerodowntime

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/capture"
	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// warmUp is how long the load runs before a window opens, so every client is already waiting on the server.
const warmUp = 200 * time.Millisecond

// confirmPoll is how often the confirming session reads whether the copy is held.
const confirmPoll = 200 * time.Millisecond

// floorFor is how long the load runs alone, which is what the server's own noise floor is read over.
const floorFor = 2 * time.Second

// settle is how long the load runs before the floor is read, while a server flushes the seed it just took.
// Measured, mysql80gtid's worst query straight after the seed was 638ms, and under 30ms a run later.
const settle = 2 * time.Second

// minWindow is the shortest a direction's window is, so an operation done between two queries still overlaps some.
const minWindow = 50 * time.Millisecond

// Suite is one driver against one server, with the load its own test spells.
type Suite struct {
	// Driver is the driver under test, which compiles every operation.
	Driver driver.Driver
	// Open opens a fresh session recording its runs in the history table named, as migr up opens one.
	Open func(t *testing.T, historyTable string) driver.Conn
	// Load is every statement a client sends, spelled for this engine.
	Load Load
	// Prefix keeps this run's tables apart from every other suite's, and must be short.
	Prefix string
	// Capture opens a change stream where the server permits one, and is nil where the shadow case is skipped.
	Capture func(ctx context.Context, req capture.Request) (capture.Stream, error)
	// ShadowOnly runs the shadow-table cases alone, on a server there for its change stream whose sibling runs the rest.
	ShadowOnly bool
}

// run is one Suite's run: what it was given, and every kind it has judged or seen honestly refused.
type run struct {
	// s is the suite.
	s Suite
	// budget is the longest a promised-free client query may wait.
	budget time.Duration
	// ty are the cases' logical types.
	ty types
	// n are the tables.
	n names
	// reached are the kinds a measured direction ran, or the server declared unsupported.
	reached map[ir.Kind]bool
	// ran counts the cases -run did not filter out, so a filtered run is not judged on coverage.
	ran int
}

// Run seeds the loaded table, reads the server's noise floor, runs every case both ways, and checks coverage.
func (s Suite) Run(t *testing.T) {
	t.Helper()

	if s.Driver == nil || s.Open == nil || s.Load == nil || s.Prefix == "" {
		t.Fatal("the suite needs a driver, a way to open a session, a load and a prefix")
	}

	budget, err := budgetFrom(os.Getenv(BudgetVariable))
	if err != nil {
		t.Fatal(err)
	}

	ty, err := readTypes()
	if err != nil {
		t.Fatal(err)
	}

	r := &run{s: s, budget: budget, ty: ty, reached: map[ir.Kind]bool{},
		n: names{main: s.Prefix + "_m", side: s.Prefix + "_s", other: s.Prefix + "_o"}}

	r.prepare(t)

	cases := s.cases()
	for _, sc := range cases {
		t.Run(sc.name, func(t *testing.T) {
			r.runCase(t, sc)
		})
	}

	if s.ShadowOnly {
		return
	}

	if r.ran < len(cases) {
		t.Logf("-run filtered out %d of %d cases, so coverage is not judged", len(cases)-r.ran, len(cases))

		return
	}

	for _, missing := range unreached(r.reached) {
		t.Errorf("no case ran %s on %s, so its verdict was never held to a client's wait", missing, s.Driver.Name())
	}
}

// cases are the scenarios this suite runs: every one, or the shadow-table ones where it is ShadowOnly.
func (s Suite) cases() []scenario {
	if !s.ShadowOnly {
		return scenarios()
	}

	var online []scenario

	for _, sc := range scenarios() {
		if sc.online {
			online = append(online, sc)
		}
	}

	return online
}

// seeded is how many rows the loaded table holds: SeededRows, or SideRows where no case but the shadow ones reads it.
func (s Suite) seeded() int {
	if s.ShadowOnly {
		return SideRows
	}

	return SeededRows
}

// unreached lists every kind no case reached, in the IR's own order.
func unreached(reached map[ir.Kind]bool) []ir.Kind {
	var missing []ir.Kind

	for _, kind := range ir.Kinds() {
		if !reached[kind] {
			missing = append(missing, kind)
		}
	}

	return missing
}

// prepare creates and seeds the loaded table, then holds the server's own noise floor to the budget.
func (r *run) prepare(t *testing.T) {
	t.Helper()

	ctx := t.Context()
	if err := r.s.Load.Drop(ctx, r.n.main); err != nil {
		t.Fatalf("dropping a previous run's table: %v", err)
	}

	t.Cleanup(func() { _ = r.s.Load.Drop(context.WithoutCancel(ctx), r.n.main) })

	conn := r.s.Open(t, r.s.Prefix+"_base")
	r.create(t, conn, tableOf(r.n.main, r.ty, true))
	r.create(t, conn, ir.AddIndex{Table: r.n.main, Name: r.s.Prefix + "_label_idx", Columns: []string{"label"}})
	r.create(t, conn, ir.AddIndex{Table: r.n.main, Name: r.s.Prefix + "_pad_idx", Columns: []string{"pad"}})

	if err := r.s.Load.Seed(ctx, r.n.main, r.s.seeded()); err != nil {
		t.Fatalf("seeding %s with %d rows: %v", r.n.main, r.s.seeded(), err)
	}

	load := startLoad(ctx, r.s.Load, r.n.main, r.s.seeded(), sinceEpoch)

	time.Sleep(settle)

	from := sinceEpoch()

	time.Sleep(floorFor)

	floor, query, count := load.worst(from, sinceEpoch())
	if failures, _ := load.halt(); len(failures) > 0 {
		t.Fatalf("the load failed with nothing running: %v", errors.Join(failures...))
	}

	t.Logf("%s's noise floor: the worst of %d client queries took %s (%s), against a %s budget",
		r.s.Driver.Name(), count, floor, query, r.budget)

	if floor >= r.budget {
		t.Fatalf("with nothing running a client query took %s, past the %s budget, so no operation can be "+
			"judged on this server -- raise %s", floor, r.budget, BudgetVariable)
	}
}

// create compiles one operation and runs its statements outside any migration, which a fixture needs no history for.
func (r *run) create(t *testing.T, conn driver.Conn, op ir.Operation) {
	t.Helper()

	stmts, err := r.s.Driver.Compile(op)
	if err != nil {
		t.Fatalf("compiling %s: %v", op.Kind(), err)
	}

	for _, stmt := range stmts {
		if err := conn.Exec(t.Context(), stmt); err != nil {
			t.Fatalf("running %s: %v", op.Kind(), err)
		}
	}
}

// leftovers are every table a case can leave, which it drops before it starts and after it ends.
func (r *run) leftovers(historyTable string, change migration.Migration) []string {
	return []string{r.n.side, r.n.other, historyTable, history.ShadowTable(historyTable),
		history.ProgressTable(historyTable), history.ReleaseTable(historyTable),
		engine.ShadowName(r.n.side, change.Version), engine.RetiredName(r.n.side, change.Version)}
}

// runCase applies a case's setup, measures its change up and down, and puts everything back.
func (r *run) runCase(t *testing.T, sc scenario) {
	r.ran++

	if sc.online && r.s.Capture == nil {
		t.Skip("the server permits no log-based capture, so it runs no shadow-table migration")
	}

	src := r.staged(sc)

	if sc.rewrittenOnly && !r.rewrites(t, src.change) {
		t.Skipf("%s performs this change as declared, which the side-table case already holds", r.s.Driver.Name())
	}

	files, change, err := src.files()
	if err != nil {
		t.Fatal(err)
	}

	historyTable := r.s.Prefix + "_h"
	leftovers := r.leftovers(historyTable, change)
	r.drop(t, leftovers)
	t.Cleanup(func() { r.drop(t, leftovers) })

	conn := r.s.Open(t, historyTable)

	var online migration.Online
	if sc.online {
		online = migration.Online{change.Version: migration.ShadowTable}
	}

	opts := optionsFor(r.s.Driver, conn, src, files, online, r.s.Capture)

	switch {
	case sc.unattended:
		opts = unattended(t, opts)
	case sc.online:
		r.confirmWhenHeld(t, historyTable, change.Version)
	}

	if sc.side {
		r.create(t, conn, tableOf(r.n.side, r.ty, !sc.keyless))

		if err := r.s.Load.Seed(t.Context(), r.n.side, SideRows); err != nil {
			t.Fatalf("seeding %s: %v", r.n.side, err)
		}
	}

	if len(src.setup) > 0 {
		r.setUp(t, conn, opts)
	}

	if !r.measure(t, conn, opts, sc, ir.Up) {
		return
	}

	if sc.online {
		if _, err := engine.Retire(t.Context(), conn, opts, change.Version); err != nil {
			t.Fatalf("retiring the original: %v", err)
		}

		return
	}

	r.measure(t, conn, opts, sc, ir.Down)

	if len(src.setup) > 0 && !sc.side {
		opts.Scope = engine.Steps(1)
		if _, err := engine.Rollback(t.Context(), conn, opts); err != nil {
			t.Fatalf("undoing the setup, which the next case needs gone: %v", err)
		}
	}
}

// unattended has a run cut over by itself, at the lag migr up's default gives the suite's lock wait, logging each held read's lag.
func unattended(t *testing.T, opts engine.Options) engine.Options {
	t.Helper()

	wait, err := LockWait()
	if err != nil {
		t.Fatal(err)
	}

	opts.Unattended = true
	opts.CutoverLag = min(config.DefaultCutoverLag, wait/2)
	opts.Shadowed = func(p engine.ShadowProgress) {
		if p.Holding {
			t.Logf("held read: %d changes, %s behind", p.Batch, p.Behind)
		}
	}

	return opts
}

// confirmWhenHeld confirms a shadow-table migration's cutover from a second session once it holds, as an operator would.
func (r *run) confirmWhenHeld(t *testing.T, historyTable, version string) {
	t.Helper()

	store, shadows := r.s.Open(t, historyTable).History().(history.Shadows)
	if !shadows {
		t.Fatalf("%s keeps no shadow-table state", r.s.Driver.Name())
	}

	ctx, stop := context.WithCancel(context.WithoutCancel(t.Context()))
	done := make(chan struct{})

	t.Cleanup(func() {
		stop()
		<-done
	})

	go func() {
		defer close(done)

		for ctx.Err() == nil {
			states, err := store.ShadowStates(ctx)
			if state, found := history.FindShadow(states, version); err == nil && found &&
				state.Stage == history.StageSyncing {
				_ = store.ConfirmShadow(ctx, version)

				return
			}

			time.Sleep(confirmPoll)
		}
	}()
}

// rewrites reports whether a session of this driver performs every operation of a change as a safer sequence.
func (r *run) rewrites(t *testing.T, change ir.Operations) bool {
	t.Helper()

	rewriter, rewrites := r.s.Open(t, r.s.Prefix+"_probe").(driver.Rewriter)
	if !rewrites {
		return false
	}

	for _, op := range change {
		if _, rewritten := rewriter.Rewrite(op); !rewritten {
			return false
		}
	}

	return true
}

// LockWait is the statement lock wait a driver's sessions run the suite with: four fifths of the budget.
func LockWait() (time.Duration, error) {
	budget, err := budgetFrom(os.Getenv(BudgetVariable))
	if err != nil {
		return 0, err
	}

	return budget * 4 / 5, nil
}

// staged builds a case's migrations, a side case's setup creating its own table first.
func (r *run) staged(sc scenario) staged {
	var setup ir.Operations

	if sc.setup != nil {
		setup = append(setup, sc.setup(r.n, r.ty)...)
	}

	src := staged{setup: setup, change: sc.change(r.n, r.ty, r.s.Load, r.s.Driver.Name()), authored: sc.authored}
	if sc.down != nil {
		src.down = sc.down(r.n, r.s.Load, r.s.Driver.Name())
	}

	if sc.undo != nil {
		src.undo = sc.undo(r.n, r.ty)
	}

	return src
}

// setUp applies a case's setup migration, unmeasured.
func (r *run) setUp(t *testing.T, conn driver.Conn, opts engine.Options) {
	t.Helper()

	setup, err := migration.Parse(setupFile)
	if err != nil {
		t.Fatal(err)
	}

	opts.Scope = engine.To(setup.Version)
	if _, err := engine.Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("applying the setup: %v", err)
	}
}

// drop drops what a case leaves, on a context that outlives a failed test.
func (r *run) drop(t *testing.T, tables []string) {
	t.Helper()

	if err := r.s.Load.Drop(context.WithoutCancel(t.Context()), tables...); err != nil {
		t.Fatalf("dropping %s: %v", strings.Join(tables, ", "), err)
	}
}

// measure previews one direction, runs it under load, and judges it, reporting whether it ran.
func (r *run) measure(t *testing.T, conn driver.Conn, opts engine.Options, sc scenario, dir ir.Direction) bool {
	t.Helper()

	opts.Scope = engine.All()
	if dir == ir.Down {
		opts.Scope = engine.Steps(1)
	}

	previewed, err := engine.Preview(t.Context(), conn, opts, dir)
	if refusal := (*driver.UnsupportedError)(nil); errors.As(err, &refusal) {
		r.reach([]ir.Kind{refusal.Kind})
		t.Logf("%s %s: the compiler refuses it on %s (%v), which is honest", sc.name, dir, r.s.Driver.Name(), err)

		return false
	}

	if err != nil {
		t.Fatalf("previewing %s %s: %v", sc.name, dir, err)
	}

	declared, kinds, refused := declaredOf(previewed)
	r.reach(kinds)

	if refused != "" {
		t.Logf("%s %s: declared refused on %s (%s), which is honest", sc.name, dir, r.s.Driver.Name(), refused)

		return false
	}

	m, failures, runErr := r.underLoad(t, sc, dir, conn, opts)
	if runErr != nil {
		t.Fatalf("running %s %s: %v", sc.name, dir, runErr)
	}

	if len(failures) > 0 {
		t.Errorf("%s %s: a client query failed while it ran: %v", sc.name, dir, errors.Join(failures...))
	}

	m.Declared = declared

	v := judge(m, r.budget)
	if v.Failed != "" {
		t.Error(v.Failed)
	}

	if v.Note != "" {
		t.Log(v.Note)
	}

	return true
}

// underLoad runs one direction while the load works the case's table, and measures the clients' worst wait.
func (r *run) underLoad(t *testing.T, sc scenario, dir ir.Direction, conn driver.Conn, opts engine.Options,
) (measured, []error, error) {
	rows := SeededRows
	if sc.side {
		rows = SideRows
	}

	load := startLoad(t.Context(), r.s.Load, sc.loaded(r.n), rows, sinceEpoch)

	time.Sleep(warmUp)

	from := sinceEpoch()

	var err error
	if dir == ir.Up {
		err = r.apply(t, sc, conn, opts)
	} else {
		_, err = engine.Rollback(t.Context(), conn, opts)
	}

	if ran := sinceEpoch() - from; ran < minWindow {
		time.Sleep(minWindow - ran)
	}

	to := sinceEpoch()

	worst, query, count := load.worst(from, to)
	failures, written := load.halt()

	if err == nil && sc.online {
		r.checkNoWriteLost(t, rows, written)
	}

	return measured{Case: sc.name, Direction: dir, Worst: worst, Query: query, Queries: count}, failures, err
}

// apply runs a case up, holding an unattended shadow case to cutting over by itself within unattendedBound.
func (r *run) apply(t *testing.T, sc scenario, conn driver.Conn, opts engine.Options) error {
	t.Helper()

	ctx := t.Context()

	if sc.unattended {
		var stop context.CancelFunc

		ctx, stop = context.WithTimeout(ctx, unattendedBound)
		defer stop()
	}

	result, err := engine.Apply(ctx, conn, opts)
	if err != nil {
		if sc.unattended && errors.Is(err, engine.ErrAwaitingCutover) {
			return fmt.Errorf("an unattended run under the load did not cut over within %s: %w", unattendedBound, err)
		}

		return err
	}

	if sc.unattended {
		for _, ran := range result.Runs {
			if ran.Shadow != nil {
				t.Logf("%s: cut over unattended %s behind the log", sc.name, ran.Shadow.Behind)
			}
		}
	}

	return nil
}

// checkNoWriteLost holds a shadowed table to every committed write: a write the cutover let into the retired original is lost.
func (r *run) checkNoWriteLost(t *testing.T, rows int, written int64) {
	t.Helper()

	got, err := r.s.Load.Sum(t.Context(), r.n.side)
	if err != nil {
		t.Fatalf("summing %s: %v", r.n.side, err)
	}

	if want := int64(rows)*int64(rows+1)/2 + written; got != want {
		t.Errorf("after the cutover %s sums to %d, want %d: %d committed writes are missing from it, "+
			"landed in the retired original or lost", r.n.side, got, want, want-got)
	}
}

// reach records kinds as judged or honestly refused on this server.
func (r *run) reach(kinds []ir.Kind) {
	for _, kind := range kinds {
		r.reached[kind] = true
	}
}

// declaredOf reads a preview's worst step cost, the kinds it runs, and why the server refuses it, empty where it does not.
func declaredOf(p engine.Previewed) (capability.LockImpact, []ir.Kind, string) {
	var (
		declared capability.LockImpact
		kinds    []ir.Kind
		refused  []string
	)

	for _, planned := range p.Steps {
		if planned.Shadow != nil && planned.Shadow.Refused != nil {
			refused = append(refused, planned.Shadow.Refused.Error())
		}

		for _, step := range planned.Plan.Steps {
			kinds = append(kinds, step.Op.Kind())

			if !step.Verdict.Allowed() {
				refused = append(refused, fmt.Sprintf("%s: %s", step.Op.Kind(), step.Verdict.Reason()))

				continue
			}

			declared = worse(declared, step.Lock())
		}
	}

	return declared, slices.Compact(kinds), strings.Join(refused, "; ")
}
