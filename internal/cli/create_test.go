package cli

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/source/golang"
	"github.com/emrebdr/migr/internal/source/sql"
	"github.com/emrebdr/migr/internal/source/typescript"
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

func TestEveryLanguageMigrKnowsHasATemplate(t *testing.T) {
	t.Log("M7-T2 wrote the last one, so there is no longer a language migr create refuses. What")
	t.Log("replaces that refusal is this: a fourth language added to migr.yaml and not to the")
	t.Log("template map fails here rather than at the moment somebody runs migr create.")

	for _, lang := range config.Languages() {
		if _, ok := templates[lang]; !ok {
			t.Errorf("migr.yaml accepts migrations.language: %s, and migr create cannot write one", lang)
		}
	}

	if len(templates) != len(config.Languages()) {
		t.Errorf("templates holds %d languages and migr knows %d: one of them is not a language",
			len(templates), len(config.Languages()))
	}
}

func TestEveryLanguageOwnsItsExtensionsAndTheFirstIsWhatCreateWrites(t *testing.T) {
	t.Log("The relation is one language to many extensions -- migr-runtime-node loads .ts, .js and")
	t.Log(".mjs -- and M7-T8 wrote it as one-to-one, which refused a .js migration in a TypeScript")
	t.Log("project. The first entry is the one migr create writes, so the two maps cannot disagree.")

	claimed := make(map[string]config.Language, len(config.Languages()))

	for _, lang := range config.Languages() {
		exts, owned := extensions[lang]
		if !owned || len(exts) == 0 {
			t.Errorf("%s owns no extension, so nothing routes a migration to it", lang)

			continue
		}

		if exts[0] != templates[lang].ext {
			t.Errorf("%s owns %v first and migr create writes %s", lang, exts[0], templates[lang].ext)
		}

		for _, ext := range exts {
			if holder, taken := claimed[ext]; taken {
				t.Errorf("%s and %s both claim %s, so routing one file has two answers",
					holder, lang, ext)
			}

			claimed[ext] = lang

			if languageOf(ext) != lang {
				t.Errorf("languageOf(%s) = %q, and %s owns it", ext, languageOf(ext), lang)
			}
		}
	}

	if languageOf(".py") != "" {
		t.Errorf("languageOf(.py) = %q, and nothing here reads one", languageOf(".py"))
	}
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

	stdout, _, err := runIn(t, dir, noon, "create", "add_users")
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	if !strings.HasSuffix(strings.TrimSpace(stdout), ".ts") {
		t.Errorf("create reported %q, want a .ts file", stdout)
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

func TestCreateWritesAGoMigrationDiscoveryAccepts(t *testing.T) {
	dir := initialized(t)

	stdout, _, err := runIn(t, dir, noon, "create", "add_users", "--lang", "go")
	if err != nil {
		t.Fatalf("create --lang go: %v", err)
	}

	written := strings.TrimSpace(strings.TrimPrefix(stdout, "created "))

	parsed, err := migration.Parse(filepath.Base(written))
	if err != nil {
		t.Fatalf("create wrote %q, which discovery refuses: %v", written, err)
	}

	t.Log("The grammar records an extension and never judges it, so a .golang file passes")
	t.Log("discovery and then matches no runtime -- which is why this asserts the extension too.")

	if parsed.Ext != ".go" {
		t.Errorf("create wrote a %q file, want a .go one", parsed.Ext)
	}

	body, err := os.ReadFile(written)
	if err != nil {
		t.Fatalf("reading what create wrote: %v", err)
	}

	for _, want := range []string{"migr.Register", golang.SDK, "func (m Migration) Up("} {
		if !strings.Contains(string(body), want) {
			t.Errorf("the template carries no %q:\n%s", want, body)
		}
	}
}

func TestCreateWritesATypeScriptMigrationDiscoveryAccepts(t *testing.T) {
	for _, flag := range []string{"typescript", "ts"} {
		t.Run(flag, func(t *testing.T) {
			dir := initialized(t)

			stdout, _, err := runIn(t, dir, noon, "create", "add_users", "--lang", flag)
			if err != nil {
				t.Fatalf("create --lang %s: %v", flag, err)
			}

			written := strings.TrimSpace(strings.TrimPrefix(stdout, "created "))

			parsed, err := migration.Parse(filepath.Base(written))
			if err != nil {
				t.Fatalf("create wrote %q, which discovery refuses: %v", written, err)
			}

			if parsed.Ext != ".ts" {
				t.Errorf("create wrote a %q file, want a .ts one (source/typescript says %q)",
					parsed.Ext, typescript.Ext)
			}

			body, err := os.ReadFile(written)
			if err != nil {
				t.Fatalf("reading what create wrote: %v", err)
			}

			for _, want := range []string{"defineMigration", typescript.SDK, "async up(s)", "t."} {
				if !strings.Contains(string(body), want) {
					t.Errorf("the template carries no %q:\n%s", want, body)
				}
			}
		})
	}

	t.Log("`--lang ts` is the spelling CLAUDE.md documents, and it resolves to typescript before")
	t.Log("the template is looked up -- otherwise the alias would report an unknown language.")
}

func TestTheTypeScriptTemplateCarriesBothStylesAndTheChoiceOfUndo(t *testing.T) {
	body := typescript.Template

	t.Log("A module uses one export style or the other and offering both is refused, so a")
	t.Log("template showing only one leaves an author to discover the other from an error.")
	t.Log("It is the same argument as the Go template's: a migration that reaches the")
	t.Log("database itself owes an authored down or a declared irreversibility.")

	for _, want := range []string{
		"export default", "export async function up", "export const dependencies",
		"irreversible:", "one or the other", "no inverse to derive",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the template never mentions %q", want)
		}
	}
}

func TestTheTypeScriptTemplateShowsTheBuilderAndWhatForgettingApplyCosts(t *testing.T) {
	body := typescript.Template

	t.Log("The scaffold is where an author meets the SDK, and the builder emits nothing until")
	t.Log("apply -- so a template that showed the chain without the terminal call would teach")
	t.Log("the one mistake that makes a migration report success having changed nothing. This")
	t.Log("is TestTheGoTemplateShowsTheBuilder... asked of the SDK that mirrors it.")

	for _, want := range []string{
		"s.createTable(", "s.table(", ".apply()", "t.uuid()", "null()", "NOT NULL",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the template never mentions %q", want)
		}
	}
}

func TestTheGoTemplateSaysWhatTheEscapeHatchCosts(t *testing.T) {
	body := golang.Template

	t.Log("A derived down is refused outright for a migration that used s.Exec, so")
	t.Log("an author who reaches for it has to learn the price where they first meet it.")

	for _, want := range []string{"s.Exec", "Irreversible", "no inverse"} {
		if !strings.Contains(body, want) {
			t.Errorf("the template never mentions %q", want)
		}
	}

	if strings.Count(body, "s.Exec") < 2 {
		t.Error("the template mentions s.Exec once; say what it is and then what it costs")
	}
}

func TestTheGoTemplateShowsTheBuilderAndWhatForgettingApplyCosts(t *testing.T) {
	body := golang.Template

	t.Log("The scaffold is where an author meets the SDK, and the builder emits nothing until")
	t.Log("Apply -- so a template that showed the chain without the terminal call would teach")
	t.Log("the one mistake that makes a migration report success having changed nothing.")

	for _, want := range []string{"s.CreateTable(", "s.Table(", "Apply(ctx)", "Null", "NOT NULL"} {
		if !strings.Contains(body, want) {
			t.Errorf("the template never mentions %q", want)
		}
	}
}

func TestEveryTemplateIsWhatItClaimsToBe(t *testing.T) {
	for language, tmpl := range templates {
		if tmpl.ext != ".go" {
			continue
		}

		if _, err := parser.ParseFile(token.NewFileSet(), "migration.go", tmpl.body, parser.AllErrors); err != nil {
			t.Errorf("the %s template is not valid Go: %v", language, err)
		}
	}
}
