package pgtest

import (
	"context"
	"fmt"
	"strings"

	"github.com/emrebdr/migr/internal/driver"
)

// Drop removes one table if it is there, so a test can undo what a migration did outside a run.
func Drop(ctx context.Context, conn driver.Conn, schema, table string) error {
	for _, name := range []string{schema, table} {
		if err := checkIdentifier(name); err != nil {
			return err
		}
	}

	_, err := conn.Perform(ctx, driver.Query{
		SQL: `DROP TABLE IF EXISTS ` + quote(schema) + `.` + quote(table),
	})
	if err != nil {
		return fmt.Errorf("dropping %s.%s: %w", schema, table, err)
	}

	return nil
}

// checkIdentifier refuses a name this package will not interpolate, which is anything unusual.
func checkIdentifier(name string) error {
	if name == "" || strings.IndexFunc(name, unusual) >= 0 {
		return fmt.Errorf("%q is not a name this probe interpolates: lowercase letters, digits "+
			"and underscores only, which is what every table a test stages is called", name)
	}

	return nil
}

// unusual reports a character a staged table's name never carries.
func unusual(r rune) bool {
	return r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9')
}

// quote renders an identifier PostgreSQL reads exactly as written, as the driver's own quote does.
func quote(name string) string {
	return `"` + name + `"`
}
