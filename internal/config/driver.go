package config

import "github.com/emrebdr/migr/internal/driver"

// DriverOptions returns what a driver for one database is built from, resolved out of migr.yaml.
func (c *Config) DriverOptions(db Database) driver.Options {
	return driver.Options{
		Schema:            db.Schema,
		HistoryTable:      c.History.Table,
		LockStrategy:      string(c.Lock.Strategy),
		LockWait:          c.Lock.Timeout.Duration(),
		StatementLockWait: c.Safety.LockTimeout.Duration(),
	}
}
