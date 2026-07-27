package drivertest

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// namedCompiler answers to a driver name, as every real driver.Driver does.
type namedCompiler struct {
	fakeCompiler

	name string
}

// Name returns the driver name this compiler claims to be.
func (c *namedCompiler) Name() string { return c.name }

func TestStatementBoundariesSurviveRendering(t *testing.T) {
	separate := renderStatements([]driver.Statement{{SQL: "-- one"}, {SQL: "-- two"}})
	fused := renderStatements([]driver.Statement{{SQL: "-- one\n\n-- two"}})

	if separate == fused {
		t.Errorf("two statements and one containing a blank line both render as %q", separate)
		t.Log("Joining on blank lines makes those two indistinguishable, and they are not the")
		t.Log("same thing: M3-T5 groups statements around the transaction boundary, so fusing")
		t.Log("two into one changes what actually runs. create_view and raw both carry")
		t.Log("multi-line SQL, so this is reached by ordinary fixtures rather than a corner.")
	}

	if strings.Count(separate, statementHeader) != 2 {
		t.Errorf("%q does not number both statements", separate)
	}
}

func TestTheGoldenFormatCoversEveryStatementField(t *testing.T) {
	const covered = 2

	if fields := reflect.TypeOf(driver.Statement{}).NumField(); fields != covered {
		t.Errorf("driver.Statement has %d fields but the renderer covers %d", fields, covered)
		t.Log("A field added to Statement is invisible to every golden file until it is")
		t.Log("rendered, so a driver could change behavior with no diff anywhere. Adding one")
		t.Log("means deciding how it is recorded, then raising this count.")
	}
}

func TestAnOperationCompilingToNothingIsAProblem(t *testing.T) {
	s := Suite{Driver: "fake", Compiler: &silentCompiler{}, Root: t.TempDir()}

	if !strings.Contains(joined(s.check(nil, true)), "no statements at all") {
		t.Error("an operation that compiled to nothing was recorded as an empty golden file")
		t.Log("An empty file and \"this driver does nothing here\" look identical afterwards.")
		t.Log("A driver with nothing to emit should refuse and say why.")
	}
}

func TestARefusalMustNameItselfCorrectly(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "blank reason",
			err:  driver.Unsupported("fake", ir.KindDropTable, "   "),
			want: "without a reason",
		},
		{
			name: "another operation",
			err:  driver.Unsupported("fake", ir.KindAddIndex, "a reason"),
			want: "refused add_index while being asked about drop_table",
		},
		{
			name: "another driver",
			err:  driver.Unsupported("mysql", ir.KindDropTable, "a reason"),
			want: `names driver "mysql"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := Suite{
				Driver:   "fake",
				Compiler: &fakeCompiler{refusals: map[ir.Kind]error{ir.KindDropTable: tt.err}},
				Root:     t.TempDir(),
			}

			if !strings.Contains(joined(s.check(nil, true)), tt.want) {
				t.Errorf("problems %v do not report %q", s.check(nil, true), tt.want)
				t.Log("driver.Unsupported validates none of this, so a refusal can name the wrong")
				t.Log("operation, another engine, or nothing at all and still be recorded as this")
				t.Log("driver's honest answer.")
			}
		})
	}
}

func TestASuiteWiredToAnotherDriversCompilerSaysSo(t *testing.T) {
	s := Suite{
		Driver:   "postgres",
		Compiler: &namedCompiler{name: "mysql"},
		Root:     t.TempDir(),
	}

	if !strings.Contains(joined(s.check(nil, true)), "reports its name as") {
		t.Error("a suite recorded one driver's output under another driver's directory")
		t.Log("Every driver's golden test is a copy of the one before it, so handing the wrong")
		t.Log("compiler to a copied suite is the likeliest mistake there is -- and it would")
		t.Log("write mysql's SQL into test/golden/postgres/ with nothing to notice.")
	}
}

func TestComparisonIsByteExact(t *testing.T) {
	s := seeded(t, &fakeCompiler{})
	path := goldenPath(s, ir.KindDropTable)

	recorded, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the recorded golden: %v", err)
	}

	if err := os.WriteFile(path, append(recorded, '\n'), 0o600); err != nil {
		t.Fatalf("appending a blank line: %v", err)
	}

	if s.check(nil, false) == nil {
		t.Error("a golden file with a trailing blank line compared clean")
		t.Log("Trimming before comparing lets an editor's save silently rewrite a golden, and")
		t.Log("the next real change then lands on a file nobody has actually reviewed.")
	}
}

// silentCompiler compiles every operation to nothing at all, without reporting a refusal.
type silentCompiler struct{}

// Compile returns no statements and no error, which is the one answer a driver may not give.
func (c *silentCompiler) Compile(ir.Operation) ([]driver.Statement, error) {
	return nil, nil
}

// compile-time proof that both extra fakes satisfy the compiler contract.
var (
	_ driver.Compiler = (*silentCompiler)(nil)
	_ driver.Compiler = (*namedCompiler)(nil)
)
