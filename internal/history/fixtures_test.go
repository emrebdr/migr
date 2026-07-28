package history

import (
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/checksum"
	"github.com/emrebdr/migr/internal/ir"
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
