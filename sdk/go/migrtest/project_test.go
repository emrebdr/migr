package migrtest

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

// How this suite gets a migr to drive, and why it does not skip when it has none.
//
// MIGR_BIN first, so CI or a developer can point at a binary that is already built. Otherwise it
// builds this working tree's, because that is the engine these assertions are about -- a migr that
// happened to be on PATH could be an old release, and a suite that measured it would be measuring
// something nobody chose. The second rung being a build rather than a skip is what keeps this test
// inside make test, which CI runs before make build.

// theEngine builds the CLI once per package run, since every case here drives the same one.
var theEngine = sync.OnceValues(buildEngine)

// builtIn is the directory the built binary lives in, which TestMain removes.
var builtIn string

// TestMain removes what this suite built, which is the only thing it leaves outside a TempDir.
func TestMain(m *testing.M) {
	code := m.Run()

	if builtIn != "" {
		_ = os.RemoveAll(builtIn)
	}

	os.Exit(code)
}

// buildEngine resolves the binary this suite drives, building this repository's when none is named.
func buildEngine() (string, error) {
	if named, set := os.LookupEnv(BinaryVar); set {
		return stated(named)
	}

	dir, err := os.MkdirTemp("", "migrtest-engine-")
	if err != nil {
		return "", fmt.Errorf("making somewhere to build migr: %w", err)
	}

	builtIn = dir
	binary := filepath.Join(dir, "migr")

	build := exec.Command("go", "build", "-o", binary, "./cmd/migr")
	build.Dir = filepath.Join("..", "..", "..")

	if out, err := build.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building migr from this repository: %w\n%s", err, out)
	}

	return binary, nil
}

// engine reports the migr this case drives.
func engine(t *testing.T) string {
	t.Helper()

	binary, err := theEngine()
	if err != nil {
		t.Fatalf("this suite drives the real binary: %v", err)
	}

	return binary
}

// theProjectModule is the module staged migrations are built inside, pointing at this SDK.
const theProjectModule = `module example.com/migrtest

go 1.26

require github.com/emrebdr/migr/sdk/go v0.0.0

replace github.com/emrebdr/migr/sdk/go => `

// fixture is one migration file a staged project holds.
type fixture struct {
	// name is the filename, which carries the version.
	name string
	// body is the migration itself.
	body string
}

// addAccounts creates a table and authors the down that drops it.
var addAccounts = fixture{
	name: "20260101T000000_add_accounts.go",
	body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddAccounts{}) }

// AddAccounts creates the table this project is about.
type AddAccounts struct{}

// Up creates it.
func (m AddAccounts) Up(ctx context.Context, s *migr.Schema) error {
	return s.CreateTable("accounts").
		Column("id", migr.Int64()).
		Column("email", migr.String(255)).
		PrimaryKey("id").
		Apply(ctx)
}

// Down drops it, recording what it held.
func (m AddAccounts) Down(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").Drop().Was(migr.TableDef{
		Columns: []migr.Column{
			{Name: "id", Type: migr.Int64()},
			{Name: "email", Type: migr.String(255)},
		},
		PrimaryKey: []string{"id"},
	}).Apply(ctx)
}
`,
}

// addTier adds a nullable column and authors no down, so the engine derives its inverse.
var addTier = fixture{
	name: "20260101T000100_add_tier.go",
	body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(AddTier{}) }

// AddTier adds one column and leaves the undo to the engine.
type AddTier struct{}

// Up adds it.
func (m AddTier) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").AddColumn("tier", migr.Int32()).Null().Apply(ctx)
}
`,
}

// dropsWithoutRecording drops an index naming nothing it was, which nothing can invert.
var dropsWithoutRecording = fixture{
	name: "20260101T000200_drop_index.go",
	body: `package migrations

import (
	"context"

	migr "github.com/emrebdr/migr/sdk/go"
)

func init() { migr.Register(DropIndex{}) }

// DropIndex drops an index, recording nothing for a derivation to put back.
type DropIndex struct{}

// Up drops it.
func (m DropIndex) Up(ctx context.Context, s *migr.Schema) error {
	return s.Table("accounts").DropIndex("idx_accounts_email").Apply(ctx)
}
`,
}

// stage writes a module whose migrations directory holds the fixtures given, and returns that directory.
func stage(t *testing.T, fixtures ...fixture) string {
	t.Helper()

	root := t.TempDir()
	sdk, err := filepath.Abs("..")
	if err != nil {
		t.Fatalf("resolving this SDK: %v", err)
	}

	writeFile(t, filepath.Join(root, "go.mod"), theProjectModule+sdk+"\n")

	dir := filepath.Join(root, "migrations")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatalf("staging the migrations directory: %v", err)
	}

	for _, f := range fixtures {
		writeFile(t, filepath.Join(dir, f.name), f.body)
	}

	return dir
}

// writeFile puts one file where a test needs it, failing the test rather than the code under it.
func writeFile(t *testing.T, path, content string) {
	t.Helper()

	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// project stages a project over the fixtures given, driven by the binary this suite built.
func project(t *testing.T, fixtures ...fixture) *Running {
	t.Helper()

	return New(t, Project{Dir: stage(t, fixtures...), Binary: engine(t)})
}

func TestAStagedProjectAppliesAndRollsBackThroughTheRealBinary(t *testing.T) {
	p := project(t, addAccounts, addTier)

	applied := p.Up(t)

	if got := applied.Versions(); len(got) != 2 {
		t.Fatalf("migr up applied %v, want both migrations", got)
	}

	if applied.Runs[0].File != addAccounts.name {
		t.Errorf("the first run was %q, want %q", applied.Runs[0].File, addAccounts.name)
	}

	if applied.Batch == 0 {
		t.Error("the runs were recorded under no batch")
	}

	if counts := p.Status(t).Counts; counts.Applied != 2 || counts.Pending != 0 {
		t.Errorf("status reports %+v, want both applied", counts)
	}

	rolled := p.Down(t)

	if got := rolled.Versions(); len(got) != 2 {
		t.Errorf("migr down rolled back %v, want both", got)
	}

	if counts := p.Status(t).Counts; counts.Applied != 0 || counts.Pending != 2 {
		t.Errorf("status reports %+v, want both pending", counts)
	}

	t.Log("The second migration authors no down, so one of those rollbacks ran an inverse the")
	t.Log("engine derived -- which is the half no SDK computes and this helper does not either.")
}

func TestARoundTripSaysTheRollbackLeftNothingBehind(t *testing.T) {
	project(t, addAccounts, addTier).RoundTrip(t)

	t.Log("Residue is what this catches: a down that left the table behind makes the second up")
	t.Log("fail, and the server is what says so. It is not a snapshot comparison -- that needs")
	t.Log("Conn.Introspect, which is M11-T2.")
}

func TestTheConfigIsWrittenInsideTheProjectBecauseTheRuntimeWorksFromIt(t *testing.T) {
	dir := stage(t, addAccounts)
	root := filepath.Dir(dir)

	var config string

	t.Run("while the project is staged", func(t *testing.T) {
		p := New(t, Project{Dir: dir, Binary: engine(t)})
		config = p.Config()

		if parent := filepath.Dir(filepath.Dir(config)); parent != filepath.Join(root, cacheDir) {
			t.Errorf("the config is at %q, want it under %q", config, filepath.Join(root, cacheDir))
		}

		body := read(t, config)

		if !strings.Contains(body, fmt.Sprintf("dir: %q", dir)) {
			t.Errorf("the config names the migrations directory relatively:\n%s", body)
		}

		if ignored := read(t, filepath.Join(root, cacheDir, ".gitignore")); ignored != "*\n" {
			t.Errorf("%s/.gitignore holds %q, want the self-ignore", cacheDir, ignored)
		}
	})

	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Errorf("the config outlived the test that staged it: %v", err)
	}

	t.Log("docs/protocol.md §9 makes the directory holding migr.yaml the runtime's working")
	t.Log("directory, and both adapters probe from it: a config in a TempDir fails with \"this")
	t.Log("project is not inside a Go module\" before a migration is ever loaded.")
}

func TestTheDatabaseIsThrownAwayWithTheTestThatMadeIt(t *testing.T) {
	var dsn string

	t.Run("while the project is staged", func(t *testing.T) {
		p := project(t, addAccounts)
		dsn = p.DSN()

		p.Up(t)

		if _, err := os.Stat(dsn); err != nil {
			t.Fatalf("the database was not created at %q: %v", dsn, err)
		}
	})

	if _, err := os.Stat(dsn); !os.IsNotExist(err) {
		t.Errorf("the database outlived the test that made it: %v", err)
	}
}

func TestValidateReportsWhatTheChecksFoundAndFailsNothing(t *testing.T) {
	report := project(t, addAccounts, addTier).Validate(t)

	report.NoProblems(t)

	if report.Checked != 2 {
		t.Errorf("validate looked at %d migrations, want 2", report.Checked)
	}

	if report.Connected {
		t.Error("validate opened a database, and this helper passes no --connect")
	}

	if rules := report.Rules(); len(rules) != 1 || rules[0] != "missing-down" {
		t.Errorf("the warnings name %v, want the one rule the second fixture trips", rules)
	}
}

func TestValidateReportsAMigrationNothingCanUndo(t *testing.T) {
	report := project(t, addAccounts, dropsWithoutRecording).Validate(t)

	if len(report.Problems) == 0 {
		t.Fatal("a migration whose drop records nothing was reported as fine")
	}

	found := report.Problems[0]

	if found.Check != "reverse" {
		t.Errorf("the problem was found by %q, want the reverse check", found.Check)
	}

	if found.Version != "20260101T000200" {
		t.Errorf("the problem is about %q, want the migration that records nothing", found.Version)
	}

	t.Log("This is the half an SDK cannot answer: whether an operation has an inverse at all is")
	t.Log("internal/ir's, and the engine is what reports it.")
}

func TestAFailingCommandIsReportedWithTheDocumentItWrote(t *testing.T) {
	failure := project(t, addAccounts).Refused(t, "down", "--to", "20990101T000000")

	if failure.Code != 1 {
		t.Errorf("the failure carries code %d, want 1", failure.Code)
	}

	if !failure.Says("does not carry that version") {
		t.Errorf("the failure says %q", failure.Error)
	}
}

func TestEachProjectRecordsItsRunsInATableOfItsOwn(t *testing.T) {
	shared := filepath.Join(t.TempDir(), "shared.db")

	first := New(t, Project{Dir: stage(t, addAccounts), Binary: engine(t),
		Driver: "sqlite", DSN: shared})
	second := New(t, Project{Dir: stage(t, addAccounts), Binary: engine(t),
		Driver: "sqlite", DSN: shared})

	if got := first.Up(t).Versions(); len(got) != 1 {
		t.Fatalf("the first project applied %v, want one migration", got)
	}

	if got := second.Status(t).Counts.Applied; got != 0 {
		t.Fatalf("the second project sees %d applied migrations in its own history", got)
	}

	if read(t, first.Config()) == read(t, second.Config()) {
		t.Errorf("both projects are driven through the same config:\n%s",
			read(t, first.Config()))
	}

	t.Log("M7-T8's lesson: the database is the thing that remembers, so two projects sharing one")
	t.Log("history table make the second read the first one's runs as its own. What they do share")
	t.Log("here is the data, which is why the second project is not applied as well.")
}

func TestOutputHandsBackTheDocumentForACommandThisHelperDoesNotType(t *testing.T) {
	p := project(t, addAccounts)
	p.Up(t)

	var history struct {
		Runs []struct {
			Version string `json:"version"`
			Runtime string `json:"runtime"`
		} `json:"runs"`
	}

	if err := json.Unmarshal([]byte(p.Output(t, "history")), &history); err != nil {
		t.Fatalf("reading what migr history printed: %v", err)
	}

	if len(history.Runs) != 1 || history.Runs[0].Runtime != migr.Language {
		t.Errorf("history reports %+v, want the one run this SDK's language performed",
			history.Runs)
	}
}

func TestTheCommandIsBuiltWithTheConfigAndJsonAheadOfIt(t *testing.T) {
	p := New(t, Project{Dir: stage(t), Binary: fakeEngine(t, `printf '%s' "$*"`)})

	printed := p.Output(t, "status", "--json-is-already-there")
	args := strings.Fields(printed)

	if len(args) < 4 {
		t.Fatalf("the binary was given %q, want the config and the command", printed)
	}

	if args[0] != "--config" || args[1] != p.Config() {
		t.Errorf("the binary was given %q, want --config first", printed)
	}

	if args[2] != "--json" || args[3] != "status" {
		t.Errorf("the binary was given %q, want --json ahead of the command", printed)
	}
}

func TestACommandThatFailedFailsTheTestRatherThanBeingParsed(t *testing.T) {
	for _, engine := range []struct {
		name  string
		shell string
	}{
		{"it printed nothing but a failure", `echo '{"code":1,"error":"nothing doing"}' >&2; exit 1`},
		{"it printed a summary and then failed", `echo '{"database":"default","direction":"up",` +
			`"runs":[{"ordinal":1,"version":"20260101T000000","file":"a.go","statements":1}]}';` +
			`echo '{"code":1,"error":"nothing doing"}' >&2; exit 1`},
	} {
		t.Run(engine.name, func(t *testing.T) {
			p := New(t, Project{Dir: stage(t), Binary: fakeEngine(t, engine.shell)})

			for _, c := range []struct {
				name string
				run  func(reporter)
			}{
				{"up reads a document", func(r reporter) { p.ran(r, []string{"up"}) }},
				{"output hands one back", func(r reporter) { p.printed(r, []string{"status"}) }},
				{"validate tolerates only exit 2", func(r reporter) {
					p.validated(r, []string{"validate"})
				}},
			} {
				t.Run(c.name, func(t *testing.T) {
					stood := newReported(t)

					c.run(stood)

					if !stood.fatal {
						t.Fatalf("a command that exited 1 was read as a document:\n%s", stood.said())
					}

					if !strings.Contains(stood.said(), "nothing doing") {
						t.Errorf("the failure does not carry what was on stderr:\n%s", stood.said())
					}
				})
			}
		})
	}

	t.Log("A run that committed two migrations and then failed leaves one parseable")
	t.Log("document on each stream, so a printed summary is no evidence that the run succeeded.")
}

func TestABlankAnswerIsNeverAReport(t *testing.T) {
	p := New(t, Project{Dir: stage(t), Binary: fakeEngine(t,
		`echo '{"code":2,"error":"README.md is not a migration"}' >&2; exit 2`)})

	stood := newReported(t)

	p.validated(stood, []string{"validate"})

	if !stood.fatal {
		t.Fatalf("an empty stdout was read as a report:\n%s", stood.said())
	}

	for _, phrase := range []string{"printed no document", "not a migration"} {
		if !strings.Contains(stood.said(), phrase) {
			t.Errorf("the failure does not say %q:\n%s", phrase, stood.said())
		}
	}

	t.Log("migr validate exits 2 for two different things: the checks found problems, whose report")
	t.Log("is on stdout, and the command was given something it refuses -- a filename the grammar")
	t.Log("will not take -- whose message is on stderr. A printed document is what tells them apart.")
}

func TestAnExitCodeIsSurfacedWithWhatWasOnStderr(t *testing.T) {
	p := New(t, Project{Dir: stage(t),
		Binary: fakeEngine(t, `echo '{"code":4,"error":"the checksum moved"}' >&2; exit 4`)})

	failure := p.Refused(t, "up")

	if failure.Code != 4 {
		t.Errorf("the failure carries code %d, want the 4 the binary exited with", failure.Code)
	}

	if !failure.Says("checksum moved") {
		t.Errorf("the failure says %q", failure.Error)
	}
}

// fakeEngine writes a stand-in migr running one shell line, for a case about this helper and not migr.
func fakeEngine(t *testing.T, body string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "migr")

	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatalf("writing a stand-in migr: %v", err)
	}

	return path
}

// read returns a file's contents, failing the test rather than the code under it.
func read(t *testing.T, path string) string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	return string(body)
}

func TestTheRenderedConfigNamesEverythingTheEngineNeeds(t *testing.T) {
	rendered := Project{}.render("/srv/app/migrations", "/tmp/app.db", "migr_case")

	for _, want := range []string{
		`dir: "/srv/app/migrations"`, `language: "go"`, `driver: "sqlite"`,
		`dsn: "/tmp/app.db"`, `table: "migr_case"`, "timeout: 5s",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the config does not hold %s:\n%s", want, rendered)
		}
	}

	if strings.Contains(rendered, "validation:") {
		t.Error("a project permitting nothing wrote a validation block")
	}
}

func TestAProjectPermitsDestructionByNamingTheKinds(t *testing.T) {
	rendered := Project{Destructive: []string{"raw", "drop_table"}}.
		render("/srv/app/migrations", "/tmp/app.db", "migr_case")

	if !strings.Contains(rendered, `allow_destructive: ["raw", "drop_table"]`) {
		t.Errorf("the config does not permit what the project named:\n%s", rendered)
	}
}

func TestAConfigSuppliedWholeIsWhatIsWritten(t *testing.T) {
	supplied := "version: 1\n# whatever the fields cannot describe\n"

	if rendered := (Project{Config: supplied}).render("/a", "/b", "c"); rendered != supplied {
		t.Errorf("the config was rendered rather than taken:\n%s", rendered)
	}
}

func TestTheLanguageAndDriverDefaultToThisSdkAndTheEngineNeedingNoDaemon(t *testing.T) {
	if got := (Project{}).language(); got != migr.Language {
		t.Errorf("the language defaults to %q, want this SDK's own", got)
	}

	if got := (Project{Language: "typescript"}).language(); got != "typescript" {
		t.Errorf("a project naming its language got %q", got)
	}

	if got := (Project{}).driver(); got != "sqlite" {
		t.Errorf("the driver defaults to %q, want the one needing no daemon", got)
	}
}

func TestAHistoryTableNameIsOneEveryEngineAccepts(t *testing.T) {
	for _, c := range []struct {
		name  string
		given string
		want  string
	}{
		{"a plain name", "TestThing", "testthing"},
		{"a subtest", "TestThing/a_case_with_spaces", "testthing_a_case_with_spaces"},
		{"punctuation", "TestThing#01", "testthing_01"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := identifier(c.given); got != c.want {
				t.Errorf("%q renders as %q, want %q", c.given, got, c.want)
			}
		})
	}

	dir := stage(t)
	first := Project{}.table(t, inside(t, dir))
	second := Project{}.table(t, inside(t, dir))

	if first == second {
		t.Errorf("two projects in one test derived the same table %q, so the second would read "+
			"the first one's runs as its own", first)
	}

	for _, derived := range []string{first, second} {
		if len(derived) > tableLimit {
			t.Errorf("%q is %d characters, and PostgreSQL truncates an identifier at %d",
				derived, len(derived), tableLimit)
		}
	}
}

func TestALongTestNameIsTruncatedWithoutLosingWhatMakesItUnique(t *testing.T) {
	dir := stage(t)

	t.Run(strings.Repeat("a_name_long_enough_to_be_truncated", 3), func(t *testing.T) {
		home := inside(t, dir)
		unique := identifier(filepath.Base(home))

		derived := Project{}.table(t, home)

		if len(derived) > tableLimit {
			t.Errorf("%q is %d characters, and the limit is %d", derived, len(derived), tableLimit)
		}

		if !strings.HasSuffix(derived, unique) {
			t.Errorf("%q does not end in %q, so truncation dropped what made it unique",
				derived, unique)
		}
	})
}
