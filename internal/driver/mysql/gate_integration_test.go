//go:build integration

package mysql

import (
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
)

func TestAContractHeldAfterASwapSaysTheSwapIsTwoCommitsHere(t *testing.T) {
	eachServer(t, func(t *testing.T, _ server, c *mysqlConn) {
		table := uniqueName(t) + "_t"
		fresh(t, c, table)

		held := c.History().(mysqlHistory)
		dropLater(t, c, held.releaseTable())

		opts := staged(t, ir.Operations{ir.CreateTable{Table: table, Columns: []ir.Column{
			{Name: "id", Type: mustType(t, "int64")}}, PrimaryKey: []string{"id"}}})

		second, err := migration.Parse("20260927T153001_contract_email.sql")
		if err != nil {
			t.Fatalf("parsing the second staged filename: %v", err)
		}

		first, err := migration.Parse("20260927T153000_run_partway.sql")
		if err != nil {
			t.Fatalf("parsing the first staged filename: %v", err)
		}

		opts.Migrations = append(opts.Migrations, history.NewFile(second, []byte("-- staged again\n")))
		opts.Phases = migration.Phases{
			first.Version:  {Change: "users.email", Name: "swap"},
			second.Version: {Change: "users.email", Name: "contract", After: "swap"},
		}
		opts.Gates = map[string]migration.Gate{second.Version: {Phase: opts.Phases[second.Version], Prior: first}}
		opts.Release = "r1"

		result, err := engine.Apply(t.Context(), c, opts)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}

		if len(result.Runs) != 1 || result.Held == nil {
			t.Fatalf("the run applied %d and held %+v, want the swap applied and the contract held",
				len(result.Runs), result.Held)
		}

		if !strings.Contains(result.Held.Reason, "between its renames the column an app reads does not exist") {
			t.Errorf("the hold does not say what a swap is on this engine: %s", result.Held.Reason)
		}

		releases, err := held.Releases(t.Context())
		if err != nil || len(releases) != 1 || releases[0].Name != "r1" {
			t.Errorf("the run recorded the releases %+v, %v, want r1 once", releases, err)
		}
	})

	t.Log("Every DDL statement commits on its own here, so a swap's two renames are two moments and")
	t.Log("between them the column an app reads is gone. A gate waiting on the app says so.")
}
