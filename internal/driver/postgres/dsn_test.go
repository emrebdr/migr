package postgres

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// leakyPassword is a password with a space in it, which is what pgx's redaction cannot handle.
const leakyPassword = "my secret"

// unparsableDSN carries that password and fails to parse, which is when pgx renders it.
// The key order matters: the invalid port has to be reached before the password.
const unparsableDSN = "host=localhost port=notanumber password=" + leakyPassword

func TestAnUnparsableDSNNeverReachesTheMessage(t *testing.T) {
	_, err := parseDSN(unparsableDSN)
	if err == nil {
		t.Fatal("a connection string with an invalid sslmode parsed successfully")
	}

	for _, secret := range []string{leakyPassword, "secret", "localhost", "notanumber"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error text contains %q: %s", secret, err)
			t.Log("pgx renders the connection string through a best-effort redaction, and its own")
			t.Log("source says a static string would be safer. The keyword/value fallback matches")
			t.Log("password=[^ ]*, so an unquoted password with a space keeps its tail -- and a")
			t.Log("string that will not parse is exactly the case the URL branch cannot help with.")
		}
	}
}

func TestAnUnparsableDSNIsStillDiagnosable(t *testing.T) {
	_, err := parseDSN(unparsableDSN)

	if !errors.Is(err, ErrDSN) {
		t.Errorf("parseDSN returned %v, want ErrDSN", err)
	}

	var cause *pgconn.ParseConfigError
	if !errors.As(err, &cause) {
		t.Error("the pgx cause is not recoverable from the wrapper")
		t.Log("Withholding the text must not mean discarding the diagnosis: a caller that wants")
		t.Log("the underlying failure can still reach it, it simply is not printed by default.")
	}
}

func TestPgxLeaksWhatThisDriverWithholds(t *testing.T) {
	_, err := pgx.ParseConfig(unparsableDSN)
	if err == nil {
		t.Fatal("pgx accepted a connection string this test needs it to reject")
	}

	if strings.Contains(err.Error(), "secret") {
		t.Log("Confirmed against this pgx build: its own error text leaks the password's tail,")
		t.Log("which is why parseDSN never renders it. If this stops being true the wrapper")
		t.Log("becomes belt-and-braces rather than load-bearing, and nothing here breaks.")

		return
	}

	t.Log("This pgx build did not leak the password for this input. The wrapper stays: pgx's")
	t.Log("redaction is documented in its own source as best-effort, and the guarantee this")
	t.Log("driver makes about DSNs cannot rest on that.")
}

func TestAValidDSNParses(t *testing.T) {
	cfg, err := parseDSN("postgres://migr:hunter2@localhost:5432/migr?sslmode=disable")
	if err != nil {
		t.Fatalf("parseDSN: %v", err)
	}

	if cfg.User != "migr" || cfg.Database != "migr" || cfg.Port != 5432 {
		t.Errorf("parsed user %q database %q port %d, want migr/migr/5432",
			cfg.User, cfg.Database, cfg.Port)
	}
}
