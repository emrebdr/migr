package postgres

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/emrebdr/migr/internal/driver"
)

// invalidWait bounds the one catalog read that names an index a failed build left behind.
const invalidWait = 5 * time.Second

// ErrInvalidIndex marks a concurrent build that failed and left its index behind INVALID.
var ErrInvalidIndex = errors.New("a concurrent index build failed and left the index behind INVALID")

// concurrentBuild reads the index a CREATE INDEX CONCURRENTLY names, quoted or not.
var concurrentBuild = regexp.MustCompile(`(?is)^\s*CREATE\s+(?:UNIQUE\s+)?INDEX\s+CONCURRENTLY\s+` +
	`(?:IF\s+NOT\s+EXISTS\s+)?("(?:[^"]|"")+"|[A-Za-z_][A-Za-z0-9_$]*)`)

// invalidQuery reports whether this namespace holds the index, and PostgreSQL has marked it unusable.
const invalidQuery = `SELECT NOT i.indisvalid FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid ` +
	`WHERE c.relname = $1 AND c.relnamespace = current_schema()::regnamespace`

// InvalidIndexError is a failed concurrent build, naming the index it left and the statement that drops it.
type InvalidIndexError struct {
	// Index is the index PostgreSQL left behind marked INVALID.
	Index string
	// Err is what stopped the build.
	Err error
}

// Error names the index left INVALID and the drop to run before the dirty run is settled.
func (e *InvalidIndexError) Error() string {
	return fmt.Sprintf("%v -- PostgreSQL left %s behind INVALID: it slows every write to the table "+
		"and no query uses it. migr drops it before trying the build again; where it could not, run "+
		"DROP INDEX CONCURRENTLY %s yourself, since a second build fails on its name", e.Err,
		quote(e.Index), quote(e.Index))
}

// Unwrap reports the sentinel and what stopped the build, so errors.Is finds either.
func (e *InvalidIndexError) Unwrap() []error {
	return []error{ErrInvalidIndex, e.Err}
}

// leftInvalid names the index a failed concurrent build left behind, where it left one.
func (c *pgConn) leftInvalid(ctx context.Context, stmt driver.Statement, err error) error {
	name, ok := builtIndex(stmt.SQL)
	if !ok || c.tx != nil {
		return err
	}

	bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), invalidWait)
	defer cancel()

	var invalid bool
	if c.conn.QueryRow(bound, invalidQuery, name).Scan(&invalid) != nil || !invalid {
		return err
	}

	return &InvalidIndexError{Index: name, Err: err}
}

// builtIndex reads the index a concurrent build names, unquoting it as PostgreSQL would.
func builtIndex(sql string) (string, bool) {
	found := concurrentBuild.FindStringSubmatch(sql)
	if found == nil {
		return "", false
	}

	name := found[1]
	if strings.HasPrefix(name, `"`) {
		return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`), true
	}

	return strings.ToLower(name), true
}
