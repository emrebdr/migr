package config

import (
	"errors"
	"fmt"

	"github.com/emrebdr/migr/internal/driver"
)

// DefaultDatabase is the database a command acts on when --database names none and it exists.
const DefaultDatabase = "default"

// ErrNoDatabase marks a --database naming nothing this config defines, for errors.Is.
var ErrNoDatabase = errors.New("no database by that name is configured")

// ErrAmbiguousDatabase marks a config with several databases and nothing saying which, for errors.Is.
var ErrAmbiguousDatabase = errors.New("this config defines more than one database")

// Target resolves which database a command acts on: the one named, the default, or the only one.
func (c *Config) Target(name string) (string, Database, error) {
	if name != "" {
		return c.named(name)
	}

	if db, ok := c.Databases[DefaultDatabase]; ok {
		return DefaultDatabase, db, nil
	}

	if len(c.Databases) == 1 {
		return c.sole()
	}

	return "", Database{}, c.ambiguous()
}

// named returns one database by name, listing what is defined when nothing carries it.
func (c *Config) named(name string) (string, Database, error) {
	db, ok := c.Databases[name]
	if !ok {
		return "", Database{}, fmt.Errorf("--database %s: %w (this migr.yaml defines %s)",
			name, ErrNoDatabase, join(sortedKeys(c.Databases)))
	}

	return name, db, nil
}

// sole returns the only database a config defines, which needs no naming to be unambiguous.
func (c *Config) sole() (string, Database, error) {
	for _, name := range sortedKeys(c.Databases) {
		return name, c.Databases[name], nil
	}

	return "", Database{}, c.ambiguous()
}

// ambiguous refuses to guess, since a migr.yaml holding a development and a production target is one file.
func (c *Config) ambiguous() (err error) {
	if len(c.Databases) == 0 {
		return fmt.Errorf("%w: this migr.yaml defines none", ErrNoDatabase)
	}

	return fmt.Errorf("%w and names no %q, so pass --database to say which one: %s",
		ErrAmbiguousDatabase, DefaultDatabase, join(sortedKeys(c.Databases)))
}

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
