package postgres

import (
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
	"github.com/emrebdr/migr/internal/ir"
)

// composed16 is a PostgreSQL 16 as compose runs it, which declares what the 17 beside it does.
var composed16 = serverFacts{version: sixteen, uuidDefault: defaultFunction{exists: true, volatile: true}}

// rewritingAt performs an operation as a session of one server would, which needs no connection.
type rewritingAt struct {
	// version is the server the rewrites are chosen for.
	version serverVersion
}

// Rewrite returns the safer sequence a session of this version performs the operation as.
func (r rewritingAt) Rewrite(op ir.Operation) (driver.Rewrite, bool) {
	return rewriteFor(r.version, op)
}

func TestTheGuidesLockTableIsWhatPostgreSQLDeclares(t *testing.T) {
	tables := make([]string, 0, 2)

	for _, server := range []serverFacts{composed16, modernServer} {
		set, err := capability.NewSet(server.facts(), server.rules())
		if err != nil {
			t.Fatalf("NewSet(%s): %v", server.version, err)
		}

		table := drivertest.LockTable{Engine: "postgres", Driver: Name, Capabilities: set,
			Rewriter: rewritingAt{version: server.version}}
		tables = append(tables, table.Render())

		if server.version == modernServer.version {
			table.Run(t)
		}
	}

	if tables[0] != tables[1] {
		t.Errorf("PostgreSQL 16 and 17 declare different costs, and the guide prints one table for both:\n"+
			"--- 16\n%s--- 17\n%s", tables[0], tables[1])
	}
}
