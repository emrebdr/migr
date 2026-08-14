package validate

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/migration"
	"github.com/emrebdr/migr/internal/plan"
	"github.com/emrebdr/migr/internal/schema"
)

// driverName is what the fake compiler here is called, and what every raw operation is addressed to.
const driverName = "fake"

// versionAt returns the version the nth staged migration carries, one second apart.
func versionAt(n int) string {
	return fmt.Sprintf("20260727T1530%02d", n)
}

// migrationAt returns the nth staged migration, named for what a test is doing with it.
func migrationAt(n int, name string) migration.Migration {
	return migration.Migration{Version: versionAt(n), Name: name, Ext: ".sql"}
}

// staged is what one migration emits in each direction, or refuses to say.
type staged struct {
	// up are the operations it performs.
	up ir.Operations
	// down are the operations it undoes itself with, nil when it authors none.
	down ir.Operations
	// declared is the reason it gives for having no undo at all, empty when it gives none.
	declared string
	// unreadable is why its file cannot be read as a migration, empty when it can.
	unreadable string
	// downUnreadable is why its down section cannot be read, which is neither absent nor declared.
	downUnreadable string
}

// source is a set of migrations with no files, answering exactly what a test staged for each.
type source map[string]staged

// compile-time proof that this fake satisfies the contract the executor asks its migrations through.
var _ engine.Source = source{}

// Emit answers what one migration does, the way a real source reports each of the same cases.
func (s source) Emit(_ context.Context, m migration.Migration, dir ir.Direction, _ engine.Mode,
) (ir.Operations, error) {
	held, ok := s[m.Version]
	if !ok {
		return nil, fmt.Errorf("%s: no migration was staged for it", m.Filename())
	}

	if held.unreadable != "" {
		return nil, fmt.Errorf("%s: %s", m.Filename(), held.unreadable)
	}

	if dir == ir.Up {
		return held.up, nil
	}

	return held.undo()
}

// asked wraps a source and remembers the mode each half was read in.
type asked struct {
	source
	// modes are the modes it was asked in, in the order it was asked.
	modes []engine.Mode
}

// Emit records the mode and answers what the wrapped source staged.
func (a *asked) Emit(ctx context.Context, m migration.Migration, dir ir.Direction, mode engine.Mode,
) (ir.Operations, error) {
	a.modes = append(a.modes, mode)

	return a.source.Emit(ctx, m, dir, mode)
}

// undo answers the down question the four ways a source may answer it.
func (s staged) undo() (ir.Operations, error) {
	if s.downUnreadable != "" {
		return nil, errors.New(s.downUnreadable)
	}

	if s.declared != "" {
		return nil, engine.DeclaredIrreversible(s.declared)
	}

	if len(s.down) == 0 {
		return nil, engine.ErrNoDownAuthored
	}

	return s.down, nil
}

// refusalReason is why the fake compiler refuses what a test told it to refuse.
const refusalReason = "this fake engine has no such thing"

// compiler compiles every operation to one statement, refusing what a test told it to refuse.
type compiler struct {
	// refuse is the SQL whose raw operation this compiler will not compile.
	refuse string
	// refuseKind is the operation kind it will not compile, whatever the operation carries.
	refuseKind ir.Kind
}

// compile-time proof that this fake satisfies the contract a planner compiles through.
var _ driver.Compiler = compiler{}

// Compile turns an operation into one statement, refusing whichever a test asked it to refuse.
func (c compiler) Compile(op ir.Operation) ([]driver.Statement, error) {
	if c.refuseKind != "" && op.Kind() == c.refuseKind {
		return nil, driver.Unsupported(driverName, op.Kind(), refusalReason)
	}

	raw, ok := op.(ir.Raw)
	if !ok {
		return []driver.Statement{{SQL: "-- " + op.Kind().String()}}, nil
	}

	if c.refuse != "" && raw.SQL == c.refuse {
		return nil, driver.Unsupported(driverName, op.Kind(), refusalReason)
	}

	return []driver.Statement{{SQL: raw.SQL}}, nil
}

// raw builds the operation a .sql statement reaches the engine as.
func raw(sql string) ir.Raw {
	return ir.Raw{Driver: driverName, SQL: sql}
}

// table builds a valid create_table, which is the reversible operation these tests derive downs from.
func table(t *testing.T, name string) ir.CreateTable {
	t.Helper()

	int64Type, err := schema.Parse(string(schema.KindInt64))
	if err != nil {
		t.Fatalf("parsing a column type: %v", err)
	}

	return ir.CreateTable{Table: name, Columns: []ir.Column{{Name: "id", Type: int64Type}}}
}

// run validates a staged project, failing the test rather than the code under test on a setup error.
func run(t *testing.T, src source, c compiler, found ...migration.Migration) Report {
	t.Helper()

	report, err := Run(t.Context(), Options{
		Migrations: found,
		Source:     src,
		Planner:    plan.Planner{Compiler: c},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	return report
}

// checksFound returns the check each problem came from, in the order the run found them.
func checksFound(report Report) []Check {
	found := make([]Check, 0, len(report.Problems()))
	for _, problem := range report.Problems() {
		found = append(found, problem.Check)
	}

	return found
}

// filesFound returns the migration each problem belongs to, in the order the run found them.
func filesFound(report Report) []string {
	found := make([]string, 0, len(report.Problems()))
	for _, problem := range report.Problems() {
		found = append(found, problem.Migration.Filename())
	}

	return found
}
