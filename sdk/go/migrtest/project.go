package migrtest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	migr "github.com/emrebdr/migr/sdk/go"
)

// A throwaway database, and one place the config cannot be put.
//
// The config has to live inside the project rather than in the test's own temporary directory, and
// that is measured rather than tidy: docs/protocol.md §9 makes the directory holding migr.yaml the
// runtime's working directory, and both adapters probe from it -- the Go one runs go list -m there,
// the Node one asks Node to resolve @migr/sdk from there. A config in /tmp therefore fails with
// "this project is not inside a Go module" before a migration is ever loaded.
//
// So it goes under <project>/.migr/, which is the directory the Go adapter already writes its
// generated runner into, and which ignores itself from the inside with a .gitignore holding "*".
// The database does not: it lives in the test's own t.TempDir() and is removed with it,
// along with the lock file the SQLite driver puts beside it.
//
// The history table is derived per project rather than per test, because a test may stage two of them
// and a shared table makes the second project read the first one's runs as its own -- measured here
// rather than reasoned about, since the first version of this derived the name from t.Name() alone.

// cacheDir is the directory migr already owns inside a project, which ignores itself from the inside.
const cacheDir = ".migr"

// lockTimeout keeps a wedged run failing in seconds rather than waiting out the 30s default.
const lockTimeout = "5s"

// tableLimit is the longest history table name this helper derives, PostgreSQL truncating at 63.
const tableLimit = 63

// exitValidation is what migr validate leaves when it found problems, which is a report and not a bug.
const exitValidation = 2

// Project is the throwaway a migration is run against: a database deleted with the test that made it.
type Project struct {
	// Dir is the migrations directory, which the config names absolutely whatever this says.
	Dir string
	// Language is what migrations.language holds, and is this SDK's own language by default.
	Language string
	// Driver is the engine to run against, and is sqlite over a file of this test's own by default.
	Driver string
	// DSN is how that engine is reached, and must be given whenever Driver is.
	DSN string
	// History is the table the runs are recorded in, and is one named for the test by default.
	History string
	// Destructive are the operation kinds this project permits, which migr up refuses otherwise.
	Destructive []string
	// Config is the whole migr.yaml to write instead, for a project the fields above cannot describe.
	Config string
	// Binary is the migr to run, and is what Found resolves by default.
	Binary string
}

// Running is a staged project with a database of its own, and the commands run against it.
type Running struct {
	// binary is the migr this project is driven by.
	binary string
	// config is the migr.yaml this helper wrote, inside the project for the reason above.
	config string
	// dsn is how the database is reached, which a test may inspect with its own driver.
	dsn string
}

// New stages a project over a database of its own, and returns what commands are run against it.
func New(t *testing.T, p Project) *Running {
	t.Helper()

	dir := absolute(t, p.Dir)
	home := inside(t, dir)
	dsn := p.target(t)

	return &Running{
		binary: p.resolve(t),
		config: p.write(t, home, dir, dsn, p.table(t, home)),
		dsn:    dsn,
	}
}

// DSN reports how the throwaway database is reached, for a test that inspects it with its own driver.
func (r *Running) DSN() string {
	return r.dsn
}

// Config reports the migr.yaml this project is driven through, for a test that reads or extends it.
func (r *Running) Config() string {
	return r.config
}

// Up applies this project's migrations, failing the test with what migr reported if it could not.
func (r *Running) Up(t *testing.T, args ...string) Run {
	t.Helper()

	return r.ran(t, append([]string{"up"}, args...))
}

// Down rolls them back, failing the test with what migr reported if it could not.
func (r *Running) Down(t *testing.T, args ...string) Run {
	t.Helper()

	return r.ran(t, append([]string{"down"}, args...))
}

// ran runs one direction and reads what it did, which is one document either way.
func (r *Running) ran(report reporter, args []string) Run {
	report.Helper()

	var summary Run

	r.into(report, args, &summary)

	return summary
}

// Validate reports what the checks found, since problems are this command's product and not a failure.
func (r *Running) Validate(t *testing.T, args ...string) Report {
	t.Helper()

	return r.validated(t, append([]string{"validate"}, args...))
}

// validated runs the checks, tolerating the one exit code that means problems were found.
func (r *Running) validated(report reporter, args []string) Report {
	report.Helper()

	out, stderr, code := r.output(report, args)

	var found Report

	decoded(report, args, document(report, args, out, stderr, code, exitValidation), &found)

	return found
}

// Status reports what this database holds, which takes no migration lock and promises no action.
func (r *Running) Status(t *testing.T, args ...string) Status {
	t.Helper()

	var reported Status

	r.into(t, append([]string{"status"}, args...), &reported)

	return reported
}

// Output runs any command and returns the document it printed, for one this helper does not type.
func (r *Running) Output(t *testing.T, args ...string) string {
	t.Helper()

	return r.printed(t, args)
}

// printed runs one command and returns its document, failing with both streams if it failed.
func (r *Running) printed(report reporter, args []string) string {
	report.Helper()

	out, stderr, code := r.output(report, args)

	return document(report, args, out, stderr, code, 0)
}

// A blank answer is never a report, and migr validate is why that is a check rather than an
// assumption: exit 2 is both "the checks found problems", whose report is on stdout, and "this
// command was given something it refuses" -- a filename the grammar will not take, say -- whose
// message is on stderr. Which of the two happened is decided by whether a document was printed.

// document reports the one document a command printed, failing where migr printed none.
func document(report reporter, args []string, out, stderr string, code, tolerated int) string {
	report.Helper()

	if code != 0 && code != tolerated {
		report.Fatalf("migr %s exited %d: %s",
			strings.Join(args, " "), code, strings.TrimSpace(stderr))

		return ""
	}

	if strings.TrimSpace(out) == "" {
		report.Fatalf("migr %s exited %d and printed no document: %s",
			strings.Join(args, " "), code, strings.TrimSpace(stderr))

		return ""
	}

	return out
}

// Refused runs a command expecting it to fail, and reports the failure migr wrote on stderr.
func (r *Running) Refused(t *testing.T, args ...string) Failure {
	t.Helper()

	return r.refusal(t, args)
}

// refusal reads the document a failing command wrote, which --json puts on stderr as one line.
func (r *Running) refusal(report reporter, args []string) Failure {
	report.Helper()

	_, stderr, code := r.output(report, args)
	if code == 0 {
		report.Fatalf("migr %s succeeded, and this case is about how it fails",
			strings.Join(args, " "))
	}

	var failure Failure

	if err := json.Unmarshal([]byte(lastLine(stderr)), &failure); err != nil {
		report.Fatalf("migr %s exited %d and wrote no failure document: %s",
			strings.Join(args, " "), code, strings.TrimSpace(stderr))
	}

	return failure
}

// RoundTrip applies and rolls back twice, which is what says a rollback left nothing behind.
func (r *Running) RoundTrip(t *testing.T) {
	t.Helper()

	first := r.Up(t)
	r.Down(t)
	again := r.Up(t)
	r.Down(t)

	if len(again.Runs) != len(first.Runs) {
		t.Errorf("the first up applied %d migrations and the second applied %d, so the rollback "+
			"between them did not leave the database where the first up found it",
			len(first.Runs), len(again.Runs))
	}

	if applied := r.Status(t).Counts.Applied; applied != 0 {
		t.Errorf("%d migrations are still applied after the second rollback", applied)
	}
}

// into runs one command, fails the test if migr did, and reads the document it printed.
func (r *Running) into(report reporter, args []string, target any) {
	report.Helper()

	decoded(report, args, r.printed(report, args), target)
}

// output runs one command against this project, and reports both streams and the status it left.
func (r *Running) output(report reporter, args []string) (out, stderr string, code int) {
	report.Helper()

	full := append([]string{"--config", r.config, "--json"}, args...)

	//nolint:gosec // G204: the binary is what Found resolved or the test named, and the arguments are the test's own
	cmd := exec.CommandContext(report.Context(), r.binary, full...)

	var printed, reported bytes.Buffer

	cmd.Stdout = &printed
	cmd.Stderr = &reported

	err := cmd.Run()

	var exited *exec.ExitError
	if err != nil && !errors.As(err, &exited) {
		report.Fatalf("running %s %s: %v", r.binary, strings.Join(full, " "), err)

		return printed.String(), reported.String(), -1
	}

	return printed.String(), reported.String(), cmd.ProcessState.ExitCode()
}

// decoded reads one command's document, failing with what it printed rather than with a type error.
func decoded(report reporter, args []string, out string, target any) {
	report.Helper()

	if err := json.Unmarshal([]byte(out), target); err != nil {
		report.Fatalf("reading what migr %s printed: %v\n%s", strings.Join(args, " "), err, out)
	}
}

// lastLine is where a failure document is, since a run that printed a summary also wrote one.
func lastLine(stderr string) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")

	return lines[len(lines)-1]
}

// resolve reports which migr this project is driven by, failing the test when there is none.
func (p Project) resolve(t *testing.T) string {
	t.Helper()

	if p.Binary != "" {
		return p.Binary
	}

	binary, err := Found()
	if err != nil {
		t.Fatalf("this helper runs migr itself, since no SDK can compile an operation into SQL: %v",
			err)
	}

	return binary
}

// target reports how the database is reached, which is a file of this test's own by default.
func (p Project) target(t *testing.T) string {
	t.Helper()

	if (p.DSN == "") != (p.Driver == "") {
		t.Fatal("a project naming its own Driver names its own DSN too, and the other way round")
	}

	if p.DSN != "" {
		return p.DSN
	}

	return filepath.Join(t.TempDir(), "throwaway.db")
}

// write stages the config this project is driven through, in the directory inside made for it.
func (p Project) write(t *testing.T, home, dir, dsn, table string) string {
	t.Helper()

	path := filepath.Join(home, "migr.yaml")

	if err := os.WriteFile(path, []byte(p.render(dir, dsn, table)), 0o600); err != nil {
		t.Fatalf("staging migr.yaml: %v", err)
	}

	return path
}

// render builds the whole migr.yaml, or returns the one a project supplied instead of the fields.
func (p Project) render(dir, dsn, table string) string {
	if p.Config != "" {
		return p.Config
	}

	return fmt.Sprintf(`version: 1
migrations:
  dir: %q
  language: %q
databases:
  default:
    driver: %q
    dsn: %q
history:
  table: %q
lock:
  timeout: %s
`, dir, p.language(), p.driver(), dsn, table, lockTimeout) + p.permitted()
}

// permitted renders which kinds this project lets a migration destroy, which migr up reads.
func (p Project) permitted() string {
	if len(p.Destructive) == 0 {
		return ""
	}

	kinds := make([]string, 0, len(p.Destructive))
	for _, kind := range p.Destructive {
		kinds = append(kinds, fmt.Sprintf("%q", kind))
	}

	return fmt.Sprintf("validation:\n  allow_destructive: [%s]\n", strings.Join(kinds, ", "))
}

// language reports what migrations.language says, which is this SDK's own unless a project says otherwise.
func (p Project) language() string {
	if p.Language != "" {
		return p.Language
	}

	return migr.Language
}

// driver reports which engine to run against, which is the one needing no daemon by default.
func (p Project) driver() string {
	if p.Driver != "" {
		return p.Driver
	}

	return "sqlite"
}

// table reports which history table the runs go in, which is one per project for M7-T8's reason.
func (p Project) table(t *testing.T, home string) string {
	t.Helper()

	if p.History != "" {
		return p.History
	}

	unique := identifier(filepath.Base(home))
	named := "migr_" + identifier(t.Name())

	if room := tableLimit - len(unique) - 1; len(named) > room {
		named = named[:room]
	}

	return named + "_" + unique
}

// identifier renders a test's name as something every engine accepts as a table name.
func identifier(name string) string {
	var out strings.Builder

	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)

			continue
		}

		out.WriteRune('_')
	}

	return out.String()
}

// absolute resolves the migrations directory, which the config names absolutely for §9's reason.
func absolute(t *testing.T, dir string) string {
	t.Helper()

	if dir == "" {
		t.Fatal("a project names the directory its migrations are in")
	}

	resolved, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving the migrations directory %q: %v", dir, err)
	}

	if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
		t.Fatalf("%q is not a directory this test can stage migrations from", resolved)
	}

	return resolved
}

// inside makes the directory the throwaway config lives in, which has to be within the project.
func inside(t *testing.T, dir string) string {
	t.Helper()

	base := filepath.Join(filepath.Dir(dir), cacheDir)

	if err := os.MkdirAll(base, 0o750); err != nil {
		t.Fatalf("making %s inside the project: %v", cacheDir, err)
	}

	ignore(t, base)

	home, err := os.MkdirTemp(base, "test-")
	if err != nil {
		t.Fatalf("staging a directory for this test's config: %v", err)
	}

	t.Cleanup(func() { _ = os.RemoveAll(home) })

	return home
}

// ignore writes the self-ignore migr's own cache directory carries, where there is not one already.
func ignore(t *testing.T, base string) {
	t.Helper()

	path := filepath.Join(base, ".gitignore")

	if _, err := os.Stat(path); err == nil {
		return
	}

	if err := os.WriteFile(path, []byte("*\n"), 0o600); err != nil {
		t.Fatalf("writing the self-ignore for %s: %v", base, err)
	}
}
