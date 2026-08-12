package cli

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/source/sql"
)

// migrations opens the migrations directory of an initialized project, as discovery is handed it.
func migrations(t *testing.T, dir string) fs.FS {
	t.Helper()

	return os.DirFS(filepath.Join(dir, scaffoldDir))
}

// writeConfigNaming stages a config for a language other than the one init writes.
func writeConfigNaming(t *testing.T, dir string, lang config.Language) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://localhost:5432/migr")

	body := strings.Replace(scaffoldConfig, "language: "+string(config.LanguageSQL),
		"language: "+string(lang), 1)

	if err := os.WriteFile(filepath.Join(dir, config.DefaultFilename), []byte(body), 0o600); err != nil {
		t.Fatalf("staging a config: %v", err)
	}

	if err := os.MkdirAll(filepath.Join(dir, scaffoldDir), 0o750); err != nil {
		t.Fatalf("staging a migrations directory: %v", err)
	}
}

func TestCreateWritesAMigrationDiscoveryReadsBack(t *testing.T) {
	dir := initialized(t)

	if _, _, err := runIn(t, dir, noon, "create", "add_users"); err != nil {
		t.Fatalf("create: %v", err)
	}

	if _, _, err := runIn(t, dir, noon.Add(time.Minute), "create", "add_teams"); err != nil {
		t.Fatalf("create: %v", err)
	}

	found, err := migration.Discover(migrations(t, dir))
	if err != nil {
		t.Fatalf("discovery refuses what create wrote: %v", err)
	}

	want := []migration.Migration{
		{Version: noonVersion, Name: "add_users", Ext: sql.Ext},
		{Version: "20260727T153100", Name: "add_teams", Ext: sql.Ext},
	}

	if len(found) != len(want) || found[0] != want[0] || found[1] != want[1] {
		t.Fatalf("discovery found %+v, want %+v", found, want)
	}

	t.Log("This is the whole task in one assertion: what create writes is what the rest of migr")
	t.Log("reads. Both go through migration.New, so the two cannot drift into disagreeing.")
}

func TestTheFileCreateWritesIsTheTemplate(t *testing.T) {
	dir := initialized(t)

	if _, _, err := runIn(t, dir, noon, "create", "add_users"); err != nil {
		t.Fatalf("create: %v", err)
	}

	written, err := os.ReadFile(filepath.Join(dir, scaffoldDir, noonVersion+"_add_users"+sql.Ext))
	if err != nil {
		t.Fatalf("reading what create wrote: %v", err)
	}

	if string(written) != sql.Template {
		t.Errorf("create wrote:\n%s\nwant the template:\n%s", written, sql.Template)
	}
}

func TestCreateVersionsAMigrationInUTC(t *testing.T) {
	dir := initialized(t)

	stdout, _, err := runIn(t, dir, noon, "create", "add_users")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !strings.Contains(stdout, noonVersion) {
		t.Errorf("create reported %q, want the instant %s in UTC", stdout, noon)
		t.Log("The clock here reads 17:30 in a zone two hours ahead. A version is UTC, and a")
		t.Log("migration created in Berlin must sort against one created in Denver.")
	}
}

func TestCreateRefusesANameTheGrammarRefuses(t *testing.T) {
	dir := initialized(t)

	_, _, err := runIn(t, dir, noon, "create", "add-users")
	if err == nil {
		t.Fatal("create wrote a file discovery would refuse to read")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d: the argument is wrong, and knowing that needs no database",
			got, ExitValidation)
	}

	for _, want := range []string{"add-users", "snake_case name", "20260727T153000_add_users.sql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q: %v", want, err)
		}
	}

	t.Log("The refusal is internal/migration's own, naming the grammar and an example. Quietly")
	t.Log("rewriting the name to add_users would be a second opinion about what a name may be,")
	t.Log("and the file on disk would not be the one that was asked for.")
}

func TestCreateRefusesALanguageWhoseTemplateHasNotArrived(t *testing.T) {
	dir := initialized(t)

	for _, tc := range []struct{ flag, task string }{
		{flag: "go", task: "M6-T7"},
		{flag: "typescript", task: "M7-T2"},
		{flag: "ts", task: "M7-T2"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			_, _, err := runIn(t, dir, noon, "create", "add_users", "--lang", tc.flag)
			if err == nil {
				t.Fatal("create wrote a migration in a language nothing can run")
			}

			if got := ExitCode(err); got != ExitValidation {
				t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
			}

			if !strings.Contains(err.Error(), tc.task) {
				t.Errorf("the refusal does not name the task that adds it: %v", err)
				t.Log("An honest refusal naming the milestone beats a stub that will not compile,")
				t.Log("and it tells a reader whether to wait or to write SQL.")
			}
		})
	}

	t.Log("`--lang ts` is the spelling CLAUDE.md documents, and it resolves to typescript before")
	t.Log("support is checked -- otherwise the alias would report an unknown language instead.")
}

func TestCreateRefusesALanguageMigrDoesNotKnow(t *testing.T) {
	dir := initialized(t)

	_, _, err := runIn(t, dir, noon, "create", "add_users", "--lang", "cobol")
	if err == nil {
		t.Fatal("create accepted a language nothing defines")
	}

	for _, lang := range config.Languages() {
		if !strings.Contains(err.Error(), string(lang)) {
			t.Errorf("the refusal does not offer %q as an alternative: %v", lang, err)
		}
	}
}

func TestCreateWritesTheLanguageTheConfigNames(t *testing.T) {
	dir := t.TempDir()
	writeConfigNaming(t, dir, config.LanguageTypeScript)

	_, _, err := runIn(t, dir, noon, "create", "add_users")
	if err == nil {
		t.Fatal("create ignored migrations.language and wrote something else")
	}

	if !strings.Contains(err.Error(), "M7-T2") {
		t.Errorf("create did not default to the language the config names: %v", err)
		t.Log("--lang is an override, and a create that always wrote SQL would look correct in")
		t.Log("every test written against a scaffolded project, which names SQL already.")
	}
}

func TestCreateRefusesAMigrationsDirectoryThatIsNotThere(t *testing.T) {
	dir := initialized(t)

	if err := os.Remove(filepath.Join(dir, scaffoldDir)); err != nil {
		t.Fatalf("removing the migrations directory: %v", err)
	}

	_, _, err := runIn(t, dir, noon, "create", "add_users")
	if err == nil {
		t.Fatal("create wrote into a directory that does not exist")
	}

	if !errorSays(err, "migr init") {
		t.Errorf("the refusal does not say how to get a migrations directory: %v", err)
	}

	if got := ExitCode(err); got != ExitRuntime {
		t.Errorf("ExitCode = %d, want %d", got, ExitRuntime)
	}

	t.Log("Discovery refuses to read a directory that is not there rather than reading it as an")
	t.Log("empty project. Writing into one migr would then have to create is the same mistake:")
	t.Log("a typo in migrations.dir would build the wrong tree and migr up would find nothing.")
}

func TestCreateRefusesASecondMigrationClaimingOneVersion(t *testing.T) {
	dir := initialized(t)

	if _, _, err := runIn(t, dir, noon, "create", "add_users"); err != nil {
		t.Fatalf("create: %v", err)
	}

	_, _, err := runIn(t, dir, noon, "create", "add_teams")
	if err == nil {
		t.Fatal("two migrations were written claiming one version")
	}

	if !errorSays(err, noonVersion, noonVersion+"_add_users"+sql.Ext) {
		t.Errorf("the refusal names neither the version nor the file holding it: %v", err)
	}

	if exists(t, filepath.Join(dir, scaffoldDir, noonVersion+"_add_teams"+sql.Ext)) {
		t.Error("the second migration was written anyway")
	}

	if _, err := migration.Discover(migrations(t, dir)); err != nil {
		t.Errorf("discovery refuses what survived: %v", err)
	}

	t.Log("create is the only place a duplicate version can be prevented at all. Left to")
	t.Log("discovery, the same failure arrives at the next `migr up` and blocks every migration")
	t.Log("in the project until somebody renames a file.")
}

func TestCreateWithoutAConfigIsAValidationFailure(t *testing.T) {
	_, _, err := runIn(t, t.TempDir(), noon, "create", "add_users")
	if err == nil {
		t.Fatal("create ran in a directory that was never initialized")
	}

	if got := ExitCode(err); got != ExitValidation {
		t.Errorf("ExitCode = %d, want %d", got, ExitValidation)
	}

	if !errorSays(err, "migr init") {
		t.Errorf("the refusal does not say how to get a config: %v", err)
	}
}

func TestCreateJSONNamesTheFileItWrote(t *testing.T) {
	dir := initialized(t)

	stdout, stderr, err := runIn(t, dir, noon, "create", "add_users", "--json")
	if err != nil {
		t.Fatalf("create --json: %v", err)
	}

	var result struct {
		File     string `json:"file"`
		Version  string `json:"version"`
		Name     string `json:"name"`
		Language string `json:"language"`
	}

	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\ngot: %q", err, stdout)
	}

	if result.Version != noonVersion || result.Name != "add_users" || result.Language != "sql" {
		t.Errorf("create reported %+v, want the migration it wrote", result)
	}

	if !exists(t, result.File) {
		t.Errorf("create named a file it did not write: %q", result.File)
	}

	if stderr != "" {
		t.Errorf("nothing should reach stderr at default verbosity, got %q", stderr)
	}
}

func TestClaimantFindsTheFileHoldingAVersion(t *testing.T) {
	entries, err := fs.ReadDir(fstest.MapFS{
		"20260727T153000_add_users.sql":  {},
		"20260727T160000_add_teams.sql":  {},
		"20260727T153000-not-a-migr.sql": {},
		"README.md":                      {},
		".gitkeep":                       {},
	}, ".")
	if err != nil {
		t.Fatalf("reading the fixture: %v", err)
	}

	tests := []struct {
		version string
		want    string
	}{
		{version: "20260727T153000", want: "20260727T153000_add_users.sql"},
		{version: "20260727T160000", want: "20260727T160000_add_teams.sql"},
		{version: "20260727T170000", want: ""},
	}

	for _, tt := range tests {
		if got := claimant(entries, tt.version); got != tt.want {
			t.Errorf("claimant(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}

	t.Log("The scan compares parsed versions rather than filenames, so a second name under one")
	t.Log("version is caught and a file that is not a migration at all is left to discovery,")
	t.Log("which refuses it with a message about the file rather than about the version.")
}

// errorSays reports whether an error's message contains every phrase.
func errorSays(err error, phrases ...string) bool {
	for _, phrase := range phrases {
		if !strings.Contains(err.Error(), phrase) {
			return false
		}
	}

	return true
}
