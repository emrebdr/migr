package sqlite

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver/drivertest"
)

func TestTheGuidesLockTableIsWhatSQLiteDeclares(t *testing.T) {
	drivertest.LockTable{Engine: "sqlite", Driver: Name, Capabilities: setAt(t, linked(t))}.Run(t)
}
