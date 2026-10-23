package engine

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/remedy"
	"github.com/emrebdr/migr/internal/schema"
)

// keyedTable is the table every chunked fixture writes, keyed by one integer column.
const keyedTable = "people"

// keyed is a table of rows keyed 1 through n that a fake chunker pages through as a driver would.
type keyed struct {
	// session is the recorder every chunk is noted on, so a test can see where it ran.
	session *recorder
	// rows is how many rows the table holds.
	rows int
	// failAt is the chunk, counting from one across every run, that fails; zero fails none.
	failAt int
	// busyAt is the chunk, counting from one across every run, that gives up on a lock; zero none.
	busyAt int
	// slowAt is the chunk, counting from one across every run, that runs until it is stopped.
	slowAt int
	// served counts the chunks asked for, across every run that shares this table.
	served int
	// slices is every slice a chunk was asked for, in order.
	slices []driver.Slice
	// writes counts how many times each key was written, which exactly-once is held to.
	writes map[int]int
}

// newKeyed stages a table of rows on a session, and the live schema that says it has a key.
func newKeyed(conn *recorder, rows int) *keyed {
	conn.live = keyedSnapshot(true)

	return &keyed{session: conn, rows: rows, writes: map[int]int{}}
}

// keyedSnapshot is the live schema the chunked fixtures run against, the keyed table's key optional.
func keyedSnapshot(withKey bool) *schema.Snapshot {
	text, _ := schema.Parse("string(64)")
	exact, _ := schema.ExactType(text, "varchar(64)")
	integer, _ := schema.Parse("int64")
	id, _ := schema.ExactType(integer, "bigint")

	table := schema.Table{Name: keyedTable, Columns: []schema.Column{
		{Name: "id", Type: id},
		{Name: "nickname", Type: exact, Nullable: true},
		{Name: "email", Type: exact},
	}}

	if withKey {
		table.PrimaryKey = &schema.PrimaryKey{Columns: []string{"id"}}
	}

	staged := schema.Table{Name: stagedTable, Columns: []schema.Column{{Name: "id", Type: id}}}

	return &schema.Snapshot{Tables: []schema.Table{table, staged}}
}

// Chunk covers the rows after the cursor as a driver does: done only where fewer than asked remain.
func (k *keyed) Chunk(ctx context.Context, s driver.Slice) (driver.Sliced, error) {
	k.session.note(k.describeChunk())
	k.served++

	if k.served == k.slowAt {
		<-ctx.Done()

		return driver.Sliced{}, fmt.Errorf("the fake server stopped the chunk: %w", ctx.Err())
	}

	if k.served == k.failAt {
		return driver.Sliced{}, errRefused
	}

	if k.served == k.busyAt {
		return driver.Sliced{}, fmt.Errorf("%w: the fake's row lock is held", driver.ErrStatementLockTimeout)
	}

	k.slices = append(k.slices, s)

	after := 0
	if len(s.After) > 0 {
		after, _ = strconv.Atoi(s.After[0])
	}

	remaining := k.rows - after
	if remaining < s.Rows {
		k.wrote(after, k.rows)

		return driver.Sliced{Written: int64(remaining), Last: s.After, Done: true}, nil
	}

	k.wrote(after, after+s.Rows)

	return driver.Sliced{Written: int64(s.Rows), Last: []string{strconv.Itoa(after + s.Rows)}}, nil
}

// describeChunk names the chunk call and whether it ran inside a transaction, as every write is noted.
func (k *keyed) describeChunk() string {
	if k.session.inTx {
		return "chunk"
	}

	return "chunk-outside-any-transaction"
}

// wrote counts a write of every key after one and up to another.
func (k *keyed) wrote(after, through int) {
	for key := after + 1; key <= through; key++ {
		k.writes[key]++
	}
}

// writtenOnce reports whether every row was written exactly once, which a resume must leave.
func (k *keyed) writtenOnce() bool {
	for key := 1; key <= k.rows; key++ {
		if k.writes[key] != 1 {
			return false
		}
	}

	return len(k.writes) == k.rows
}

// SaveCheckpoint replaces one migration's checkpoint, noting whether a transaction was open.
func (j *journal) SaveCheckpoint(_ context.Context, cp history.Checkpoint) error {
	j.session.note(j.describeWrite("save-checkpoint", history.Record{Version: cp.Version}))

	if err := cp.Validate(); err != nil {
		return err
	}

	j.cps = append(slices.DeleteFunc(j.cps, func(held history.Checkpoint) bool {
		return held.Version == cp.Version && held.Direction == cp.Direction
	}), cp)

	return nil
}

// Checkpoints returns every checkpoint the journal holds.
func (j *journal) Checkpoints(context.Context) ([]history.Checkpoint, error) {
	return slices.Clone(j.cps), nil
}

// ClearCheckpoint removes one migration's checkpoint, noting whether a transaction was open.
func (j *journal) ClearCheckpoint(_ context.Context, version string, dir ir.Direction) error {
	j.session.note(j.describeWrite("clear-checkpoint", history.Record{Version: version}))

	j.cps = slices.DeleteFunc(j.cps, func(held history.Checkpoint) bool {
		return held.Version == version && held.Direction == dir
	})

	return nil
}

// compile-time proof that the journal remembers how far a chunked operation got.
var _ history.Checkpointer = (*journal)(nil)

// paces is a pacer that hands out a scripted pace per chunk and remembers what it was shown.
type paces struct {
	// script is the pace for each chunk in turn, the last repeated once it runs out.
	script []Pace
	// seen is the progress each call was given, which is what the last chunk left.
	seen []Progress
}

// Pace returns the next scripted pace.
func (p *paces) Pace(_ context.Context, done Progress) (Pace, error) {
	p.seen = append(p.seen, done)

	return p.script[min(len(p.seen), len(p.script))-1], nil
}

// waits records every wait a run asked for instead of waiting it.
type waits struct {
	// asked is every wait, in order.
	asked []time.Duration
	// cancel ends the run's context at the wait numbered here, counting from one; zero never.
	cancel int
	// stop is what ends it.
	stop context.CancelFunc
}

// sleep records one wait, canceling the run when the script says to.
func (w *waits) sleep(ctx context.Context, d time.Duration) error {
	w.asked = append(w.asked, d)

	if len(w.asked) == w.cancel {
		w.stop()
	}

	return ctx.Err()
}

// nicknames is the data operation the chunked fixtures run, filling the nicknames nobody set.
func nicknames(literal string) ir.DataBackfill {
	text, _ := schema.Parse("string(64)")

	return ir.DataBackfill{Table: keyedTable, Column: "nickname", Type: text,
		Value: &ir.Default{Literal: &literal}}
}

// chunked stages a data-only migration and an index after it, run in chunks of the given size.
func chunked(t *testing.T, rows int, size int) (Options, *source, *recorder, *keyed) {
	t.Helper()

	opts, src := staged(t, "", "a")
	src.ops[versionAt(0)] = ir.Operations{nicknames("anonymous")}

	conn := newRecorder(t, true)
	table := newKeyed(conn, rows)

	opts.Planner.ChunkRows = size
	opts.Chunker = table
	opts.Pacer = Steady{Rows: size}
	opts.Sleep = func(ctx context.Context, _ time.Duration) error { return ctx.Err() }

	return opts, src, conn, table
}

// outcomes renders what each history row recorded, version and outcome, in the order written.
func outcomes(runs []history.Record) []string {
	rendered := make([]string, 0, len(runs))
	for _, rec := range runs {
		rendered = append(rendered, rec.Version+" "+string(rec.Outcome)+" "+
			strconv.Itoa(int(rec.Ordinal)))
	}

	return rendered
}

func TestADataOnlyMigrationRunsInChunksTheSizeAndPaceItsPacerSays(t *testing.T) {
	opts, _, conn, table := chunked(t, 25, 10)

	pacer := &paces{script: []Pace{
		{Rows: 10},
		{Rows: 5, Wait: 3 * time.Second, Throttle: "replica 9s behind"},
		{Rows: 20},
	}}
	slept := &waits{}
	var reported []Progress

	opts.Pacer, opts.Sleep = pacer, slept.sleep
	opts.Progress = func(p Progress) { reported = append(reported, p) }

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	sizes := make([]int, 0, len(table.slices))
	for _, s := range table.slices {
		sizes = append(sizes, s.Rows)
	}

	if !slices.Equal(sizes, []int{10, 5, 20}) {
		t.Errorf("the chunks covered %v rows, want the pacer's 10, 5 and 20", sizes)
	}

	if got := table.slices[2].After; !slices.Equal(got, []string{"15"}) {
		t.Errorf("the third chunk started after %v, want after the second's last key 15", got)
	}

	if !slices.Equal(slept.asked, []time.Duration{0, 3 * time.Second, 0}) {
		t.Errorf("the run waited %v, want each pace's own wait before its chunk", slept.asked)
	}

	if len(reported) != 3 || reported[1].Throttle != "replica 9s behind" || !reported[2].Done {
		t.Fatalf("progress was reported as %+v, want three chunks, the second throttled and the "+
			"third done", reported)
	}

	if reported[2].Written != 25 || reported[2].Chunks != 3 || reported[2].Table != keyedTable {
		t.Errorf("the last progress says %d rows in %d chunks of %q, want 25 in 3 of %q",
			reported[2].Written, reported[2].Chunks, reported[2].Table, keyedTable)
	}

	if pacer.seen[1].Chunks != 1 || !slices.Equal(pacer.seen[1].Cursor, []string{"10"}) {
		t.Errorf("the pacer was shown %+v before the second chunk, want what the first left",
			pacer.seen[1])
		t.Log("The pacer is the seam M13-T8 drives from replica lag, so what it is shown before a")
		t.Log("chunk has to be what the chunks before it committed, or it paces against nothing.")
	}
}

func TestTheFirstChunkMarksTheRunDirtyAndTheLastRecordsItApplied(t *testing.T) {
	opts, _, conn, table := chunked(t, 25, 10)

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{
		versionAt(0) + " dirty 1", versionAt(0) + " applied 2", versionAt(1) + " applied 3",
	}
	if got := outcomes(conn.store.runs); !slices.Equal(got, want) {
		t.Errorf("the history holds %q, want %q", got, want)
		t.Log("The dirty row goes in with the first chunk, so a run killed between two chunks leaves")
		t.Log("the evidence a failed one does -- and the applied row takes the next ordinal, which the")
		t.Log("migration after it then moves one past.")
	}

	if len(conn.store.cps) != 0 {
		t.Errorf("the checkpoint survived the run that finished: %+v", conn.store.cps)
	}

	for _, call := range conn.calls {
		if strings.Contains(call, "outside-any-transaction") {
			t.Errorf("%q ran outside the chunk's transaction, so a crash between the two would "+
				"leave rows the checkpoint does not account for", call)
		}
	}

	if len(table.slices) != 3 || result.Runs[0].Chunks != 3 || result.Runs[0].Written != 25 {
		t.Errorf("the run reported %d chunks writing %d rows over %d slices, want 3, 25 and 3",
			result.Runs[0].Chunks, result.Runs[0].Written, len(table.slices))
	}
}

func TestATableOneChunkCoversWritesNoDirtyRow(t *testing.T) {
	opts, _, conn, _ := chunked(t, 5, 10)

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	want := []string{versionAt(0) + " applied 1", versionAt(1) + " applied 2"}
	if got := outcomes(conn.store.runs); !slices.Equal(got, want) {
		t.Errorf("the history holds %q, want %q", got, want)
		t.Log("One chunk is the whole run, so it commits with its applied row and never leaves the")
		t.Log("database between states -- a dirty row there would be noise in every small table's log.")
	}
}

func TestAnInterruptedChunkedRunResumesAfterTheLastCommittedChunk(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	_, err := Apply(t.Context(), conn, opts)

	var stopped *DirtyError
	if !errors.As(err, &stopped) || stopped.Checkpoint == nil {
		t.Fatalf("a chunk failing partway reported %v, want a DirtyError carrying the checkpoint", err)
	}

	if stopped.Checkpoint.Chunks != 2 || !slices.Equal(stopped.Checkpoint.Cursor, []string{"20"}) {
		t.Errorf("the checkpoint says %+v, want 2 chunks through key 20", *stopped.Checkpoint)
	}

	if !strings.Contains(remedy.Of(err), "run migr up again to carry on") {
		t.Errorf("the failure does not say what carries the run on: %q", remedy.Of(err))
	}

	result, err := Apply(t.Context(), conn, opts)
	if err != nil {
		t.Fatalf("resuming: %v", err)
	}

	if got := table.slices[2].After; !slices.Equal(got, []string{"20"}) {
		t.Errorf("the resumed run started after %v, want after the checkpoint's key 20", got)
	}

	if !table.writtenOnce() {
		t.Errorf("rows were written %v times each, want every one of the 50 exactly once",
			table.writes)
		t.Log("A data_copy's rows cannot be told from the rows already there, so a resume that")
		t.Log("restarted, or re-ran the chunk the checkpoint names, duplicates them.")
	}

	if len(conn.store.cps) != 0 || !result.Runs[0].Resumed {
		t.Errorf("after the resume the checkpoints are %+v and the run reports resumed %v",
			conn.store.cps, result.Runs[0].Resumed)
	}

	if last := conn.store.runs[len(conn.store.runs)-2]; last.Outcome != history.Applied ||
		last.Version != versionAt(0) {
		t.Errorf("the resumed migration's last row is %s %s, want it applied", last.Version,
			last.Outcome)
	}
}

func TestACanceledRunIsRecordedDirtyOnAContextThatOutlivesTheCancel(t *testing.T) {
	opts, _, conn, _ := chunked(t, 50, 10)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	slept := &waits{cancel: 3, stop: cancel}
	opts.Sleep = slept.sleep

	_, err := Apply(ctx, conn, opts)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrDirty) {
		t.Fatalf("a canceled run reported %v, want the cancel and the dirty state together", err)
	}

	if len(conn.store.cps) != 1 || conn.store.cps[0].Chunks != 2 {
		t.Errorf("the canceled run left the checkpoints %+v, want the two chunks that committed",
			conn.store.cps)
	}

	for _, call := range conn.calls {
		if strings.HasPrefix(call, "mark-dirty") && strings.Contains(call, "canceled") {
			t.Errorf("the dirty row was written on the canceled context: %q", call)
		}
	}
}

func TestAResumeRefusesAMigrationThatEmitsDifferentlyNow(t *testing.T) {
	opts, src, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	src.ops[versionAt(0)] = ir.Operations{nicknames("someone")}
	served := table.served

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNotResumable) {
		t.Fatalf("a migration emitting a different backfill resumed with %v", err)
	}

	if table.served != served {
		t.Errorf("%d chunks ran before the refusal", table.served-served)
		t.Log("The checkpoint names a cursor through the rows the recorded operations wrote; under a")
		t.Log("different value it would carry on half-filled with one literal and half with another.")
	}
}

func TestAResumeRefusesAMigrationThatWouldNoLongerRunInChunks(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	opts.Planner.ChunkRows = 0

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrNotResumable) {
		t.Fatalf("a run that would not chunk resumed a chunked one with %v", err)
	}
}

func TestADriftedMigrationIsRefusedRatherThanResumed(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	opts.Migrations[0] = history.NewFile(migrationAt(t, 0), []byte("-- edited\n"))

	var moved *DriftError
	if _, err := Apply(t.Context(), conn, opts); !errors.As(err, &moved) || !moved.Resuming {
		t.Fatalf("an edited migration was resumed with %v, want a drift refusal that says it "+
			"stopped partway", err)
	}
}

func TestAMigrationThatAlsoChangesTheSchemaRunsItsDataAsOneStatement(t *testing.T) {
	opts, src, conn, table := chunked(t, 50, 10)
	src.ops[versionAt(0)] = ir.Operations{addIndex("x0"), nicknames("anonymous")}

	if _, err := Apply(t.Context(), conn, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if len(table.slices) != 0 || !slices.Contains(conn.calls, "exec fill "+keyedTable) {
		t.Errorf("a mixed migration ran %d chunks, and its backfill statement ran: %v",
			len(table.slices), slices.Contains(conn.calls, "exec fill "+keyedTable))
		t.Log("Each chunk commits on its own, so a migration that also changes the schema could no")
		t.Log("longer be the one transaction it runs in; splitting one around its data is M13-T4's.")
	}
}

func TestATableWithNoPrimaryKeyIsRefusedBeforeAnyChunkRuns(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	conn.live = keyedSnapshot(false)

	_, err := Apply(t.Context(), conn, opts)
	if !errors.Is(err, ErrNoChunkKey) {
		t.Fatalf("a table with no key was chunked with %v", err)
	}

	if len(table.slices) != 0 || len(conn.store.runs) != 1 ||
		conn.store.runs[0].Outcome != history.Failed {
		t.Errorf("a refusal before any chunk ran %d chunks and recorded %q, want none and failed",
			len(table.slices), outcomes(conn.store.runs))
	}

	for _, remedy := range []string{"give the table a primary key", "safety.chunk_rows to 0"} {
		if !strings.Contains(err.Error(), remedy) {
			t.Errorf("the refusal does not name %q:\n%v", remedy, err)
		}
	}
}

func TestResolveResumeCarriesAChunkedRunOnFromItsCheckpoint(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	settled, err := Resolve(t.Context(), conn, opts, Repair{Mode: RepairResume})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if settled.Checkpoint == nil || !settled.Ran || !table.writtenOnce() {
		t.Errorf("resolve --resume reported checkpoint %v and ran %v, with every row once: %v",
			settled.Checkpoint, settled.Ran, table.writtenOnce())
	}

	if len(conn.store.cps) != 0 {
		t.Errorf("the checkpoint survived the resume: %+v", conn.store.cps)
	}
}

func TestAnAssertedResolutionClearsTheCheckpoint(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	if _, err := Resolve(t.Context(), conn, opts, Repair{Mode: RepairRolledBack,
		Reason: "emptied the column by hand"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	if len(conn.store.cps) != 0 {
		t.Errorf("an asserted resolution left the checkpoint %+v", conn.store.cps)
		t.Log("The next run of that migration is a fresh one, and a checkpoint left behind would")
		t.Log("have a later stopped run resume from a cursor through rows this one never wrote.")
	}

	if !slices.Contains(conn.calls, "clear-checkpoint "+versionAt(0)) {
		t.Errorf("the checkpoint was not cleared inside the resolution's transaction: %v", conn.calls)
	}
}

func TestARollbackOverAStoppedChunkedRunNamesTheCheckpoint(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	table.failAt = 3

	if _, err := Apply(t.Context(), conn, opts); !errors.Is(err, ErrDirty) {
		t.Fatalf("staging the stopped run: %v", err)
	}

	_, err := Rollback(t.Context(), conn, opts)

	var between *DirtyStateError
	if !errors.As(err, &between) || between.Checkpoint == nil {
		t.Fatalf("a rollback over a stopped chunked run reported %v, want the refusal naming its "+
			"checkpoint", err)
	}

	if !strings.Contains(err.Error(), "through key (20)") {
		t.Errorf("the refusal does not say how far the run got:\n%v", err)
	}
}

func TestSteadyWaitsItsPauseBeforeEveryChunkButTheFirst(t *testing.T) {
	steady := Steady{Rows: 7, Pause: time.Second}

	first, _ := steady.Pace(t.Context(), Progress{})
	later, _ := steady.Pace(t.Context(), Progress{Chunks: 4})

	if first != (Pace{Rows: 7}) || later != (Pace{Rows: 7, Wait: time.Second}) {
		t.Errorf("Steady paced %+v then %+v", first, later)
	}
}

func TestAPacerAllowingNoRowsFailsTheRunRatherThanLooping(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)
	opts.Pacer = &paces{script: []Pace{{Rows: 0}}}

	if _, err := Apply(t.Context(), conn, opts); err == nil || len(table.slices) != 0 {
		t.Fatalf("a pacer allowing 0 rows ran %d chunks and reported %v", len(table.slices), err)
	}
}

func TestAStoppedChunkedRollbackIsRefusedByTheNextApplyRatherThanCarriedOn(t *testing.T) {
	opts, _, conn, table := chunked(t, 50, 10)

	stopped := undone(t, 0, 2, 1)
	stopped.Checksum = sumOf(0)
	stopped.Outcome, stopped.LastStatement, stopped.Error = history.Dirty, 1, "interrupted"

	conn.store.table = true
	conn.store.runs = []history.Record{ran(t, 0, 1, 1), stopped}
	conn.store.cps = []history.Checkpoint{{Version: versionAt(0), Direction: ir.Down, Step: 0,
		Cursor: []string{"20"}, Chunks: 2, Written: 20}}

	_, err := Apply(t.Context(), conn, opts)

	var between *DirtyStateError
	if !errors.As(err, &between) || between.Checkpoint == nil {
		t.Fatalf("an up over a stopped chunked rollback reported %v, want the dirty refusal "+
			"naming its checkpoint", err)
	}

	if len(table.slices) != 0 {
		t.Errorf("the up ran %d chunks of a rollback's work", len(table.slices))
		t.Log("Only an up carries itself on: a rollback is the operator's own instruction, so one")
		t.Log("that stopped partway is settled by migr resolve, the command that says what it does.")
	}
}
