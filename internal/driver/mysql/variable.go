package mysql

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The session variables this driver owns, and a connection string may therefore not set.
const (
	// varSQLMode carries the string literal grammar the compiler is written against.
	varSQLMode = "sql_mode"
	// varTimeZone is what a stored instant is converted through, and is pinned to UTC.
	varTimeZone = "time_zone"
	// varLockWait bounds a statement's wait for a metadata lock, which DDL takes on every table.
	varLockWait = "lock_wait_timeout"
	// varRowLockWait bounds a statement's wait for a row lock, which is InnoDB's separate queue.
	varRowLockWait = "innodb_lock_wait_timeout"
	// varReadOnly refuses every write on the session, and Query is what turns it on for one read.
	varReadOnly = "transaction_read_only"
)

// varCharacterSet is the encoding a value crosses the wire through, which this driver checks and never sets.
const varCharacterSet = "character_set_connection"

// ownedVariables lists them in the order a refusal renders them.
var ownedVariables = []string{varSQLMode, varTimeZone, varLockWait, varRowLockWait, varReadOnly}

// noBackslashEscapes is the sql_mode under which doubling a quote is the whole defense.
//
// This is PostgreSQL's standard_conforming_strings in MySQL's spelling, and the reason is the same:
// Compile is pure by contract and cannot read a session setting, so the session is pinned to the
// grammar the compiler quotes for rather than trusted to already have it. Both servers leave it off
// by default, so this is an override with a measured cost -- DEFAULT 'a\b' stores two characters with
// it and one control character without -- and doc.go states that cost rather than burying it.
const noBackslashEscapes = "NO_BACKSLASH_ESCAPES"

// utcOffset is what time_zone is pinned to, so an instant rendered as UTC text is stored as itself.
const utcOffset = "+00:00"

// requiredCharacterSet is the one connection encoding every value can cross the wire as JSON through.
const requiredCharacterSet = "utf8mb4"

// The two values a boolean session variable reads back as, whatever spelling it was set with.
const (
	variableOn  = "1"
	variableOff = "0"
)

// maxWaitSeconds is the longest lock_wait_timeout MySQL keeps, past which it clamps and reports success.
const maxWaitSeconds = 31536000

// ownsVariable reports whether a name is one of this driver's, however the connection string cased it.
func ownsVariable(name string) bool {
	return slices.Contains(ownedVariables, strings.ToLower(strings.TrimSpace(name)))
}

// applyVariables puts the session in the state this driver compiles and executes for.
func applyVariables(ctx context.Context, c *mysqlConn, statementLockWait time.Duration) error {
	if err := checkCharacterSet(ctx, c); err != nil {
		return err
	}

	if err := pinSQLMode(ctx, c); err != nil {
		return err
	}

	if err := pinText(ctx, c, varTimeZone, utcOffset); err != nil {
		return err
	}

	wait := waitSeconds(statementLockWait)

	if err := pinNumber(ctx, c, varLockWait, wait); err != nil {
		return err
	}

	if err := pinNumber(ctx, c, varRowLockWait, wait); err != nil {
		return err
	}

	return pinNumber(ctx, c, varReadOnly, variableOff)
}

// waitSeconds renders a wait the way both lock timeouts take it, which is a whole number of seconds.
func waitSeconds(wait time.Duration) string {
	return strconv.FormatInt(int64(wait/time.Second), 10)
}

// checkCharacterSet refuses a connection that would transcode values on the way out to a migration.
func checkCharacterSet(ctx context.Context, c *mysqlConn) error {
	got, err := readVariable(ctx, c, varCharacterSet)
	if err != nil {
		return err
	}

	if got == requiredCharacterSet {
		return nil
	}

	return &CharacterSetError{Got: got, Want: requiredCharacterSet}
}

// pinSQLMode adds the one mode this driver owns, leaving every other mode the operator set alone.
func pinSQLMode(ctx context.Context, c *mysqlConn) error {
	current, err := readVariable(ctx, c, varSQLMode)
	if err != nil {
		return err
	}

	if err := bindVariable(ctx, c, varSQLMode, withMode(current, noBackslashEscapes)); err != nil {
		return err
	}

	got, err := readVariable(ctx, c, varSQLMode)
	if err != nil {
		return err
	}

	if hasMode(got, noBackslashEscapes) {
		return nil
	}

	return &VariableError{Variable: varSQLMode, Want: noBackslashEscapes, Got: got}
}

// withMode returns a sql_mode carrying one more mode, and the same one unchanged when it is there.
func withMode(current, mode string) string {
	if hasMode(current, mode) {
		return current
	}

	if strings.TrimSpace(current) == "" {
		return mode
	}

	return current + "," + mode
}

// hasMode reports whether a sql_mode carries one mode, which the server returns in its own order.
func hasMode(modes, mode string) bool {
	return slices.Contains(strings.Split(modes, ","), mode)
}

// pinText sets one string-valued session variable through a bound parameter and reads it back.
func pinText(ctx context.Context, c *mysqlConn, name, value string) error {
	if err := bindVariable(ctx, c, name, value); err != nil {
		return err
	}

	return confirmVariable(ctx, c, name, value)
}

// pinNumber sets one numeric or boolean session variable and reads it back.
func pinNumber(ctx context.Context, c *mysqlConn, name, value string) error {
	if err := renderVariable(ctx, c, name, value); err != nil {
		return err
	}

	return confirmVariable(ctx, c, name, value)
}

// confirmVariable reads a session variable back, since MySQL clamps out of range and reports success.
func confirmVariable(ctx context.Context, c *mysqlConn, name, value string) error {
	got, err := readVariable(ctx, c, name)
	if err != nil {
		return err
	}

	if got == value {
		return nil
	}

	return &VariableError{Variable: name, Want: value, Got: got}
}

// bindVariable sets a string-valued session variable, whose value may have come from the server.
func bindVariable(ctx context.Context, c *mysqlConn, name, value string) error {
	if _, err := c.runner().ExecContext(ctx, "SET SESSION "+name+" = ?", value); err != nil {
		return fmt.Errorf("%w: setting %s: %w", ErrConnect, name, err)
	}

	return nil
}

// renderVariable writes a numeric session variable into the statement, MySQL binding no such value.
func renderVariable(ctx context.Context, c *mysqlConn, name, value string) error {
	if err := checkDigits(name, value); err != nil {
		return err
	}

	if _, err := c.runner().ExecContext(ctx, "SET SESSION "+name+" = "+value); err != nil {
		return fmt.Errorf("%w: setting %s to %s: %w", ErrConnect, name, value, err)
	}

	return nil
}

// checkDigits refuses a value that is about to be written into a statement and is not a number.
func checkDigits(name, value string) error {
	if _, err := strconv.ParseUint(value, 10, 64); err != nil {
		return fmt.Errorf("%w: %s was given %q, which is written into the statement rather than "+
			"bound and must therefore be a number: %w", ErrConnect, name, value, err)
	}

	return nil
}

// readVariable reports what one session variable currently is, which is how a clamp becomes visible.
func readVariable(ctx context.Context, c *mysqlConn, name string) (string, error) {
	var value string

	if err := c.runner().QueryRowContext(ctx, "SELECT @@session."+name).Scan(&value); err != nil {
		return "", fmt.Errorf("%w: reading %s back: %w", ErrConnect, name, err)
	}

	return value, nil
}
