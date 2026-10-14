package drivertest

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// chunkDir is the directory under a driver's own that its chunk golden files live in.
const chunkDir = "chunk"

// chunkKeys are the primary keys every chunk golden file renders a chunk under: one column, and two.
var chunkKeys = [][]string{{"id"}, {"tenant", "id"}}

// Chunks holds the statements one chunk of each data operation is against the golden files for it.
type Chunks struct {
	// Driver names the directory under the golden root, spelled as the driver is registered.
	Driver string
	// Compiler is the driver under test, which renders a chunk with no database.
	Compiler driver.ChunkCompiler
	// Root overrides where golden files live, so a test can use a directory it owns.
	Root string
}

// Run renders one chunk of every data operation under each key and holds it against its golden file.
func (c Chunks) Run(t *testing.T) {
	t.Helper()

	for _, problem := range c.check(*update) {
		t.Error(problem)
	}
}

// check renders every data operation's chunk and reports everything that disagrees with the files.
func (c Chunks) check(updating bool) []string {
	if c.Driver == "" || c.Compiler == nil {
		return []string{"the chunk suite needs a driver name and the compiler to render with"}
	}

	root := c.Root
	if root == "" {
		found, err := defaultRoot()
		if err != nil {
			return []string{err.Error()}
		}

		root = found
	}

	var problems []string

	for _, op := range dataOperations(c.Driver) {
		rel := path.Join(c.Driver, chunkDir, op.Kind().String()+extension)

		rendered, err := c.render(op)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", rel, err))

			continue
		}

		if updating {
			problems = append(problems, record(root, rel, rendered)...)
		} else {
			problems = append(problems, compare(root, rel, rendered)...)
		}
	}

	return append(problems, c.checkOrphans(root)...)
}

// dataOperations returns the representative operation of every data kind, in the order Kinds lists them.
func dataOperations(name string) []ir.Operation {
	var ops []ir.Operation

	for _, op := range Operations(name) {
		if slices.Contains(ir.DataKinds(), op.Kind()) {
			ops = append(ops, op)
		}
	}

	return ops
}

// render turns one operation into a chunk golden file body, a section per key.
func (c Chunks) render(op ir.Operation) (string, error) {
	sections := make([]string, 0, len(chunkKeys))

	for _, key := range chunkKeys {
		stmts, err := c.Compiler.CompileChunk(op, key)
		if err != nil {
			return "", fmt.Errorf("rendering a chunk keyed by (%s): %w", strings.Join(key, ", "), err)
		}

		if len(stmts) == 0 {
			return "", fmt.Errorf("a chunk keyed by (%s) rendered no statements", strings.Join(key, ", "))
		}

		sections = append(sections, chunkHeader+"("+strings.Join(key, ", ")+")\n"+
			renderStatements(stmts))
	}

	return strings.Join(sections, "\n"), nil
}

// checkOrphans reports chunk golden files no data operation renders.
func (c Chunks) checkOrphans(root string) []string {
	claimed := make(map[string]bool)
	for _, op := range dataOperations(c.Driver) {
		claimed[op.Kind().String()+extension] = true
	}

	entries, err := fs.ReadDir(os.DirFS(root), path.Join(c.Driver, chunkDir))
	if err != nil {
		return nil
	}

	var problems []string

	for _, entry := range entries {
		if !claimed[entry.Name()] {
			problems = append(problems, fmt.Sprintf("%s: a chunk golden file no data operation "+
				"renders; delete it or restore the operation", path.Join(c.Driver, chunkDir,
				entry.Name())))
		}
	}

	return problems
}
