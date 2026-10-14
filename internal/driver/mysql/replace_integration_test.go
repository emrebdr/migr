//go:build integration

package mysql

import (
	"context"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/schema"
)

// phased is a source of several migrations, each with the up and the down its author wrote.
type phased struct {
	up, down map[string]ir.Operations
}

// Emit returns what one migration emits in the direction asked, as an SDK phase would.
func (p phased) Emit(_ context.Context, m migration.Migration, dir ir.Direction, _ engine.Mode,
) (engine.Emitted, error) {
	if dir == ir.Down {
		return engine.Emitted{Operations: p.down[m.Version]}, nil
	}

	return engine.Emitted{Operations: p.up[m.Version]}, nil
}

// replacement stages the four phases of widening one column, as both SDKs emit them for it.
func replacement(t *testing.T, c *mysqlConn, table string) engine.Options {
	t.Helper()

	was, becomes := liveType(t, "string(255)"), liveType(t, "string(320)")
	retired := ir.Column{Name: "email_old", Type: was, Nullable: true}
	staged := ir.Column{Name: "email_next", Type: becomes, Nullable: true}
	src := phased{up: map[string]ir.Operations{}, down: map[string]ir.Operations{}}

	phases := []struct {
		file     string
		up, down ir.Operations
	}{
		{"20261001T090200_expand_email.sql",
			ir.Operations{ir.AddColumn{Table: table, Column: staged}},
			ir.Operations{ir.DropColumn{Table: table, Column: staged.Name, From: &staged}}},
		{"20261001T090300_backfill_email.sql",
			ir.Operations{ir.DataTransform{Table: table, Column: staged.Name, Type: becomes,
				From: "email", Function: ir.TransformCopy}},
			ir.Operations{}},
		{"20261001T090400_swap_email.sql",
			ir.Operations{
				ir.AlterColumn{Table: table, Column: "email", Nullable: ptr(true),
					From: &ir.ColumnState{Nullable: ptr(false)}},
				ir.AlterColumn{Table: table, Column: staged.Name, Nullable: ptr(false),
					From: &ir.ColumnState{Nullable: ptr(true)}},
				ir.RenameColumn{Table: table, From: "email", To: retired.Name},
				ir.RenameColumn{Table: table, From: staged.Name, To: "email"},
			},
			ir.Operations{
				ir.RenameColumn{Table: table, From: "email", To: staged.Name},
				ir.RenameColumn{Table: table, From: retired.Name, To: "email"},
				ir.AlterColumn{Table: table, Column: staged.Name, Nullable: ptr(true),
					From: &ir.ColumnState{Nullable: ptr(false)}},
				ir.AlterColumn{Table: table, Column: "email", Nullable: ptr(false),
					From: &ir.ColumnState{Nullable: ptr(true)}},
			}},
		{"20261001T090500_contract_email.sql",
			ir.Operations{ir.DropColumn{Table: table, Column: retired.Name, From: &retired}},
			ir.Operations{
				ir.AddColumn{Table: table, Column: retired},
				ir.DataTransform{Table: table, Column: retired.Name, Type: was, From: "email",
					Function: ir.TransformCopy},
			}},
	}

	files := make([]history.File, 0, len(phases))

	for _, phase := range phases {
		m, err := migration.Parse(phase.file)
		if err != nil {
			t.Fatalf("parsing %s: %v", phase.file, err)
		}

		src.up[m.Version], src.down[m.Version] = phase.up, phase.down
		files = append(files, history.NewFile(m, []byte("-- "+phase.file+"\n")))
	}

	return engine.Options{
		Planner:    plan.Planner{Compiler: compilerOnly{}, Expander: c, ChunkRows: 7},
		Source:     src,
		Migrations: files,
		Clock:      steadyClock(),
		Runtime:    func(migration.Migration) string { return runRuntime },
		Allow:      engine.Allowance{All: true},
		Chunker:    c,
		Pacer:      engine.Steady{Rows: 7},
		Sleep:      func(context.Context, time.Duration) error { return nil },
	}
}

// tableOf reads one table's definition back through the snapshot.
func tableOf(t *testing.T, c *mysqlConn, name string) schema.Table {
	t.Helper()

	held, err := c.Introspect(t.Context())
	if err != nil {
		t.Fatalf("introspecting: %v", err)
	}

	table, ok := held.Table(name)
	if !ok {
		t.Fatalf("the snapshot holds no table %q", name)
	}

	return table
}

func TestAColumnReplacementRunsAndRollsBackWholeOnBothServers(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		people, archive := owned(t, "people"), owned(t, "archive")
		manyPeople(t, c, people, archive, 40)

		held := c.History().(mysqlHistory)
		dropLater(t, c, held.table, held.progressTable())
		drop(t.Context(), c, held.table, held.progressTable())

		before := tableOf(t, c, people)
		opts := replacement(t, c, people)

		for phase := 1; phase <= 4; phase++ {
			opts.Scope = engine.Steps(1)

			if _, err := engine.Apply(t.Context(), c, opts); err != nil {
				t.Fatalf("phase %d: %v", phase, err)
			}
		}

		after := tableOf(t, c, people)
		email, ok := after.Column("email")
		logical, _ := email.Type.Logical()
		if _, retired := after.Column("email_old"); !ok || retired || email.Nullable ||
			logical.String() != "string(320)" {
			t.Errorf("the replacement left email as %+v, retired still there: %v", email, retired)
		}

		kept := "SELECT count(*) FROM " + quote(people) +
			" WHERE `email` = CONCAT('User', `id`, '@Example.COM')"
		if got := countOf(t, c, kept); got != 40 {
			t.Errorf("%d of 40 rows hold their own address after the replacement", got)
		}

		opts.Scope = engine.All()

		result, err := engine.Rollback(t.Context(), c, opts)
		if err != nil {
			t.Fatalf("rolling the replacement back: %v", err)
		}

		if len(result.Runs) != 4 || result.Runs[2].Statements != 0 {
			t.Errorf("the rollback ran %+v, want four downs, the backfill's undoing nothing", result.Runs)
		}

		if restored := tableOf(t, c, people); !restored.Equal(before) {
			t.Errorf("the rollback did not restore the table\nbefore %+v\nafter  %+v", before, restored)
		}

		if got := countOf(t, c, kept); got != 40 {
			t.Errorf("%d of 40 rows hold their own address after the rollback", got)
		}
	})
}
