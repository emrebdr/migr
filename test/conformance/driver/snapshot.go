package driver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
	"github.com/emrebdr/migr/internal/schema"
)

// normalized is the native spelling every column type is rewritten to before two engines compare.
const normalized = "normalized"

// Every driver must read the same fixture back as the same snapshot, which is M11-T2's done-when:
// equivalent schemas compare equal across engines despite differing native type names. It is
// stronger than comparing engines pairwise -- there is one expected value and every engine is held
// to it -- and it needs no second connection, so SQLite runs it untagged where PostgreSQL and MySQL
// run it under the integration tag.

// Snapshot holds one driver's introspection to the schema its own operations describe.
type Snapshot struct {
	// Driver is the driver under test, which is what compiles the fixture.
	Driver driver.Driver
	// Conn is an open session against an empty namespace, which is what reads the snapshot back.
	Conn driver.Conn
	// Prefix makes the fixture's names unique on a server several runs share, and is required.
	Prefix string
}

// Run applies the fixture, reads it back, and reports every way this driver's answer differs.
func (s Snapshot) Run(t *testing.T) {
	t.Helper()

	for _, problem := range s.checkSuite() {
		t.Error(problem)
	}

	if t.Failed() {
		return
	}

	s.clear(t.Context())

	t.Cleanup(func() { s.clear(context.WithoutCancel(t.Context())) })

	if err := s.apply(t.Context(), t); err != nil {
		t.Fatalf("applying the fixture: %v", err)
	}

	read, err := s.Conn.Introspect(t.Context())
	if err != nil {
		t.Fatalf("Introspect: %v", err)
	}

	for _, problem := range s.differences(t, read) {
		t.Error(problem)
	}

	s.checkCoverage(t)
}

// checkSuite reports a suite that cannot ask a driver anything at all.
func (s Snapshot) checkSuite() []string {
	var problems []string

	if s.Driver == nil {
		problems = append(problems, "the suite has no driver, so there is nothing to compile with")
	}

	if s.Conn == nil {
		problems = append(problems, "the suite has no session, so there is nothing to read back")
	}

	if strings.TrimSpace(s.Prefix) == "" {
		problems = append(problems, "the suite has no prefix, and the fixture's names must be "+
			"unique on a server that several runs share")
	}

	return problems
}

// Column types are compared on the logical type alone, which is what normalization means: the
// native spellings differ on every engine and must not make two equivalent schemas unequal. The
// certainty each engine earns is its own test's to assert.

// differences reports every way one driver's snapshot differs from the schema the fixture describes.
func (s Snapshot) differences(t *testing.T, read *schema.Snapshot) []string {
	t.Helper()

	if err := read.Validate(); err != nil {
		return []string{fmt.Sprintf("this driver read a snapshot that is not usable: %v", err)}
	}

	mine := s.own(read)

	var problems []string

	for _, finding := range schema.Unrepresentable(mine) {
		problems = append(problems, fmt.Sprintf("the fixture is built from the operations alone "+
			"and this driver marked %s", finding.Describe()))
	}

	want, got := s.expected(t), normalize(&mine)

	if want.Equal(*got) {
		return problems
	}

	return append(problems, fmt.Sprintf("this driver reads the fixture back as a different "+
		"schema\n\nwant:\n%s\n\ngot:\n%s", render(t, want), render(t, *got)))
}

// The snapshot model is deliberate that a guess never equals a fact, which is why this is the suite's own
// transform and not the model's: SQLite declares all three integer widths as integer and MySQL both
// timestamps as datetime(6), so those columns read back ambiguous where PostgreSQL's read back
// exact. A column no logical type names at all is left alone and fails the comparison, which is
// what it should do -- every type in this fixture is one the driver itself emitted.

// Scoped rather than whole, because one of the three engines gives every test in a package the same
// database: a table some other case left behind would otherwise fail this suite for a reason that
// says nothing about introspection. What the prefix buys is that the fixture owns its own names, so
// an element this suite did not create and does not expect still fails it.

// own returns the part of a snapshot this fixture built, which is every name under its prefix.
func (s Snapshot) own(read *schema.Snapshot) schema.Snapshot {
	mine := schema.Snapshot{}

	for _, table := range read.Tables {
		if strings.HasPrefix(table.Name, s.Prefix) {
			mine.Tables = append(mine.Tables, table)
		}
	}

	for _, view := range read.Views {
		if strings.HasPrefix(view.Name, s.Prefix) {
			mine.Views = append(mine.Views, view)
		}
	}

	return mine
}

// normalize rewrites every column type to its logical answer alone, so two engines can compare.
func normalize(read *schema.Snapshot) *schema.Snapshot {
	rewritten := read.Canonical()

	for at, table := range rewritten.Tables {
		for i, column := range table.Columns {
			logical, _ := column.Type.Logical()
			if logical.Zero() {
				continue
			}

			exact, err := schema.ExactType(logical, normalized)
			if err != nil {
				continue
			}

			rewritten.Tables[at].Columns[i].Type = exact
		}
	}

	return &rewritten
}

// render renders a snapshot for a failure, so two engines' answers can be read side by side.
func render(t *testing.T, snapshot schema.Snapshot) string {
	t.Helper()

	encoded, err := schema.Marshal(snapshot)
	if err != nil {
		return fmt.Sprintf("<a snapshot that will not encode: %v>", err)
	}

	return string(encoded)
}

// apply compiles the fixture and runs it, asking the session for anything the compiler cannot.
func (s Snapshot) apply(ctx context.Context, t *testing.T) error {
	return s.run(ctx, s.fixture(t))
}

// One operation is compiled and run before the next is compiled, which is load-bearing rather than
// incidental: an emulated operation is compiled against the database, so a list holding
// two of them -- or one over a table an earlier operation creates -- is only compilable this way
// until M11-T7 makes the engine do the same.

// run compiles each operation and runs its statements before compiling the one after it.
func (s Snapshot) run(ctx context.Context, ops ir.Operations) error {
	for _, op := range ops {
		stmts, err := s.statements(ctx, op)
		if err != nil {
			return fmt.Errorf("%s: %w", op.Kind(), err)
		}

		for _, stmt := range stmts {
			if err := s.Conn.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("%s: %s: %w", op.Kind(), stmt.SQL, err)
			}
		}
	}

	return nil
}

// statements compiles one operation, through the session where this driver compiles against one.
func (s Snapshot) statements(ctx context.Context, op ir.Operation) ([]driver.Statement, error) {
	stmts, err := s.Driver.Compile(op)
	if !errors.Is(err, driver.ErrNeedsSession) {
		return stmts, err
	}

	expander, emulates := s.Conn.(driver.Expander)
	if !emulates {
		return nil, fmt.Errorf("this driver compiles it against the database and its session is "+
			"not a driver.Expander: %w", err)
	}

	return expander.Expand(ctx, op)
}

// clear drops whatever the fixture leaves behind, so a server several runs share stays usable.
func (s Snapshot) clear(ctx context.Context) {
	for _, table := range []string{s.child(), s.parent()} {
		stmts, err := s.Driver.Compile(ir.DropTable{Table: table})
		if err != nil {
			continue
		}

		for _, stmt := range stmts {
			_ = s.Conn.Exec(ctx, stmt)
		}
	}
}
