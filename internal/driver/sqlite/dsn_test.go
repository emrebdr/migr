package sqlite

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestTheConnectionStringFormsSQLiteReadsAllOpen(t *testing.T) {
	dir := t.TempDir()

	for name, dsn := range map[string]string{
		"a bare path":            filepath.Join(dir, "plain.db"),
		"a file URI":             "file:" + filepath.Join(dir, "uri.db"),
		"a URI with a parameter": "file:" + filepath.Join(dir, "cache.db") + "?_txlock=immediate",
		"an in-memory database":  memory,
		"a shared in-memory one": "file::memory:?cache=shared",
	} {
		t.Run(name, func(t *testing.T) {
			c := open(t, dsn, driver.Options{})

			exec(t, c, `CREATE TABLE users (id INTEGER PRIMARY KEY)`)
		})
	}
}

func TestAnEmptyConnectionStringIsRefused(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		_, err := parseDSN(dsn)
		if !errors.Is(err, ErrDSN) {
			t.Fatalf("parseDSN(%q) returned %v, want %v", dsn, err, ErrDSN)
		}

		if !strings.Contains(err.Error(), "temporary") {
			t.Errorf("the refusal does not say what an empty filename opens: %v", err)
		}
	}

	t.Log("SQLite reads an empty filename as a private temporary database that is deleted when the")
	t.Log("process exits -- measured, not assumed -- so migr up would apply every migration to a")
	t.Log("file nobody can find afterwards and report success.")
}

func TestAConnectionStringSettingAPragmaThisDriverOwnsIsRefused(t *testing.T) {
	for _, requested := range []string{
		"foreign_keys(0)",
		"FOREIGN_KEYS(1)",
		"busy_timeout(1)",
		" query_only(1) ",
	} {
		dsn := "file:app.db?_pragma=" + requested

		_, err := parseDSN(dsn)
		if !errors.Is(err, ErrDSN) {
			t.Fatalf("parseDSN(%q) returned %v, want %v", dsn, err, ErrDSN)
		}
	}

	t.Log("PostgreSQL pins lock_timeout after parsing, so a DSN that set it is overridden silently.")
	t.Log("foreign_keys(0) is different in kind: it asks to change what a migration means, and")
	t.Log("answering that by ignoring the operator is the failure mode this repository refuses.")
}

func TestAConnectionStringMayStillSetAPragmaThisDriverDoesNotOwn(t *testing.T) {
	dsn := "file:" + filepath.Join(t.TempDir(), "app.db") + "?_pragma=synchronous(1)"

	if _, err := parseDSN(dsn); err != nil {
		t.Fatalf("parseDSN refused a pragma this driver does not set: %v", err)
	}

	c := open(t, dsn, driver.Options{})

	if got := pragma(t, c, pragmaForeignKeys); got != pragmaOn {
		t.Errorf("foreign_keys is %q on a session whose DSN set another pragma", got)
	}
}

func TestAConnectionStringThatClaimsToBeAURIAndIsNotIsRefused(t *testing.T) {
	for name, dsn := range map[string]string{
		"a control character in the path": "file:app\x00.db",
		"an unreadable query":             "file:app.db?_pragma=%zz",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseDSN(dsn); !errors.Is(err, ErrDSN) {
				t.Fatalf("parseDSN returned %v, want %v", err, ErrDSN)
			}
		})
	}

	t.Log("url.Query() drops a parameter it cannot decode and reports nothing, so a _pragma hidden")
	t.Log("behind a bad escape would read as no _pragma at all. Refusing the whole query string is")
	t.Log("what keeps the check above from being evaded by a typo.")
}

func TestARefusalNeverRendersTheConnectionStringItself(t *testing.T) {
	secret := "file:/srv/app.db?_pragma=foreign_keys(0)&password=hunter2"

	_, err := parseDSN(secret)
	if err == nil {
		t.Fatal("parseDSN accepted a DSN setting a pragma this driver owns")
	}

	if strings.Contains(err.Error(), "hunter2") || strings.Contains(err.Error(), "/srv/app.db") {
		t.Errorf("the refusal renders the connection string: %v", err)
		t.Log("internal/config carries a DSN as a secret and reveals it only to Open, so a driver")
		t.Log("that printed one would undo that at the first bad character (M3-T1's DSNError).")
	}
}
