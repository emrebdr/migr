package migration

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emrebdr/migr/internal/remedy"
)

// valid is every filename this package accepts, with what each one parses to.
var valid = []struct {
	filename string
	want     Migration
}{
	{
		filename: "20260727T153000_add_users.sql",
		want:     Migration{Version: "20260727T153000", Name: "add_users", Ext: ".sql"},
	},
	{
		filename: "20260727T153000_add_users.ts",
		want:     Migration{Version: "20260727T153000", Name: "add_users", Ext: ".ts"},
	},
	{
		filename: "20240229T000000_leap_day.go",
		want:     Migration{Version: "20240229T000000", Name: "leap_day", Ext: ".go"},
	},
	{
		filename: "19700101T000000_epoch.mjs",
		want:     Migration{Version: "19700101T000000", Name: "epoch", Ext: ".mjs"},
	},
	{
		filename: "20261231T235959_last_second_of_the_year.js",
		want:     Migration{Version: "20261231T235959", Name: "last_second_of_the_year", Ext: ".js"},
	},
	{
		filename: "20260727T153000_2fa.sql",
		want:     Migration{Version: "20260727T153000", Name: "2fa", Ext: ".sql"},
	},
	{
		filename: "20260727T153000_add_users_v2.sql",
		want:     Migration{Version: "20260727T153000", Name: "add_users_v2", Ext: ".sql"},
	},
}

func TestAWellFormedNameParsesToItsThreeParts(t *testing.T) {
	for _, tc := range valid {
		t.Run(tc.filename, func(t *testing.T) {
			got, err := Parse(tc.filename)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.filename, err)
			}

			if got != tc.want {
				t.Errorf("Parse(%q) = %+v, want %+v", tc.filename, got, tc.want)
			}
		})
	}
}

func TestFilenameIsTheInverseOfParse(t *testing.T) {
	for _, tc := range valid {
		t.Run(tc.filename, func(t *testing.T) {
			parsed, err := Parse(tc.filename)
			if err != nil {
				t.Fatalf("Parse(%q): %v", tc.filename, err)
			}

			if rendered := parsed.Filename(); rendered != tc.filename {
				t.Errorf("Filename() = %q, want %q", rendered, tc.filename)
			}

			again, err := Parse(parsed.Filename())
			if err != nil {
				t.Fatalf("Parse(Filename()): %v", err)
			}

			if again != parsed {
				t.Errorf("Parse(Filename()) = %+v, want %+v", again, parsed)
			}
		})
	}

	t.Log("One format, one implementation. M5-T1's `migr create` builds a Migration and renders")
	t.Log("it rather than concatenating a version, an underscore, and a name of its own -- two")
	t.Log("spellings of one file format is how a tool stops reading what it just wrote.")
}

// malformedNames are the names this package refuses, and the word each refusal must contain.
var malformedNames = []struct {
	name     string
	filename string
	says     string
}{
	{
		name:     "a hyphen where the grammar wants an underscore",
		filename: "20260727T153000-add-users.ts",
		says:     "no _ separating",
	},
	{
		name:     "no timestamp at all",
		filename: "add_users.sql",
		says:     "not a YYYYMMDDThhmmss UTC timestamp",
	},
	{
		name:     "the thirtieth of February",
		filename: "20260230T000000_add_users.sql",
		says:     "day out of range",
	},
	{
		name:     "the twenty-ninth of a February that has none",
		filename: "20260229T000000_add_users.sql",
		says:     "day out of range",
	},
	{
		name:     "the twenty-fifth hour",
		filename: "20260727T253000_add_users.sql",
		says:     "hour out of range",
	},
	{
		name:     "the thirteenth month",
		filename: "20261301T000000_add_users.sql",
		says:     "month out of range",
	},
	{
		name:     "a sixtieth minute",
		filename: "20260727T156000_add_users.sql",
		says:     "minute out of range",
	},
	{
		name:     "a lowercase separator in the timestamp",
		filename: "20260727t153000_add_users.sql",
		says:     "not a YYYYMMDDThhmmss UTC timestamp",
	},
	{
		name:     "a timestamp one digit short",
		filename: "2026072T153000_add_users.sql",
		says:     "not a YYYYMMDDThhmmss UTC timestamp",
	},
	{
		name:     "a timestamp one digit long",
		filename: "20260727T1530000_add_users.sql",
		says:     "extra text",
	},
	{
		name:     "nothing before the first underscore",
		filename: "_add_users.sql",
		says:     "no version before its first _",
	},
	{
		name:     "nothing after the version",
		filename: "20260727T153000_.sql",
		says:     "no name after its version",
	},
	{
		name:     "a capitalized name",
		filename: "20260727T153000_AddUsers.sql",
		says:     "not snake_case",
	},
	{
		name:     "a doubled underscore",
		filename: "20260727T153000_add__users.sql",
		says:     "leading, trailing, or doubled _",
	},
	{
		name:     "a trailing underscore",
		filename: "20260727T153000_add_users_.sql",
		says:     "leading, trailing, or doubled _",
	},
	{
		name:     "a name carrying a second dot",
		filename: "20260727T153000_add_users.test.ts",
		says:     "not snake_case",
	},
	{
		name:     "no extension",
		filename: "20260727T153000_add_users",
		says:     "no extension",
	},
	{
		name:     "a bare dot for an extension",
		filename: "20260727T153000_add_users.",
		says:     "bare dot",
	},
	{
		name:     "a space in the name",
		filename: "20260727T153000_add users.sql",
		says:     "not snake_case",
	},
}

func TestAMalformedNameIsRefusedAndSaysWhy(t *testing.T) {
	for _, tc := range malformedNames {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.filename)
			if err == nil {
				t.Fatalf("Parse(%q) = %+v, want a refusal", tc.filename, got)
			}

			if !errors.Is(err, ErrMalformed) {
				t.Errorf("Parse(%q) failed with %v, want ErrMalformed", tc.filename, err)
			}

			checkNamesTheFile(t, err, tc.filename)

			if !strings.Contains(err.Error(), tc.says) {
				t.Errorf("the refusal does not say %q:\n  %v", tc.says, err)
				t.Log("Half the done-when is that a malformed name produces a clear error. An")
				t.Log("error type nobody can read satisfies that on paper and nowhere else.")
			}
		})
	}
}

// checkNamesTheFile holds a refusal to naming the file, the grammar, and what to do about it.
func checkNamesTheFile(t *testing.T, err error, filename string) {
	t.Helper()

	message := err.Error()

	for _, want := range []string{filename, grammar, example} {
		if !strings.Contains(message, want) {
			t.Errorf("the refusal does not contain %q:\n  %v", want, err)
		}
	}

	if got := remedy.Of(err); got != renameOrMove {
		t.Errorf("the refusal says to %q next, want %q", got, renameOrMove)
	}

	if !strings.HasPrefix(message, filename) {
		t.Errorf("the refusal does not open with the file it is about:\n  %v", err)
		t.Log("An operator scanning a wall of output finds the filename first or not at all.")
	}
}

func TestTheGrammarAndItsExampleAgree(t *testing.T) {
	parsed, err := Parse(example)
	if err != nil {
		t.Fatalf("the example this package prints in every refusal does not parse: %v", err)
	}

	if parsed.Filename() != example {
		t.Errorf("the example renders back as %q", parsed.Filename())
	}

	if !strings.Contains(grammar, versionShape) {
		t.Errorf("the grammar %q does not state the timestamp shape %q", grammar, versionShape)
	}
}

func TestAnEmptyNameIsNotAMigration(t *testing.T) {
	if got, err := Parse(""); err == nil {
		t.Fatalf("Parse(\"\") = %+v, want a refusal", got)
	}
}

// berlin is a zone two hours ahead of UTC, so a wall clock there disagrees with the version.
var berlin = time.FixedZone("CEST", 2*60*60)

func TestNewRendersTheVersionInUTC(t *testing.T) {
	local := time.Date(2026, 7, 27, 17, 30, 0, 0, berlin)

	m, err := New(local, "add_users", ".sql")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if m.Version != "20260727T153000" {
		t.Errorf("New at %s produced version %q, want the same instant in UTC", local, m.Version)
		t.Log("A version is a UTC timestamp, and every caller holds a local time.Now. Converting")
		t.Log("in the caller means every SDK's create has to remember; converting here means one")
		t.Log("of them forgetting writes wall-clock digits into the field that orders migrations.")
	}
}

func TestNewTruncatesToTheSecondItsGrammarHolds(t *testing.T) {
	m, err := New(time.Date(2026, 7, 27, 15, 30, 0, 999999999, time.UTC), "add_users", ".sql")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if m.Version != "20260727T153000" {
		t.Errorf("New produced version %q, want the second it falls in", m.Version)
	}
}

func TestNewProducesWhatParseAccepts(t *testing.T) {
	for _, tc := range valid {
		t.Run(tc.filename, func(t *testing.T) {
			at, err := time.Parse(versionLayout, tc.want.Version)
			if err != nil {
				t.Fatalf("the fixture's own version does not parse: %v", err)
			}

			m, err := New(at, tc.want.Name, tc.want.Ext)
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			if m != tc.want {
				t.Errorf("New = %+v, want %+v", m, tc.want)
			}

			if m.Filename() != tc.filename {
				t.Errorf("New(...).Filename() = %q, want %q", m.Filename(), tc.filename)
			}
		})
	}

	t.Log("New validates by parsing the filename it just rendered, so `migr create` cannot")
	t.Log("write a file Discover would refuse to read -- the two agree by construction rather")
	t.Log("than by both being maintained.")
}

func TestNewRefusesANameTheGrammarRefuses(t *testing.T) {
	at := time.Date(2026, 7, 27, 15, 30, 0, 0, time.UTC)

	for _, name := range []string{"add-users", "AddUsers", "add users", "", "add__users", "_add"} {
		t.Run(name, func(t *testing.T) {
			got, err := New(at, name, ".sql")
			if err == nil {
				t.Fatalf("New(%q) = %+v, want a refusal", name, got)
			}

			if !errors.Is(err, ErrMalformed) {
				t.Errorf("New(%q) refused with %v, want ErrMalformed", name, err)
			}

			checkNamesTheFile(t, err, "20260727T153000_"+name+".sql")
		})
	}

	t.Log("`migr create add-users` gets this package's own refusal, naming the grammar and the")
	t.Log("example. Normalizing the name instead would be a second opinion about what a name")
	t.Log("may be, and the file on disk would not be the one the operator asked for.")
}

func TestNewRefusesAnExtensionThatNamesNoFileType(t *testing.T) {
	at := time.Date(2026, 7, 27, 15, 30, 0, 0, time.UTC)

	for _, ext := range []string{"", ".", "sql"} {
		if got, err := New(at, "add_users", ext); err == nil {
			t.Errorf("New with extension %q = %+v, want a refusal", ext, got)
		}
	}
}
