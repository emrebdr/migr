//go:build integration

package mysql

import (
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

// twiceQueued stages two migrations that each emit the same operations, so a preview queues both.
func twiceQueued(t *testing.T, c *mysqlConn, ops ir.Operations) engine.Options {
	t.Helper()

	opts := staged(t, ops)
	caps := c.Capabilities()
	opts.Planner.Capabilities = &caps

	second, err := migration.Parse("20260927T153001_run_again.sql")
	if err != nil {
		t.Fatalf("parsing the second staged filename: %v", err)
	}

	opts.Migrations = append(opts.Migrations, history.NewFile(second, []byte("-- staged again\n")))

	return opts
}

func TestAPreviewLowersOnlyTheFirstQueuedMigrationsVerdictFromItsTable(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		table := uniqueName(t) + "_t"
		fresh(t, c, table)
		build(t, c, table, shapes["a plain table"])

		opts := twiceQueued(t, c, ir.Operations{ir.AddColumn{Table: table, Column: ir.Column{
			Name: "added", Type: liveType(t, "int32"), Nullable: true}}})

		previewed, err := engine.Preview(t.Context(), c, opts, ir.Up)
		if err != nil {
			t.Fatalf("Preview: %v", err)
		}

		if len(previewed.Steps) != 2 {
			t.Fatalf("the preview queued %d migrations, want both", len(previewed.Steps))
		}

		first, second := previewed.Steps[0].Plan.Steps[0], previewed.Steps[1].Plan.Steps[0]

		if first.Lock() != capability.BriefExclusive || !first.Assessed {
			t.Errorf("the first migration's add_column reads %s (assessed %v), and this table changes "+
				"instantly", first.Lock(), first.Assessed)
		}

		if second.Lock() != capability.TableRewrite || second.Assessed {
			t.Errorf("the second migration's add_column reads %s (assessed %v), want its declared worst",
				second.Lock(), second.Assessed)
		}

		if previewed.Steps[0].Unassessed != nil {
			t.Errorf("the session could not read the table: %v", previewed.Steps[0].Unassessed)
		}
	})

	t.Log("Declared, an add_column is table_rewrite on both servers because a COMPRESSED or FULLTEXT")
	t.Log("table is rebuilt (ADR 0081). The preview reads the first migration's table and lowers it;")
	t.Log("the second is judged before the first has run, so it keeps the worst.")
}
