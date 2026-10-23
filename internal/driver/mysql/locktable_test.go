package mysql

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver/drivertest"
)

func TestTheGuidesLockTablesAreWhatMySQLAndMariaDBDeclare(t *testing.T) {
	for engine, server := range map[string]serverFacts{
		"mysql 8.0":    {version: serverVersion{variant: MySQL, major: 8, minor: 0, patch: 46}, logBin: true},
		"mariadb 11.4": {version: serverVersion{variant: MariaDB, major: 11, minor: 4, patch: 13}},
	} {
		t.Run(engine, func(t *testing.T) {
			drivertest.LockTable{Engine: engine, Driver: Name, Capabilities: declaredSet(t, server)}.Run(t)
		})
	}
}
