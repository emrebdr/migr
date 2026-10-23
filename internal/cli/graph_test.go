package cli

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/visual"
)

func TestGraphDrawsEachStepOfAChangeADeployAfterTheOneBefore(t *testing.T) {
	dir, _, drivers := stepping(t)

	stdout, _, err := runDeclaring(t, dir, actEmits, declaresSteps, drivers, "graph")
	if err != nil {
		t.Fatalf("migr graph: %v", err)
	}

	if want := "step        users.email: step1, a deploy after 20260727T153000_swap_email.go"; !strings.Contains(stdout, want) {
		t.Errorf("migr graph printed\n%s\nwant a line %q", stdout, want)
	}

	mermaid, _, err := runDeclaring(t, dir, actEmits, declaresSteps, drivers, "graph", "--format", "mermaid")
	if err != nil {
		t.Fatalf("migr graph --format mermaid: %v", err)
	}

	if want := "m20260727T153000 -.->|next deploy| m20260727T153001"; !strings.Contains(mermaid, want) {
		t.Errorf("migr graph --format mermaid printed\n%s\nwant an edge %q", mermaid, want)
	}
}

func TestGraphJSONCarriesEveryMigrationAndEveryEdge(t *testing.T) {
	dir, _, drivers := stepping(t)

	stdout, _, err := runDeclaring(t, dir, actEmits, declaresNext, drivers, "--json", "graph")
	if err != nil {
		t.Fatalf("migr --json graph: %v", err)
	}

	var got visual.Graph
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding %s: %v", stdout, err)
	}

	if len(got.Nodes) != 2 || got.Nodes[0].Version != versionAt(1) {
		t.Errorf("migrations = %+v, want both, the later version first since the earlier declares it", got.Nodes)
	}

	want := visual.Edge{From: versionAt(1), To: versionAt(0), Kind: visual.EdgeDependency}
	if len(got.Edges) != 1 || got.Edges[0] != want {
		t.Errorf("edges = %+v, want [%+v]", got.Edges, want)
	}
}

func TestGraphDrawsTheSchemaAGoProjectBuildsAsADiagram(t *testing.T) {
	dir, _, drivers := pendingGoProject(t)

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "graph", "--schema", "--format", "mermaid")
	if err != nil {
		t.Fatalf("migr graph --schema --format mermaid: %v", err)
	}

	for _, want := range []string{"erDiagram", "  " + scriptedTable + " {", "    int64 id", "    text note"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("migr graph --schema --format mermaid printed\n%s\nwant %q", stdout, want)
		}
	}
}

func TestGraphStopsTheSchemaAtTheVersionToNames(t *testing.T) {
	dir, _, drivers := goProject(t, "first", "second")

	stdout, _, err := runScripted(t, dir, actTypes, drivers, "--json", "graph", "--schema", "--to", versionAt(0))
	if err != nil {
		t.Fatalf("migr --json graph --schema --to: %v", err)
	}

	var got struct {
		Migrations []visual.Step `json:"migrations"`
		Known      bool          `json:"known"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decoding %s: %v", stdout, err)
	}

	if len(got.Migrations) != 1 || !got.Known {
		t.Errorf("migrations = %+v, known = %v; want the first alone, its schema known", got.Migrations, got.Known)
	}
}

func TestGraphSaysWhereASQLProjectsSchemaStopsBeingKnown(t *testing.T) {
	dir, _, drivers := staged(t)

	stdout, _, err := runWith(t, dir, noon, drivers, "graph", "--schema")
	if err != nil {
		t.Fatalf("migr graph --schema: %v", err)
	}

	for _, want := range []string{
		"? the schema after this cannot be read from the migrations: operation 1 (raw) is engine SQL",
		"? not projected",
		"ended the projection.",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("migr graph --schema printed\n%s\nwant %q", stdout, want)
		}
	}
}
