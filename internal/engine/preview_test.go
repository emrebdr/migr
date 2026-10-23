package engine

import (
	"errors"
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// versionsPreviewed returns the versions a preview said would run, in the order it would run them.
func versionsPreviewed(previewed Previewed) []string {
	versions := make([]string, 0, len(previewed.Steps))

	for _, step := range previewed.Steps {
		versions = append(versions, step.Plan.Migration.Version)
	}

	return versions
}

func TestAPreviewNeverCreatesTheHistoryTable(t *testing.T) {
	conn := newRecorder(t, true)
	opts, _ := staged(t, "abc", "de")

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if slices.Contains(conn.calls, "ensure") {
		t.Errorf("a preview ensured the history table: %v", conn.calls)
		t.Log("Ensure creates a table, which is a write. A command that reports what it would do")
		t.Log("and changes the database while doing so is the one thing a dry run may not be.")
	}

	if previewed.Recorded {
		t.Errorf("a preview over a database with no history table reported one")
	}

	if got := versionsPreviewed(previewed); !slices.Equal(got, []string{versionAt(0), versionAt(1)}) {
		t.Errorf("the preview would run %q, want every migration on a virgin database", got)
		t.Log("An apply reads an absent history table as a bug, because it just ensured one. A")
		t.Log("preview never ensured one, so absent is an answer rather than a failure: nothing")
		t.Log("has run here yet, and migr up would create the table first.")
	}
}

func TestAPreviewExecutesNothingAndRecordsNothing(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "abc")

	if _, err := Preview(t.Context(), conn, opts, ir.Up); err != nil {
		t.Fatalf("Preview: %v", err)
	}

	for _, forbidden := range []string{"begin", "commit", "exec a0", "record " + versionAt(0)} {
		if slices.Contains(conn.calls, forbidden) {
			t.Errorf("a preview asked the session for %q: %v", forbidden, conn.calls)
		}
	}

	if len(conn.store.runs) != 0 {
		t.Errorf("a preview wrote %d history rows", len(conn.store.runs))
	}
}

func TestAPreviewTakesTheMigrationLock(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "abc")

	if _, err := Preview(t.Context(), conn, opts, ir.Up); err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if !slices.Contains(conn.calls, "lock") || !slices.Contains(conn.calls, "unlock") {
		t.Errorf("a preview ran without the migration lock: %v", conn.calls)
		t.Log("Reconciling outside the lock reports on a database another run is still changing,")
		t.Log("so the answer can be stale before it finishes printing.")
	}
}

func TestAPreviewCompilesEveryMigrationItWouldRun(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "abc", "de")

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if len(previewed.Steps) != 2 {
		t.Fatalf("the preview holds %d steps, want one per migration", len(previewed.Steps))
	}

	if got := previewed.Steps[0].Plan.Len(); got != 3 {
		t.Errorf("the first migration compiles to %d statements in the preview, want 3", got)
	}

	if previewed.Steps[1].Ordinal != 2 {
		t.Errorf("the second step is ordinal %d, want the position it would be recorded under",
			previewed.Steps[1].Ordinal)
	}

	if previewed.Batch != 1 {
		t.Errorf("the preview would record under batch %d, want the first", previewed.Batch)
	}

	t.Log("migr plan prints these statements and --dry-run prints their count. Both read one")
	t.Log("pipeline, so there is no preview implementation to drift away from what apply does.")
}

func TestAPreviewGoingDownReportsTheRollbackOrder(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0, 1)

	opts, _ := staged(t, "a", "b")

	previewed, err := Preview(t.Context(), conn, opts, ir.Down)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if got := versionsPreviewed(previewed); !slices.Equal(got, []string{versionAt(1), versionAt(0)}) {
		t.Errorf("the preview would undo %q, want newest first", got)
	}

	if previewed.Direction != ir.Down {
		t.Errorf("the preview reports direction %q", previewed.Direction)
	}

	for _, step := range previewed.Steps {
		if step.Plan.Direction != ir.Down {
			t.Errorf("%s compiled %q in the preview, want the direction it would run",
				step.Plan.Migration.Filename(), step.Plan.Direction)
			t.Log("A preview that queued the rollback and then compiled everything forwards would")
			t.Log("print the statements that apply a migration under the heading of undoing it —")
			t.Log("right versions, right order, and every statement the opposite of the truth.")
		}
	}
}

func TestAPreviewNarrowsTheSameWayARunDoes(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a", "b", "c")
	opts.Scope = Steps(2)

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if got := versionsPreviewed(previewed); !slices.Equal(got, []string{versionAt(0), versionAt(1)}) {
		t.Errorf("the preview would run %q, want the first two --step names", got)
	}

	if len(previewed.Skipped) != 1 {
		t.Errorf("the preview skipped %d, want the one the scope left short", len(previewed.Skipped))
	}
}

func TestAPreviewOfAnApplyRefusesTheBatchSelector(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a")
	opts.Scope = LastBatch()

	_, err := Preview(t.Context(), conn, opts, ir.Up)
	if !errors.Is(err, ErrNoBatch) {
		t.Fatalf("Preview returned %v, want the refusal an apply gets", err)
	}

	if slices.Contains(conn.calls, "lock") {
		t.Errorf("a scope nothing could act with was refused only after the lock: %v", conn.calls)
	}
}

func TestAPreviewRefusesADirectionNothingRuns(t *testing.T) {
	conn := newRecorder(t, true)
	conn.store.table = true

	opts, _ := staged(t, "a")

	if _, err := Preview(t.Context(), conn, opts, ir.Direction("sideways")); !errors.Is(
		err, ErrUnknownDirection) {
		t.Errorf("Preview returned %v, want ErrUnknownDirection", err)
	}
}

func TestAPreviewReportsDriftBeforeItReportsAQueue(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)
	conn.store.runs[0].Checksum.Source = checksum.OfSource([]byte("what the file used to hold"))

	opts, _ := staged(t, "a", "b")

	if _, err := Preview(t.Context(), conn, opts, ir.Up); !errors.Is(err, ErrDrifted) {
		t.Errorf("Preview returned %v, want the drift an apply would report", err)
		t.Log("A dry run that reports a clean queue over a drifted database is worse than no dry")
		t.Log("run: the state switch happens before narrowing, in the preview as in the apply.")
	}
}

func TestAPreviewOverADatabaseWithRunsReportsItsHistoryTable(t *testing.T) {
	conn := newRecorder(t, true)
	carried(t, conn, 0)

	opts, _ := staged(t, "a", "b")

	previewed, err := Preview(t.Context(), conn, opts, ir.Up)
	if err != nil {
		t.Fatalf("Preview: %v", err)
	}

	if !previewed.Recorded {
		t.Errorf("a preview over a database with a history table reported none")
	}

	if got := versionsPreviewed(previewed); !slices.Equal(got, []string{versionAt(1)}) {
		t.Errorf("the preview would run %q, want only what is still pending", got)
	}

	if previewed.Batch != 2 {
		t.Errorf("the preview would record under batch %d, want one past the last recorded run",
			previewed.Batch)
	}

	if len(previewed.Skipped) != 1 || previewed.Skipped[0].State != history.StateApplied {
		t.Errorf("the preview reports skipped %+v, want the applied migration and its state",
			previewed.Skipped)
	}
}
