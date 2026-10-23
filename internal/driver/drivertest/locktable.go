package drivertest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/ir"
)

// guide is the document every lock table is held against, relative to the repository root.
var guide = filepath.Join("docs", "zero-downtime.md")

// The two lines that bracket one engine's table in the guide; what lies between them is generated.
const (
	openMarker  = "<!-- migr:lock-table %s -->"
	closeMarker = "<!-- /migr:lock-table -->"
)

// LockCase is one row of a lock table: an operation, and the words the row names it by.
type LockCase struct {
	// Label is what the row says the operation is, naming the variant where the payload decides the cost.
	Label string
	// Op is the operation the row's verdict is resolved for.
	Op ir.Operation
}

// LockCases returns every row a lock table lists: each kind once, and again for each payload that changes its cost.
func LockCases(driverName string) []LockCase {
	ops := Operations(driverName)
	cases := make([]LockCase, 0, len(ops)+8)

	for _, op := range ops {
		cases = append(cases, variantsOf(op)...)
	}

	return cases
}

// variantsOf names one representative operation's row, plus the variants of its kind that are costed apart.
func variantsOf(op ir.Operation) []LockCase {
	switch o := op.(type) {
	case ir.AddColumn:
		return addColumnVariants(o)
	case ir.AlterColumn:
		return alterColumnVariants(o.Table, o.Column)
	case ir.CreateView:
		replacing := o
		replacing.OrReplace = true

		return []LockCase{{Label: "create_view", Op: o}, {Label: "create_view, or replace", Op: replacing}}
	default:
		return []LockCase{{Label: op.Kind().String(), Op: op}}
	}
}

// addColumnVariants are the three defaults whose cost differs: none, a constant, and one evaluated per row.
func addColumnVariants(o ir.AddColumn) []LockCase {
	bare, constant, volatile := o, o, o
	bare.Column.Default = nil
	volatile.Column.Type, volatile.Column.Default = typeOf("uuid"), ir.Call(ir.FuncUUID)

	return []LockCase{
		{Label: "add_column, nullable, no default", Op: bare},
		{Label: "add_column, constant default", Op: constant},
		{Label: "add_column, default evaluated per row (uuid)", Op: volatile},
	}
}

// alterColumnVariants are the changes one column takes, each of which an engine costs on its own.
func alterColumnVariants(table, column string) []LockCase {
	alter := func(change func(*ir.AlterColumn)) ir.AlterColumn {
		o := ir.AlterColumn{Table: table, Column: column}
		change(&o)

		return o
	}

	return []LockCase{
		{Label: "alter_column, change type", Op: alter(func(o *ir.AlterColumn) { o.Type = pointerTo(typeOf("string(512)")) })},
		{Label: "alter_column, set NOT NULL", Op: alter(func(o *ir.AlterColumn) { o.Nullable = pointerTo(false) })},
		{Label: "alter_column, drop NOT NULL", Op: alter(func(o *ir.AlterColumn) { o.Nullable = pointerTo(true) })},
		{Label: "alter_column, set default", Op: alter(func(o *ir.AlterColumn) { o.Default = ir.Literal("x") })},
		{Label: "alter_column, drop default", Op: alter(func(o *ir.AlterColumn) { o.DropDefault = true })},
	}
}

// LockTable holds one engine's table in the zero-downtime guide to what the engine declares.
type LockTable struct {
	// Engine names the table's block in the guide, such as "postgres 17".
	Engine string
	// Driver is the name raw operations are addressed to.
	Driver string
	// Capabilities is the declaration Open would build for the server the table describes.
	Capabilities capability.Set
	// Rewriter is how a session of that server performs an operation more safely, nil where it never does.
	Rewriter driver.Rewriter
	// Doc overrides the guide's path, so a test can use a file it owns.
	Doc string
}

// Run renders the table and holds the guide's block to it, rewriting the block under -update.
func (l LockTable) Run(t *testing.T) {
	t.Helper()

	if problem := l.check(*update); problem != "" {
		t.Error(problem)
	}
}

// check compares the guide's block with the rendered table, or rewrites it, and reports what disagrees.
func (l LockTable) check(updating bool) string {
	doc, err := l.path()
	if err != nil {
		return err.Error()
	}

	written, err := os.ReadFile(doc)
	if err != nil {
		return fmt.Sprintf("reading the guide: %v", err)
	}

	before, recorded, after, found := l.block(string(written))
	if !found {
		return fmt.Sprintf("%s holds no block for %s: add the lines %q and %q where its table goes",
			doc, l.Engine, fmt.Sprintf(openMarker, l.Engine), closeMarker)
	}

	rendered := l.Render()
	if recorded == rendered {
		return ""
	}

	if updating {
		if err := rewrite(doc, before+rendered+after); err != nil {
			return fmt.Sprintf("rewriting %s: %v", doc, err)
		}

		return ""
	}

	return fmt.Sprintf("the %s lock table in %s is not what the driver declares.\n--- in the guide\n%s--- declared\n%s%s",
		l.Engine, doc, recorded, rendered, regenerate)
}

// rewrite replaces the guide's contents, writing nowhere but the directory it is in.
func rewrite(doc, text string) error {
	root, err := os.OpenRoot(filepath.Dir(doc))
	if err != nil {
		return err
	}

	return errors.Join(root.WriteFile(filepath.Base(doc), []byte(text), 0o600), root.Close())
}

// path resolves the guide this table lives in.
func (l LockTable) path() (string, error) {
	if l.Doc != "" {
		return l.Doc, nil
	}

	start, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("locating the repository root: %w", err)
	}

	repo, err := repositoryRoot(start)
	if err != nil {
		return "", err
	}

	return filepath.Join(repo, guide), nil
}

// block splits a document around this engine's generated table: what precedes it, the table, what follows.
func (l LockTable) block(doc string) (before, table, after string, found bool) {
	opening := fmt.Sprintf(openMarker, l.Engine) + "\n"

	start := strings.Index(doc, opening)
	if start < 0 {
		return "", "", "", false
	}

	body := start + len(opening)

	end := strings.Index(doc[body:], closeMarker)
	if end < 0 {
		return "", "", "", false
	}

	return doc[:body], doc[body : body+end], doc[body+end:], true
}

// Render writes the table: one row per case, then every refusal with the reason the driver gives.
func (l LockTable) Render() string {
	var b strings.Builder

	b.WriteString("| Operation | Support | Declared cost | Cost as migr runs it | Zero-downtime |\n")
	b.WriteString("|---|---|---|---|---|\n")

	var refused []string

	for _, c := range LockCases(l.Driver) {
		row, refusal := l.row(c)
		b.WriteString(row)

		if refusal != "" {
			refused = append(refused, refusal)
		}
	}

	if len(refused) > 0 {
		b.WriteString("\nRefused on this server:\n\n")

		for _, r := range slices.Compact(refused) {
			b.WriteString(r)
		}
	}

	return b.String()
}

// row renders one case, and the refusal line beneath the table where the server will not perform it.
func (l LockTable) row(c LockCase) (string, string) {
	v := l.Capabilities.Resolve(c.Op)
	if !v.Allowed() {
		return fmt.Sprintf("| %s | unsupported | — | — | — |\n", c.Label),
			fmt.Sprintf("- **%s**: %s\n", c.Label, oneLine(v.Reason()))
	}

	ran, via := l.asRun(c.Op, v)

	return fmt.Sprintf("| %s | %s | %s | %s%s | %s |\n", c.Label, supportOf(v), declaredOf(v), code(ran), via,
		verdictOf(v, ran)), ""
}

// asRun is what the operation costs as a run performs it, and the rewrite switch that makes it so.
func (l LockTable) asRun(op ir.Operation, v capability.Verdict) (capability.LockImpact, string) {
	if l.Rewriter == nil {
		return v.Lock(), ""
	}

	rewrite, rewritten := l.Rewriter.Rewrite(op)
	if !rewritten || !rewrite.Lock.AtMost(v.Lock()) {
		return v.Lock(), ""
	}

	return rewrite.Lock, fmt.Sprintf(", rewritten (`safety.rewrites.%s`)", rewrite.Name)
}

// supportOf names how the engine performs an operation it allows.
func supportOf(v capability.Verdict) string {
	if v.Support() == capability.SupportEmulated {
		return fmt.Sprintf("emulated (%s)", v.Strategy())
	}

	return string(v.Support())
}

// declaredOf is the verdict's cost, with the cheaper one a table may earn where the verdict names one.
func declaredOf(v capability.Verdict) string {
	if v.Condition() == "" {
		return code(v.Lock())
	}

	return fmt.Sprintf("%s, or %s where %s", code(v.Lock()), code(v.Lowest()), oneLine(v.Condition()))
}

// verdictOf answers the column a reader is looking for: whether clients keep working while it runs.
func verdictOf(v capability.Verdict, ran capability.LockImpact) string {
	switch {
	case ran.AtMost(capability.BriefExclusive):
		return "yes"
	case v.Condition() != "" && v.Lowest().AtMost(capability.BriefExclusive):
		return "where the table allows"
	default:
		return "**no**"
	}
}

// code renders a lock cost as the config value it is spelled as.
func code(l capability.LockImpact) string {
	return "`" + l.String() + "`"
}

// oneLine keeps a driver's sentence inside one markdown line, which a table cell and a list item both need.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "|", `\|`)), " ")
}
