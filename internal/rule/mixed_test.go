package rule

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// mixing returns an up half that declares one schema change and sends the given statements itself.
func mixing(t *testing.T, sql ...string) Half {
	t.Helper()

	sent := make([]driver.Query, 0, len(sql))
	for _, one := range sql {
		sent = append(sent, driver.Query{SQL: one})
	}

	return Half{
		Operations: ir.Operations{ir.AddColumn{Table: "users", Column: nullable(t, "tier", "int32")}},
		Direct:     sent,
	}
}

func TestAMigrationDoingSchemaAndDataAtOnceIsReported(t *testing.T) {
	found := judging(t, MixedDataMigration, mixing(t, "UPDATE users SET tier = 1"))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1: %v", len(found), found)
	}

	if found[0].Direction != ir.Up {
		t.Errorf("the finding names the %s half, and this rule reads the up", found[0].Direction)
	}

	if found[0].Operation != 0 || found[0].Kind != "" {
		t.Errorf("the finding is %+v, want it about the whole half rather than one operation",
			found[0])
	}

	for _, phrase := range []string{"1 statement", "1 schema operation", "commit on their own",
		"no rollback reaches them"} {
		if !strings.Contains(found[0].Detail, phrase) {
			t.Errorf("detail %q does not say %q", found[0].Detail, phrase)
		}
	}
}

func TestTheStatementsAreCountedAndNeverQuoted(t *testing.T) {
	found := judging(t, MixedDataMigration, mixing(t,
		"UPDATE users SET tier = 1", "DELETE FROM sessions"))

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1 however many statements there are: %v", len(found), found)
	}

	if !strings.Contains(found[0].Detail, "2 statements") {
		t.Errorf("detail %q does not count both statements", found[0].Detail)
	}

	for _, quoted := range []string{"UPDATE", "DELETE"} {
		if strings.Contains(found[0].Detail, quoted) {
			t.Errorf("detail %q quotes the SQL, and a count is what a report of this owes", found[0].Detail)
		}
	}
}

func TestTheSchemaOperationsAreCountedToo(t *testing.T) {
	up := mixing(t, "UPDATE users SET tier = 1")
	up.Operations = append(up.Operations, ir.AddIndex{Table: "users", Name: "idx_users_tier",
		Columns: []string{"tier"}})

	found := judging(t, MixedDataMigration, up)

	if len(found) != 1 {
		t.Fatalf("found %d findings, want 1 however many operations there are: %v", len(found), found)
	}

	if !strings.Contains(found[0].Detail, "2 schema operations") {
		t.Errorf("detail %q does not count both operations, so one side of the pattern is reported "+
			"as a constant", found[0].Detail)
	}
}

func TestOneHalfOfThePatternIsNotThePattern(t *testing.T) {
	for _, c := range []struct {
		name string
		up   Half
	}{
		{"it declares schema changes and sends nothing", upOf(ir.AddColumn{Table: "users",
			Column: nullable(t, "tier", "int32")})},
		{"it sends statements and declares nothing", Half{
			Direct: []driver.Query{{SQL: "UPDATE users SET tier = 1"}}}},
		{"it does neither", Half{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if found := judging(t, MixedDataMigration, c.up); len(found) != 0 {
				t.Errorf("found %d findings: %v", len(found), found)
			}
		})
	}
}

func TestOnlyTheUpHalfOfAMixedMigrationIsReported(t *testing.T) {
	found := judgingAbout(t, MixedDataMigration, Subject{
		Up:   upOf(ir.AddColumn{Table: "users", Column: nullable(t, "tier", "int32")}),
		Down: mixing(t, "UPDATE users SET tier = NULL"),
		Undo: UndoAuthored,
	})

	if len(found) != 0 {
		t.Errorf("a down that sends its own statements was reported: %v", found)
		t.Log("A down's own statements are the author undoing their own data change, which is what")
		t.Log("you want if the up mixed. The up is where the pattern is judged.")
	}
}
