package drivertest

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/capability"
	"github.com/emrebdr/migr/internal/ir"
)

// everythingBrief is a declaration that performs every operation at a brief lock, which is a fact nobody would write.
func everythingBrief(t *testing.T) capability.Set {
	t.Helper()

	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = func(ir.Operation) capability.Verdict { return capability.Native(capability.BriefExclusive) }
	}

	set, err := capability.NewSet(capability.Facts{Driver: "fake", Server: "1"}, rules)
	if err != nil {
		t.Fatalf("NewSet: %v", err)
	}

	return set
}

// guideWith writes a guide holding one block for the fake engine, with the given body between its markers.
func guideWith(t *testing.T, body string) string {
	t.Helper()

	doc := filepath.Join(t.TempDir(), "zero-downtime.md")
	text := "# guide\n\n<!-- migr:lock-table fake 1 -->\n" + body + "<!-- /migr:lock-table -->\n\nprose after\n"

	if err := os.WriteFile(doc, []byte(text), 0o600); err != nil {
		t.Fatalf("writing the guide: %v", err)
	}

	return doc
}

func TestEveryKindHasARowInTheLockTable(t *testing.T) {
	var kinds []ir.Kind
	for _, c := range LockCases("fake") {
		kinds = append(kinds, c.Op.Kind())
	}

	for _, kind := range ir.Kinds() {
		if !slices.Contains(kinds, kind) {
			t.Errorf("%s has no row in the guide's lock tables", kind)
		}
	}
}

func TestAStaleLockTableFailsAndUpdateRewritesOnlyItsBlock(t *testing.T) {
	doc := guideWith(t, "| an old table |\n")
	table := LockTable{Engine: "fake 1", Driver: "fake", Capabilities: everythingBrief(t), Doc: doc}

	if problem := table.check(false); !strings.Contains(problem, "is not what the driver declares") {
		t.Fatalf("a stale table was reported as %q", problem)
	}

	if problem := table.check(true); problem != "" {
		t.Fatalf("-update reported %q", problem)
	}

	written, err := os.ReadFile(doc)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(written), table.Render()) || !strings.HasSuffix(string(written), "\n\nprose after\n") ||
		!strings.HasPrefix(string(written), "# guide\n") {
		t.Errorf("-update did not replace the block alone:\n%s", written)
	}

	if problem := table.check(false); problem != "" {
		t.Errorf("a regenerated table is still reported stale: %s", problem)
	}
}

func TestAGuideWithNoBlockForTheEngineFailsNamingTheMarkers(t *testing.T) {
	doc := guideWith(t, "")
	table := LockTable{Engine: "other 2", Driver: "fake", Capabilities: everythingBrief(t), Doc: doc}

	for _, updating := range []bool{false, true} {
		if problem := table.check(updating); !strings.Contains(problem, "<!-- migr:lock-table other 2 -->") {
			t.Errorf("a missing block (update %v) was reported as %q", updating, problem)
		}
	}
}

func TestARefusedOperationIsListedWithTheDriversReason(t *testing.T) {
	rules := make(map[ir.Kind]capability.Rule, len(ir.Kinds()))
	for _, kind := range ir.Kinds() {
		rules[kind] = func(ir.Operation) capability.Verdict { return capability.Native(capability.TableRewrite) }
	}

	rules[ir.KindDropColumn] = func(ir.Operation) capability.Verdict {
		return capability.Unsupported("this engine | keeps\nevery column")
	}

	set, err := capability.NewSet(capability.Facts{Driver: "fake", Server: "1"}, rules)
	if err != nil {
		t.Fatal(err)
	}

	rendered := LockTable{Engine: "fake 1", Driver: "fake", Capabilities: set}.Render()

	for _, want := range []string{
		"| drop_column | unsupported | — | — | — |",
		`- **drop_column**: this engine \| keeps every column`,
		"| create_table | native | `table_rewrite` | `table_rewrite` | **no** |",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("the table does not hold %q:\n%s", want, rendered)
		}
	}
}
