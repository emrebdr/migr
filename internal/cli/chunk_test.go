package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/engine"
	"github.com/emrebdr/migr/internal/history"
	"github.com/emrebdr/migr/internal/ir"
)

// chunkedProject is a Go project whose one migration backfills a keyed table, which runs in chunks.
func chunkedProject(t *testing.T) (string, *fake, *driver.Registry) {
	t.Helper()

	dir, f, drivers := goProject(t, "plans")
	f.live = keyedUsers()

	return dir, f, drivers
}

func TestPlanShowsTheChunkADataOnlyMigrationRunsInAndWhatItPagesBy(t *testing.T) {
	dir, _, drivers := chunkedProject(t)

	stdout, _, err := runScripted(t, dir, actBackfills, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	for _, want := range []string{
		`chunks: 1000 rows at a time by "users" (id), pausing 100ms between them, each ` +
			`committed on its own`,
		"-- the bound of a chunk keyed by (id)",
		"-- one chunk of data_backfill",
		"1 operation run in chunks",
		"run in chunks above: each chunk is its own transaction",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("the plan does not show %q:\n%s", want, stdout)
		}
	}

	if strings.Contains(stdout, `WHERE "plan" IS NULL`) {
		t.Errorf("the plan printed the one statement over the whole table, which never runs:\n%s",
			stdout)
	}
}

func TestPlanSaysWhyADataOperationBesideASchemaChangeRunsAsOneStatement(t *testing.T) {
	dir, _, drivers := goProject(t, "plans")

	stdout, _, err := runScripted(t, dir, actBackfillsBeside, drivers, "plan")
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if !strings.Contains(stdout, "runs as one statement in the migration's transaction, because "+
		"the migration also changes the schema -- put it in a migration of its own to run it in "+
		"chunks") {
		t.Errorf("the plan does not say why the backfill runs over the whole table at once:\n%s",
			stdout)
		t.Log("An operator who expected chunks and reads one statement over a large table is")
		t.Log("about to take its rows' locks for a full scan -- the plan says why, and the fix.")
	}
}

func TestPlanCarriesTheChunkShapeInJSON(t *testing.T) {
	dir, _, drivers := chunkedProject(t)

	stdout, _, err := runScripted(t, dir, actBackfills, drivers, "plan", "--json")
	if err != nil {
		t.Fatalf("plan --json: %v", err)
	}

	var doc struct {
		Chunked    int `json:"chunked_operations"`
		Migrations []struct {
			Operations []struct {
				Chunks *struct {
					Table string   `json:"table"`
					Key   []string `json:"key"`
					Rows  int      `json:"rows"`
					Pause string   `json:"pause"`
				} `json:"chunks"`
			} `json:"operations"`
		} `json:"migrations"`
	}

	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("decoding the plan: %v\n%s", err, stdout)
	}

	chunks := doc.Migrations[0].Operations[0].Chunks
	if doc.Chunked != 1 || chunks == nil || chunks.Rows != 1000 || chunks.Table != "users" ||
		chunks.Pause != "100ms" || len(chunks.Key) != 1 {
		t.Errorf("the plan's document carries %d chunked operations and %+v", doc.Chunked, chunks)
	}
}

func TestUpSaysHowManyChunksARunTookAndThatItCarriedOn(t *testing.T) {
	dir, f, drivers := chunkedProject(t)
	f.sliced = []driver.Sliced{
		{Written: 1000, Last: []string{"1000"}},
		{Written: 1000, Last: []string{"2000"}},
	}
	f.chunkFails = 3

	if _, _, err := runScripted(t, dir, actBackfills, drivers, "up"); !errors.Is(err, engine.ErrDirty) {
		t.Fatalf("the up that stopped reported %v, want the dirty state it left", err)
	}

	status, _, err := runScripted(t, dir, actBackfills, drivers, "status")
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	if !strings.Contains(status, "stopped running in chunks: operation 1, 2 chunks and 2000 rows "+
		"written, through key (2000) -- the next migr up carries on from there") {
		t.Errorf("status does not say how far the stopped run got:\n%s", status)
	}

	f.chunkFails = 0

	stdout, _, err := runScripted(t, dir, actBackfills, drivers, "up")
	if err != nil {
		t.Fatalf("the up that carries on: %v", err)
	}

	if !strings.Contains(stdout, "1 chunk, 1 row written, resumed") {
		t.Errorf("the up that carried on does not say so:\n%s", stdout)
		t.Log("A run reports the chunks it ran itself: the two the stopped run committed are")
		t.Log("that run's, and status said so before this one started.")
	}

	if last := f.chunks[len(f.chunks)-1]; len(last.After) != 1 || last.After[0] != "2000" {
		t.Errorf("the resumed run's chunk started after %v, want after the checkpoint's 2000",
			last.After)
	}
}

func TestADirtyChunkedRunCarriesItsCheckpointInTheFailureDocument(t *testing.T) {
	var out bytes.Buffer

	err := &engine.DirtyStateError{Version: "20260727T153000", Direction: ir.Up,
		LastStatement: 1, Cause: "the server refused this statement",
		Checkpoint: &history.Checkpoint{Version: "20260727T153000", Direction: ir.Up, Step: 0,
			Cursor: []string{"2000"}, Chunks: 2, Written: 2000}}

	if werr := writeFailure(&out, err); werr != nil {
		t.Fatalf("writeFailure: %v", werr)
	}

	var doc struct {
		Dirty struct {
			Chunk *checkpoint `json:"chunk"`
		} `json:"dirty"`
	}

	if jerr := json.Unmarshal(out.Bytes(), &doc); jerr != nil {
		t.Fatalf("decoding the failure: %v\n%s", jerr, out.String())
	}

	if doc.Dirty.Chunk == nil || doc.Dirty.Chunk.Chunks != 2 || doc.Dirty.Chunk.Cursor[0] != "2000" {
		t.Errorf("the failure document carries the checkpoint %+v:\n%s", doc.Dirty.Chunk,
			out.String())
	}
}
