package mysql

import (
	"errors"
	"strings"
	"testing"
)

// goodDSN is a connection string this driver accepts, which every refusal below is a variation on.
const goodDSN = "migr:secret@tcp(127.0.0.1:3306)/appdb"

func TestAConnectionStringThisDriverAcceptsIsReadIntoADatabaseAndAName(t *testing.T) {
	source, err := parseDSN(goodDSN, "")
	if err != nil {
		t.Fatalf("parseDSN: %v", err)
	}

	if source.database != "appdb" {
		t.Errorf("the database is %q, want %q", source.database, "appdb")
	}

	if !strings.HasPrefix(source.config.ConnectionAttributes, attributeName+":") {
		t.Errorf("the session does not name itself: %q", source.config.ConnectionAttributes)
	}

	if _, err := source.connector(); err != nil {
		t.Errorf("building a connector from it: %v", err)
	}
}

func TestEveryFlagThisDriverOwnsIsRefusedRatherThanOverridden(t *testing.T) {
	for _, flag := range ownedFlags {
		t.Run(flag, func(t *testing.T) {
			_, err := parseDSN(goodDSN+"?"+flag+"=true", "")
			if !errors.Is(err, ErrDSN) {
				t.Fatalf("parseDSN returned %v, want %v", err, ErrDSN)
			}

			if !strings.Contains(err.Error(), flag) {
				t.Errorf("the refusal does not name the flag: %v", err)
			}
		})
	}

	t.Log("Each of the four changes what a statement or a value means: clientFoundRows changes what")
	t.Log("Perform reports, multiStatements lets one Query be several, parseTime replaces the")
	t.Log("server's own text with a time.Time, and interpolateParams splices arguments in.")
}

func TestEverySessionVariableThisDriverOwnsIsRefusedInTheConnectionString(t *testing.T) {
	for _, name := range ownedVariables {
		t.Run(name, func(t *testing.T) {
			_, err := parseDSN(goodDSN+"?"+name+"=1", "")
			if !errors.Is(err, ErrDSN) {
				t.Fatalf("parseDSN returned %v, want %v", err, ErrDSN)
			}

			if !strings.Contains(err.Error(), name) {
				t.Errorf("the refusal does not name the variable: %v", err)
			}
		})
	}

	t.Log("go-sql-driver sends an unrecognized parameter as SET <name>=<value> at connect, so a")
	t.Log("connection string can set any of these -- and applyVariables runs afterwards and would")
	t.Log("overwrite it with nobody told.")
}

func TestAnOwnedVariableIsRefusedHoweverTheConnectionStringCasedIt(t *testing.T) {
	for _, spelling := range []string{"SQL_MODE", "Sql_Mode", "  sql_mode  "} {
		if !ownsVariable(spelling) {
			t.Errorf("ownsVariable(%q) is false, and MySQL's variable names are case-insensitive",
				spelling)
		}
	}

	if ownsVariable("autocommit") {
		t.Error("ownsVariable claims a variable this driver does not set")
	}
}

func TestAConnectionStringWithNoDatabaseIsRefusedBeforeTheLockIsTaken(t *testing.T) {
	_, err := parseDSN("migr:secret@tcp(127.0.0.1:3306)/", "")
	if !errors.Is(err, ErrDSN) {
		t.Fatalf("parseDSN returned %v, want %v", err, ErrDSN)
	}

	t.Log("MySQL resolves every unqualified name against a database, so without one the first")
	t.Log("statement of the first migration fails -- after the migration lock has been taken and")
	t.Log("the history table has been asked for.")
}

func TestAnEmptyConnectionStringIsRefused(t *testing.T) {
	for _, dsn := range []string{"", "   "} {
		if _, err := parseDSN(dsn, ""); !errors.Is(err, ErrDSN) {
			t.Errorf("parseDSN(%q) returned %v, want %v", dsn, err, ErrDSN)
		}
	}
}

func TestAConnectionStringTheLibraryCannotReadIsRefusedWithoutBeingRendered(t *testing.T) {
	_, err := parseDSN("this is not a dsn", "")
	if !errors.Is(err, ErrDSN) {
		t.Fatalf("parseDSN returned %v, want %v", err, ErrDSN)
	}

	if strings.Contains(err.Error(), "this is not a dsn") {
		t.Errorf("the refusal renders the connection string, which may hold a password: %v", err)
	}
}

func TestARefusalNeverRendersTheConnectionString(t *testing.T) {
	for _, dsn := range []string{
		goodDSN + "?parseTime=true",
		"migr:secret@tcp(127.0.0.1:3306)/",
		goodDSN + "?sql_mode=ANSI",
	} {
		_, err := parseDSN(dsn, "")
		if err == nil {
			t.Fatalf("parseDSN(%q) was accepted", dsn)
		}

		if strings.Contains(err.Error(), "secret") {
			t.Errorf("the refusal carries the password: %v", err)
		}
	}
}

func TestAConfiguredSchemaMustBeTheDatabaseTheConnectionStringNames(t *testing.T) {
	if _, err := parseDSN(goodDSN, "appdb"); err != nil {
		t.Errorf("a schema restating the database was refused: %v", err)
	}

	_, err := parseDSN(goodDSN, "other")
	if !errors.Is(err, ErrSchema) {
		t.Fatalf("parseDSN returned %v, want %v", err, ErrSchema)
	}

	for _, part := range []string{"other", "appdb"} {
		if !strings.Contains(err.Error(), part) {
			t.Errorf("the refusal does not name %q: %v", part, err)
		}
	}

	t.Log("MySQL's SCHEMA is a synonym for its DATABASE, so migr.yaml's schema and the connection")
	t.Log("string name the same thing -- and two sources for one namespace can only disagree.")
}

func TestTheSessionNameCarriesNeitherSeparatorAnAttributeListIsParsedOn(t *testing.T) {
	for _, bad := range []string{",", ":"} {
		if strings.Contains(applicationName(), bad) {
			t.Errorf("the session name %q holds %q, which ends a key or a pair", applicationName(), bad)
		}
	}

	if got := attributeSafe("migr 1.2,3:4"); got != "migr 1.2-3-4" {
		t.Errorf("attributeSafe returned %q", got)
	}
}
