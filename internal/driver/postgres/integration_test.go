//go:build integration

package postgres

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	conformance "github.com/emrebdr/migr/test/conformance/driver"
)

// servers are the PostgreSQL majors this driver is held to, and the environment variables
// that override the DSNs test/docker-compose.yml provides.
var servers = []struct {
	name  string
	env   string
	dsn   string
	major int
}{
	{
		name:  "postgres16",
		env:   "MIGR_TEST_PG16_DSN",
		dsn:   "postgres://migr:migr@127.0.0.1:55416/migr?sslmode=disable",
		major: 16,
	},
	{
		name:  "postgres17",
		env:   "MIGR_TEST_PG17_DSN",
		dsn:   "postgres://migr:migr@127.0.0.1:55417/migr?sslmode=disable",
		major: 17,
	},
}

// dsnFor returns the connection string for a server, preferring the environment override.
func dsnFor(env, fallback string) string {
	if override := os.Getenv(env); override != "" {
		return override
	}

	return fallback
}

// open connects to a server, failing rather than skipping when it is not there.
func open(t *testing.T, dsn string, opts driver.Options) driver.Conn {
	t.Helper()

	d, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	conn, err := d.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("Open: %v\n\nRun `make test-integration`, which brings the servers up first.", err)
	}

	t.Cleanup(func() {
		if err := conn.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	return conn
}

func TestConnectingToEveryServerUnderTest(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			conn := open(t, dsnFor(server.env, server.dsn), driver.Options{})

			reported := conn.ServerVersion()
			if !strings.HasPrefix(reported, strconv.Itoa(server.major)+".") {
				t.Errorf("ServerVersion() = %q, want a %d.x release", reported, server.major)
			}

			if conn.Capabilities().Server() != reported {
				t.Errorf("Capabilities().Server() = %q but ServerVersion() = %q",
					conn.Capabilities().Server(), reported)
				t.Log("Both report the same fact, so they are derived from one detection inside")
				t.Log("Open. Two detections could disagree, and a lock verdict keyed to the wrong")
				t.Log("one is exactly the outage invariant #13 exists to prevent.")
			}

			if conn.Capabilities().Driver() != Name {
				t.Errorf("the session declares capabilities for %q", conn.Capabilities().Driver())
			}

			checkHistory(t, conn)
		})
	}
}

// checkHistory holds a session's history store to what it must be before M3-T6 writes one.
func checkHistory(t *testing.T, conn driver.Conn) {
	t.Helper()

	store := conn.History()
	if store == nil {
		t.Error("the session handed back no history store")
		t.Log("History returns no error, so nil is a value no caller can check before it")
		t.Log("panics. A store that refuses is the only honest answer before M3-T6.")

		return
	}

	if err := store.Ensure(t.Context()); err == nil {
		t.Error("the history store created a table before M3-T6 wrote one")
	}
}

func TestAnUnreachableServerFailsClosed(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	_, err = d.Open(t.Context(), "postgres://migr:migr@127.0.0.1:1/migr?sslmode=disable")
	if err == nil {
		t.Fatal("opening a session against a closed port succeeded")
	}

	if !errors.Is(err, ErrConnect) {
		t.Errorf("Open failed with %v, want ErrConnect", err)
	}

	if strings.Contains(err.Error(), "migr:migr") {
		t.Errorf("the failure rendered the credentials: %s", err)
	}
}

// searchPath reads back what a session's schema option actually set.
func searchPath(t *testing.T, schema string) string {
	t.Helper()

	conn := open(t, dsnFor(servers[0].env, servers[0].dsn), driver.Options{Schema: schema})

	session, ok := conn.(*pgConn)
	if !ok {
		t.Fatalf("Open returned %T, not this package's session", conn)
	}

	var current string
	if err := session.conn.QueryRow(t.Context(), "SHOW search_path").Scan(&current); err != nil {
		t.Fatalf("reading search_path: %v", err)
	}

	return current
}

func TestAnOrdinarySchemaIsSetPlainly(t *testing.T) {
	if got := searchPath(t, "public"); got != "public" {
		t.Errorf("search_path = %q, want %q", got, "public")
		t.Log("quote_ident leaves a legitimate identifier unquoted, so the ordinary case is")
		t.Log("unchanged by the escaping the case below needs.")
	}
}

func TestTheSearchPathCannotBeEscaped(t *testing.T) {
	const injection = "public, pg_catalog"

	current := searchPath(t, injection)

	if !strings.Contains(current, `"`+injection+`"`) {
		t.Errorf("search_path is %q, want the schema kept whole as one quoted identifier", current)
		t.Log("config never validates schema -- not for identifier legality, not for length, not")
		t.Log("for commas. Setting it with a bound parameter through set_config is what makes an")
		t.Log("injected value a schema name nobody has rather than two search path entries.")
	}
}

func TestTheConformanceSuitePasses(t *testing.T) {
	for _, server := range servers {
		t.Run(server.name, func(t *testing.T) {
			d, err := New(driver.Options{})
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			conn := open(t, dsnFor(server.env, server.dsn), driver.Options{})

			conformance.Suite{Driver: d, Conn: conn}.Run(t)
		})
	}

	t.Log("First run of the conformance suite against a real driver rather than a fake. It")
	t.Log("passes because this driver refuses every operation and declares every operation")
	t.Log("unsupported -- the biconditional holds at zero as well as it will at twenty.")
}
