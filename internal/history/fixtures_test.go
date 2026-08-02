package history

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// ran is a fixed instant, because forbidigo bans time.Now and a record needs one anyway.
var ran = time.Date(2026, time.July, 27, 15, 30, 0, 0, time.UTC)

// applied returns a record of a run that completed, for a test to break one field of.
func applied(t *testing.T) Record {
	t.Helper()

	return Record{
		Batch:     1,
		Ordinal:   1,
		Version:   "20260727T153000",
		Name:      "add_users",
		Direction: ir.Up,
		Outcome:   Applied,
		Checksum:  hashOfAMigration(t),
		AppliedAt: ran,
		Duration:  1200 * time.Millisecond,
		Driver:    "postgres",
		Runtime:   "go",
	}.Stamp()
}

// hashOfAMigration returns a computed checksum, failing the test if the fixture will not hash.
func hashOfAMigration(t *testing.T) checksum.Checksum {
	t.Helper()

	sum, err := checksum.Of([]byte("-- a migration"), ir.Operations{ir.DropTable{Table: "users"}})
	if err != nil {
		t.Fatalf("checksum.Of: %v", err)
	}

	return sum
}

// at returns a migration whose version is a given minute of the one afternoon these fixtures use.
func at(minute int, name string) migration.Migration {
	return migration.Migration{
		Version: fmt.Sprintf("20260727T15%02d00", minute),
		Name:    name,
		Ext:     ".sql",
	}
}

// file returns a discovered migration together with the digest of the source it holds.
func file(minute int, name, source string) File {
	return NewFile(at(minute, name), []byte(source))
}

// edited returns the same migration carrying different source bytes, which is what drift is.
func edited(f File, source string) File {
	return NewFile(f.migration, []byte(source))
}

// up returns a forward run of a file, as the only run of the batch it belongs to.
func up(f File, batch int64, outcome Outcome) Record {
	return runAt(f, batch, 1, ir.Up, outcome)
}

// down returns a rollback of a file, as the only run of the batch it belongs to.
func down(f File, batch int64, outcome Outcome) Record {
	return runAt(f, batch, 1, ir.Down, outcome)
}

// runAt returns one recorded run of a file, at the batch and ordinal a test wants it in.
func runAt(f File, batch int64, ordinal int32, direction ir.Direction, outcome Outcome) Record {
	return Record{
		Batch:     batch,
		Ordinal:   ordinal,
		Version:   f.migration.Version,
		Name:      f.migration.Name,
		Direction: direction,
		Outcome:   outcome,
		Checksum:  checksum.Checksum{Source: f.source},
		AppliedAt: ran,
		Driver:    "postgres",
		Runtime:   "sql",
	}.Stamp()
}

// resolve reconciles one file against a list of runs and returns the single status it reached.
func resolve(t *testing.T, f File, runs ...Record) Status {
	t.Helper()

	statuses := reconcile(t, []File{f}, runs...)
	if len(statuses) != 1 {
		t.Fatalf("reconciling one file produced %d statuses, want 1", len(statuses))
	}

	return statuses[0]
}

// reconcile folds runs against files through a present history table, refusing an absent one.
func reconcile(t *testing.T, files []File, runs ...Record) []Status {
	t.Helper()

	statuses, ok := Reconcile(files, NewLog(runs)).Resolved()
	if !ok {
		t.Fatal("a log built from runs reported that this database has no history table at all")
	}

	return statuses
}

// versionsOf renders the versions a list of statuses reported, in the order it reported them.
func versionsOf(statuses []Status) []string {
	versions := make([]string, len(statuses))
	for i, status := range statuses {
		versions[i] = status.Version
	}

	return versions
}

// stubStore returns the runs or the failure a test wants List to produce, and nothing else.
type stubStore struct {
	// runs is what List hands back when it does not fail.
	runs []Record
	// err is the failure List reports instead, which Read has to classify.
	err error
}

// List returns whatever the test configured, which is all reconciliation ever asks a store for.
func (s stubStore) List(context.Context) ([]Record, error) {
	return s.runs, s.err
}
