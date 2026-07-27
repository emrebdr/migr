package drivertest

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// update rewrites golden files from what a driver compiles, instead of holding it against them.
var update = flag.Bool("update", false, "rewrite golden files from what the driver compiles")

// extension is the suffix every golden file carries.
const extension = ".sql"

// marker introduces the metadata lines a statement's own SQL cannot carry.
const marker = "-- migr:"

// The metadata a golden file records beside the statements themselves.
const (
	statementHeader = marker + "statement "
	noTransaction   = " no-transaction"
	unsupported     = marker + "unsupported "
)

// compileRuns is how often each operation compiles, since a map-order bug is intermittent.
const compileRuns = 10

// regenerate is the instruction every stale-golden failure ends with.
const regenerate = "run `make golden-update` and read the diff before committing"

// Suite holds what a driver compiles against the golden files committed for it.
type Suite struct {
	// Driver names the directory under the golden root, spelled as the driver is registered.
	Driver string
	// Compiler is the driver under test, which needs no database to compile.
	Compiler driver.Compiler
	// Root overrides where golden files live, so a test can use a directory it owns.
	Root string
}

// Run compiles every v1 operation and holds the result against its golden file.
func (s Suite) Run(t *testing.T, ops ...ir.Operation) {
	t.Helper()

	for _, problem := range s.check(ops, *update) {
		t.Error(problem)
	}
}

// check compiles every operation and reports everything that disagrees with the golden files.
func (s Suite) check(ops []ir.Operation, updating bool) []string {
	if problems := s.checkSuite(); problems != nil {
		return problems
	}

	root, err := s.root()
	if err != nil {
		return []string{err.Error()}
	}

	if len(ops) == 0 {
		ops = Operations(s.Driver)
	}

	problems := checkCoverage(ops)
	for _, op := range ops {
		problems = append(problems, s.checkOperation(root, op, updating)...)
	}

	return append(problems, s.checkOrphans(root, ops)...)
}

// checkSuite reports a suite that cannot compile anything before it reaches the filesystem.
func (s Suite) checkSuite() []string {
	if s.Driver == "" {
		return []string{"the suite names no driver, so it has no golden directory"}
	}

	if s.Compiler == nil {
		return []string{s.Driver + ": the suite has no compiler"}
	}

	return s.checkCompilerName()
}

// checkCompilerName reports a suite wired to a compiler that answers to a different name.
func (s Suite) checkCompilerName() []string {
	named, reports := s.Compiler.(interface{ Name() string })
	if !reports || named.Name() == s.Driver {
		return nil
	}

	return []string{fmt.Sprintf("the suite records golden files under %q but its compiler "+
		"reports its name as %q", s.Driver, named.Name())}
}

// root returns where golden files live, defaulting to the repository's committed tree.
func (s Suite) root() (string, error) {
	if s.Root != "" {
		return s.Root, nil
	}

	return defaultRoot()
}

// checkCoverage reports operations the suite never offers, so a missing golden means something.
func checkCoverage(ops []ir.Operation) []string {
	offered := make(map[ir.Kind]int, len(ops))
	for _, op := range ops {
		offered[op.Kind()]++
	}

	var problems []string

	for _, kind := range ir.Kinds() {
		if count := offered[kind]; count != 1 {
			problems = append(problems, describeCoverage(kind, count))
		}
	}

	return problems
}

// describeCoverage reports an operation the suite offers no times, or more than one.
func describeCoverage(kind ir.Kind, count int) string {
	if count == 0 {
		return fmt.Sprintf("%s: the suite compiles no such operation, so %s%s could go missing "+
			"without failing", kind, kind, extension)
	}

	return fmt.Sprintf("%s: the suite compiles %d of them, and they share one golden file",
		kind, count)
}

// checkOperation compiles one operation and records or compares it, as the mode requires.
func (s Suite) checkOperation(root string, op ir.Operation, updating bool) []string {
	rel := path.Join(s.Driver, op.Kind().String()+extension)

	if err := op.Validate(); err != nil {
		return []string{fmt.Sprintf("%s: the fixture is not a well-formed operation: %v", rel, err)}
	}

	compiled, err := s.compile(op)
	if err != nil {
		return []string{fmt.Sprintf("%s: %v", rel, err)}
	}

	if updating {
		return record(root, rel, compiled)
	}

	return compare(root, rel, compiled)
}

// compile renders what a driver produces, refusing a driver whose output is not reproducible.
func (s Suite) compile(op ir.Operation) (string, error) {
	first, err := s.render(op)
	if err != nil {
		return "", err
	}

	for range compileRuns - 1 {
		again, err := s.render(op)
		if err != nil {
			return "", err
		}

		if again != first {
			return "", fmt.Errorf("compiling the same operation produced different output, so "+
				"this driver depends on map iteration order or a clock (invariant #8):"+
				"\n  once: %q\n  again: %q", first, again)
		}
	}

	return first, nil
}

// render turns one operation into a golden file body, recording a declared refusal as one.
func (s Suite) render(op ir.Operation) (string, error) {
	stmts, err := s.Compiler.Compile(op)
	if err != nil {
		return s.renderRefusal(op, err)
	}

	if len(stmts) == 0 {
		return "", errors.New("compiled to no statements at all, and reported no refusal")
	}

	return renderStatements(stmts), nil
}

// renderRefusal records a refusal that names itself properly, and passes anything else on as a bug.
func (s Suite) renderRefusal(op ir.Operation, err error) (string, error) {
	var refusal *driver.UnsupportedError
	if !errors.As(err, &refusal) {
		return "", describeFailure(err)
	}

	if problem := s.checkRefusal(op, refusal); problem != "" {
		return "", errors.New(problem)
	}

	return fmt.Sprintf("%s%s: %s\n", unsupported, op.Kind(), refusal.Reason), nil
}

// describeFailure separates a refusal carrying no reason from an ordinary compiler failure.
func describeFailure(err error) error {
	if errors.Is(err, driver.ErrUnsupported) {
		return fmt.Errorf("refused without a reason an operator can act on: %w", err)
	}

	return fmt.Errorf("compiling failed: %w", err)
}

// checkRefusal reports a refusal naming no reason, the wrong operation, or another driver.
func (s Suite) checkRefusal(op ir.Operation, refusal *driver.UnsupportedError) string {
	if strings.TrimSpace(refusal.Reason) == "" {
		return "refused without a reason an operator can act on"
	}

	if refusal.Kind != op.Kind() {
		return fmt.Sprintf("refused %s while being asked about %s", refusal.Kind, op.Kind())
	}

	if refusal.Driver != s.Driver {
		return fmt.Sprintf("the refusal names driver %q, but this suite compiles for %q",
			refusal.Driver, s.Driver)
	}

	return ""
}

// renderStatements renders each statement under a numbered header, so the count is explicit.
func renderStatements(stmts []driver.Statement) string {
	blocks := make([]string, 0, len(stmts))
	for i, stmt := range stmts {
		blocks = append(blocks, renderStatement(i+1, stmt))
	}

	return strings.Join(blocks, "\n\n") + "\n"
}

// renderStatement renders one statement under the header carrying what its SQL cannot.
func renderStatement(position int, stmt driver.Statement) string {
	header := statementHeader + strconv.Itoa(position)
	if stmt.NoTransaction {
		header += noTransaction
	}

	return header + "\n" + stmt.SQL
}

// compare holds rendered output against the committed golden file.
func compare(root, rel, compiled string) []string {
	recorded, err := fs.ReadFile(os.DirFS(root), rel)
	if err != nil {
		return []string{describeUnreadable(rel, err)}
	}

	if string(recorded) == compiled {
		return nil
	}

	return []string{describeDifference(rel, string(recorded), compiled)}
}

// describeUnreadable reports an absent golden file rather than letting the operation pass.
func describeUnreadable(rel string, err error) string {
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Sprintf("%s: no golden file records this operation; %s", rel, regenerate)
	}

	return fmt.Sprintf("%s: %v", rel, err)
}

// describeDifference names the first line that differs and shows both sides of it.
func describeDifference(rel, recorded, compiled string) string {
	recordedLines := strings.Split(recorded, "\n")
	compiledLines := strings.Split(compiled, "\n")
	at := firstDifference(recordedLines, compiledLines)

	return fmt.Sprintf("%s is out of date at line %d:\n  recorded: %s\n  compiled: %s\n  %s",
		rel, at+1, lineAt(recordedLines, at), lineAt(compiledLines, at), regenerate)
}

// firstDifference returns the index of the first line that differs between two renderings.
func firstDifference(recorded, compiled []string) int {
	shared := min(len(recorded), len(compiled))

	for i := range shared {
		if recorded[i] != compiled[i] {
			return i
		}
	}

	return shared
}

// lineAt quotes one line, saying so plainly when that side has no line there at all.
func lineAt(lines []string, i int) string {
	if i >= len(lines) {
		return "(end of file)"
	}

	return strconv.Quote(lines[i])
}

// record writes what the driver compiled, creating the driver's directory if it is new.
func record(root, rel, compiled string) []string {
	full := filepath.Join(root, filepath.FromSlash(rel))

	if err := os.MkdirAll(filepath.Dir(full), 0o750); err != nil {
		return []string{fmt.Sprintf("%s: %v", rel, err)}
	}

	if err := os.WriteFile(full, []byte(compiled), 0o600); err != nil {
		return []string{fmt.Sprintf("%s: %v", rel, err)}
	}

	return nil
}

// checkOrphans reports golden files no operation claims, which is what a renamed op leaves.
func (s Suite) checkOrphans(root string, ops []ir.Operation) []string {
	claimed := make(map[string]bool, len(ops))
	for _, op := range ops {
		claimed[op.Kind().String()+extension] = true
	}

	entries, err := fs.ReadDir(os.DirFS(root), s.Driver)
	if err != nil {
		return nil
	}

	var problems []string

	for _, entry := range entries {
		if !claimed[entry.Name()] {
			problems = append(problems, fmt.Sprintf(
				"%s: a golden file no operation compiles to; delete it or restore the operation",
				path.Join(s.Driver, entry.Name())))
		}
	}

	return problems
}
