//go:build integration

package mysql

import (
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

func TestAStatementDeclaringNoTransactionRunsBareAndTheMigrationApplies(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		table := quote(uniqueName(t) + "_t")
		t.Cleanup(func() { exec(t, c, "DROP TABLE IF EXISTS "+table) })

		ops := ir.Operations{
			ir.Raw{Driver: Name, SQL: "CREATE TABLE " + table + " (`id` int PRIMARY KEY)"},
			ir.Raw{Driver: Name, SQL: "OPTIMIZE TABLE " + table, NoTransaction: true},
			ir.Raw{Driver: Name, SQL: "ALTER TABLE " + table + " ADD COLUMN `n` int NULL"},
		}

		if _, err := engine.Apply(t.Context(), c, staged(t, ops)); err != nil {
			t.Fatalf("Apply returned %v, want the migration to run in three parts", err)
		}

		runs, err := c.History().List(t.Context())
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		if len(runs) != 3 || runs[0].Outcome != history.Dirty || runs[2].Outcome != history.Applied {
			t.Errorf("the history holds %+v, want a dirty row per committed part and then applied", runs)
		}
	})

	t.Log("Every MySQL DDL statement commits on its own already, so the flag changes nothing about")
	t.Log("what survives a failure here -- it changes where the history row is written, and keeps")
	t.Log("the engine's one rule rather than a driver's (ADR 0084).")
}
