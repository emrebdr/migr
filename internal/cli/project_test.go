package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/source/sql"
)

// projectConfig is the scaffold init writes, pointed at the fake driver instead of a real one.
var projectConfig = strings.Replace(scaffoldConfig, "driver: postgres", "driver: "+fakeName, 1)

// stagedMigration is one migration a test writes, in the two-section format migr create writes.
type stagedMigration struct {
	// name is the snake_case name the file carries.
	name string
	// up is what it does, as SQL.
	up string
	// down is what it undoes, as SQL, empty for a migration that authors none.
	down string
	// declared is the reason it gives for having no undo at all, empty when it gives none.
	declared string
}

// body renders the file this migration is stored as.
func (m stagedMigration) body() string {
	if m.declared != "" {
		return sql.MarkerUp + "\n" + m.up + "\n\n" + sql.MarkerIrreversible + " " + m.declared + "\n"
	}

	return sql.MarkerUp + "\n" + m.up + "\n\n" + sql.MarkerDown + "\n" + m.down + "\n"
}

// versionAt returns the version the nth staged migration carries, one second apart.
func versionAt(n int) string {
	return fmt.Sprintf("20260727T1530%02d", n)
}

// fileAt returns the filename the nth staged migration is stored under.
func fileAt(n int, name string) string {
	return versionAt(n) + "_" + name + sql.Ext
}

// project writes a migr.yaml naming the fake driver, plus the migrations given, and returns both.
func project(t *testing.T, migrations ...stagedMigration) (string, *fake, *driver.Registry) {
	t.Helper()

	dir := t.TempDir()
	write(t, filepath.Join(dir, config.DefaultFilename), projectConfig)

	if err := os.MkdirAll(filepath.Join(dir, scaffoldDir), 0o750); err != nil {
		t.Fatalf("staging a migrations directory: %v", err)
	}

	for i, m := range migrations {
		write(t, filepath.Join(dir, scaffoldDir, fileAt(i, m.name)), m.body())
	}

	f := &fake{}

	return dir, f, f.registry(t)
}

// write puts a file where a test staged it, failing the test rather than the command under test.
func write(t *testing.T, path, body string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("staging %s: %v", path, err)
	}
}

// twoTables is the project most of these tests run against: two migrations, both reversible.
func twoTables() []stagedMigration {
	return []stagedMigration{
		{
			name: "add_users",
			up:   "CREATE TABLE users (id bigint);\nCREATE INDEX idx_users ON users (id);",
			down: "DROP TABLE users;",
		},
		{
			name: "add_teams",
			up:   "CREATE TABLE teams (id bigint);",
			down: "DROP TABLE teams;",
		},
	}
}
