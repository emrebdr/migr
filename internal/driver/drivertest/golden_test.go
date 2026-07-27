package drivertest

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// fakeCompiler turns operations into marker comments, so no golden file can hold engine SQL.
type fakeCompiler struct {
	suffix        string
	noTransaction bool
	refusals      map[ir.Kind]error
	drifts        bool
	calls         int
}

// Compile returns one marker comment per operation, or whatever refusal this fake was given.
func (c *fakeCompiler) Compile(op ir.Operation) ([]driver.Statement, error) {
	if err, refused := c.refusals[op.Kind()]; refused {
		return nil, err
	}

	c.calls++

	return []driver.Statement{{SQL: c.text(op), NoTransaction: c.noTransaction}}, nil
}

// text renders the marker, varying it per call when this fake is standing in for a bug.
func (c *fakeCompiler) text(op ir.Operation) string {
	if c.drifts {
		return "-- " + op.Kind().String() + strconv.Itoa(c.calls)
	}

	return "-- " + op.Kind().String() + c.suffix
}

// compile-time proof that the fake satisfies the contract the harness compiles through.
var _ driver.Compiler = (*fakeCompiler)(nil)

// seeded returns a suite whose temporary golden root already records every operation.
func seeded(t *testing.T, c driver.Compiler) Suite {
	t.Helper()

	s := Suite{Driver: "fake", Compiler: c, Root: t.TempDir()}
	if problems := s.check(nil, true); problems != nil {
		t.Fatalf("seeding the golden root: %v", problems)
	}

	return s
}

// goldenPath returns where one operation's golden file lives inside a suite's root.
func goldenPath(s Suite, kind ir.Kind) string {
	return filepath.Join(s.Root, s.Driver, kind.String()+extension)
}

// joined renders a suite's problems as one string, so a test can look for what it expects.
func joined(problems []string) string {
	return strings.Join(problems, "\n")
}

func TestAMissingGoldenFileFails(t *testing.T) {
	s := seeded(t, &fakeCompiler{})

	if err := os.Remove(goldenPath(s, ir.KindDropTable)); err != nil {
		t.Fatalf("removing a golden file: %v", err)
	}

	problems := joined(s.check(nil, false))

	for _, want := range []string{"fake/drop_table.sql", "no golden file", "make golden-update"} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q do not mention %q", problems, want)
			t.Log("A golden harness that treats an absent file as \"nothing to compare\" reports")
			t.Log("green for an operation nothing has ever checked, which is worse than having")
			t.Log("no golden test at all.")
		}
	}
}

func TestAnOperationTheSuiteNeverCompilesFails(t *testing.T) {
	s := Suite{Driver: "fake", Compiler: &fakeCompiler{}, Root: t.TempDir()}
	partial := Operations(s.Driver)[:3]

	problems := joined(s.check(partial, false))

	for _, kind := range []ir.Kind{ir.KindAddColumn, ir.KindRaw, ir.KindCreateView} {
		if !strings.Contains(problems, kind.String()+": the suite compiles no such operation") {
			t.Errorf("problems %q do not report %s as uncompiled", problems, kind)
			t.Log("\"A missing golden file fails\" means nothing if the driver decides which")
			t.Log("operations count. Walking ir.Kinds() is what makes an omission visible;")
			t.Log("otherwise a driver that stops compiling an op has nothing missing.")
			t.Log("The assertion names the coverage message rather than looking for the operation")
			t.Log("anywhere in the output: against a seeded root the orphan check mentions every")
			t.Log("one of them too, so the looser check passed for the wrong reason.")
		}
	}
}

func TestAnOperationCompiledTwiceSharesOneGoldenFile(t *testing.T) {
	s := seeded(t, &fakeCompiler{})
	doubled := append(Operations(s.Driver), ir.DropTable{Table: "other"})

	if !strings.Contains(joined(s.check(doubled, false)), "share one golden file") {
		t.Error("two operations of one kind were accepted, and the second silently wins")
	}
}

func TestANormalRunNeverWritesAGoldenFile(t *testing.T) {
	s := Suite{Driver: "fake", Compiler: &fakeCompiler{}, Root: t.TempDir()}

	if problems := s.check(nil, false); len(problems) != len(ir.Kinds()) {
		t.Errorf("an empty golden root reported %d problems, want one per operation", len(problems))
	}

	if _, err := os.Stat(filepath.Join(s.Root, s.Driver)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a comparison run created the driver's golden directory")
		t.Log("A test run that writes what it is meant to be checking always passes. The")
		t.Log("write path must be reachable only through -update.")
	}
}

func TestUpdateRecordsWhatTheDriverCompiled(t *testing.T) {
	s := seeded(t, &fakeCompiler{})

	recorded, err := os.ReadFile(goldenPath(s, ir.KindDropTable))
	if err != nil {
		t.Fatalf("reading the recorded golden: %v", err)
	}

	if string(recorded) != "-- migr:statement 1\n-- drop_table\n" {
		t.Errorf("recorded %q, want the compiled statement and a trailing newline", recorded)
	}

	if problems := s.check(nil, false); problems != nil {
		t.Errorf("a freshly recorded suite does not compare clean: %v", problems)
	}
}

func TestAChangedStatementFailsAndNamesTheLine(t *testing.T) {
	s := seeded(t, &fakeCompiler{})
	s.Compiler = &fakeCompiler{suffix: " -- changed"}

	problems := joined(s.check(nil, false))

	for _, want := range []string{"out of date at line 2", `"-- drop_table"`, `"-- drop_table -- changed"`} {
		if !strings.Contains(problems, want) {
			t.Errorf("problems %q do not show %s", problems, want)
		}
	}
}

func TestAStatementThatCannotRunInATransactionSaysSo(t *testing.T) {
	s := seeded(t, &fakeCompiler{})
	s.Compiler = &fakeCompiler{noTransaction: true}

	if s.check(nil, false) == nil {
		t.Error("a driver moved a statement out of its transaction with no golden change")
		t.Log("Statement.String() returns only the SQL, so rendering through it makes")
		t.Log("NoTransaction invisible to every golden file. A driver could start running DDL")
		t.Log("outside a transaction -- M13-T4's whole subject -- and the suite stays green.")
	}

	s = seeded(t, &fakeCompiler{noTransaction: true})

	recorded, err := os.ReadFile(goldenPath(s, ir.KindDropTable))
	if err != nil {
		t.Fatalf("reading the recorded golden: %v", err)
	}

	if string(recorded) != statementHeader+"1"+noTransaction+"\n-- drop_table\n" {
		t.Errorf("recorded %q, want the marker above the statement", recorded)
	}
}

func TestARefusalIsRecordedRatherThanSkipped(t *testing.T) {
	refusal := driver.Unsupported("fake", ir.KindDropColumn, "requires a table rebuild before 3.35")
	s := seeded(t, &fakeCompiler{refusals: map[ir.Kind]error{ir.KindDropColumn: refusal}})

	recorded, err := os.ReadFile(goldenPath(s, ir.KindDropColumn))
	if err != nil {
		t.Fatalf("a refused operation recorded no golden file: %v", err)
	}

	want := unsupported + "drop_column: requires a table rebuild before 3.35\n"
	if string(recorded) != want {
		t.Errorf("recorded %q, want %q", recorded, want)
		t.Log("Recording the refusal is what lets make golden-update run over the full op set")
		t.Log("with no per-driver skip list, and what makes an operation that starts or stops")
		t.Log("being refused show up as a diff instead of an edited test.")
	}

	if problems := s.check(nil, false); problems != nil {
		t.Errorf("a recorded refusal does not compare clean: %v", problems)
	}
}

func TestACompilerBugIsNotARefusal(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "ordinary failure", err: errors.New("syntax error near CREATE"), want: "compiling failed"},
		{name: "refusal with no reason", err: driver.ErrUnsupported, want: "without a reason"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Suite{
				Driver:   "fake",
				Compiler: &fakeCompiler{refusals: map[ir.Kind]error{ir.KindDropTable: tt.err}},
				Root:     t.TempDir(),
			}

			problems := joined(s.check(nil, true))
			if !strings.Contains(problems, tt.want) {
				t.Errorf("problems %q do not report %q", problems, tt.want)
			}

			if _, err := os.Stat(goldenPath(s, ir.KindDropTable)); !errors.Is(err, fs.ErrNotExist) {
				t.Error("a compiler bug was recorded as though it were a declared refusal")
				t.Log("The conformance suite asserts every op is either compilable or refused")
				t.Log("with a reason. Recording a crash as a verdict would make that true by")
				t.Log("construction and prove nothing.")
			}
		})
	}
}

func TestAGoldenFileNoOperationCompilesToFails(t *testing.T) {
	s := seeded(t, &fakeCompiler{})
	orphan := filepath.Join(s.Root, s.Driver, "renamed_op"+extension)

	if err := os.WriteFile(orphan, []byte("-- stale\n"), 0o600); err != nil {
		t.Fatalf("writing an orphan: %v", err)
	}

	if !strings.Contains(joined(s.check(nil, false)), "renamed_op.sql") {
		t.Error("a golden file no operation claims went unreported")
		t.Log("Renaming or removing an operation leaves its expectations behind, where they")
		t.Log("read as coverage that no longer exists. -update rewrites what is claimed and")
		t.Log("never deletes, so nothing else would ever notice.")
	}
}

func TestCompilingTwiceMustAgree(t *testing.T) {
	s := Suite{Driver: "fake", Compiler: &fakeCompiler{drifts: true}, Root: t.TempDir()}

	problems := joined(s.check(nil, true))

	if !strings.Contains(problems, "invariant #8") {
		t.Errorf("problems %q do not report irreproducible output", problems)
		t.Log("A Compile that ranges a map emits different SQL between runs. Without this the")
		t.Log("first run records one ordering as the golden and every later run fails for a")
		t.Log("reason the diff does not explain.")
	}

	if _, err := os.Stat(goldenPath(s, ir.KindDropTable)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("irreproducible output was recorded as a golden file anyway")
	}
}

func TestAMalformedFixtureIsATestBugNotADriverBug(t *testing.T) {
	s := seeded(t, &fakeCompiler{})

	ops := Operations(s.Driver)
	ops[1] = ir.DropTable{}

	if !strings.Contains(joined(s.check(ops, false)), "not a well-formed operation") {
		t.Error("an invalid fixture was reported as a driver problem")
	}
}

func TestASuiteWithNothingToCompileSaysSo(t *testing.T) {
	tests := []struct {
		name  string
		suite Suite
		want  string
	}{
		{name: "no driver", suite: Suite{Compiler: &fakeCompiler{}}, want: "names no driver"},
		{name: "no compiler", suite: Suite{Driver: "fake"}, want: "no compiler"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.suite.Root = t.TempDir()

			if !strings.Contains(joined(tt.suite.check(nil, true)), tt.want) {
				t.Errorf("an unusable suite was accepted: %v", tt.suite.check(nil, true))
			}
		})
	}
}
