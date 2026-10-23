package sqlite

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver/drivertest"
)

func TestTheLockTablesAreWhatSQLiteDeclares(t *testing.T) {
	table := drivertest.LockTable{Engine: "sqlite", Driver: Name, Capabilities: setAt(t, linked(t))}
	table.Run(t)

	table.Page = "docs/drivers/sqlite.md"
	table.Run(t)
}
