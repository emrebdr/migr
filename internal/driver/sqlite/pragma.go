package sqlite

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// The three session settings this driver owns, and a DSN may therefore not set.
const (
	// pragmaForeignKeys is enforcement of referential integrity, which SQLite defaults to off.
	pragmaForeignKeys = "foreign_keys"
	// pragmaBusyTimeout is how long a statement waits for the write lock, defaulting to no wait.
	pragmaBusyTimeout = "busy_timeout"
	// pragmaQueryOnly is what makes a migration's own read read-only, and Query is what toggles it.
	pragmaQueryOnly = "query_only"
)

// owned lists them in the order a refusal renders them.
var owned = []string{pragmaForeignKeys, pragmaBusyTimeout, pragmaQueryOnly}

// The two values a boolean pragma reads back as, whatever spelling it was set with.
const (
	pragmaOn  = "1"
	pragmaOff = "0"
)

// applyPragmas puts the session in the state this driver compiles and executes for.
func applyPragmas(ctx context.Context, c *sqliteConn, statementLockWait time.Duration) error {
	if err := pinPragma(ctx, c, pragmaForeignKeys, pragmaOn); err != nil {
		return err
	}

	if err := pinPragma(ctx, c, pragmaBusyTimeout, milliseconds(statementLockWait)); err != nil {
		return err
	}

	return pinPragma(ctx, c, pragmaQueryOnly, pragmaOff)
}

// milliseconds renders a wait the way busy_timeout takes it, which is a whole number of them.
func milliseconds(wait time.Duration) string {
	return strconv.FormatInt(wait.Milliseconds(), 10)
}

// pinPragma sets one session setting and reads it back, since SQLite reports success for one it ignores.
func pinPragma(ctx context.Context, c *sqliteConn, name, value string) error {
	if err := setPragma(ctx, c, name, value); err != nil {
		return err
	}

	got, err := readPragma(ctx, c, name)
	if err != nil {
		return err
	}

	if got != value {
		return &PragmaError{Pragma: name, Want: value, Got: got}
	}

	return nil
}

// setPragma asks for one session setting, whose name is this package's and never a caller's.
func setPragma(ctx context.Context, c *sqliteConn, name, value string) error {
	if _, err := c.runner().ExecContext(ctx, "PRAGMA "+name+" = "+value); err != nil {
		return fmt.Errorf("%w: setting PRAGMA %s to %s: %w", ErrConnect, name, value, err)
	}

	return nil
}

// readPragma reports what one session setting currently is, which is how a no-op becomes visible.
func readPragma(ctx context.Context, c *sqliteConn, name string) (string, error) {
	var value string

	if err := c.runner().QueryRowContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
		return "", fmt.Errorf("%w: reading PRAGMA %s back: %w", ErrConnect, name, err)
	}

	return value, nil
}
