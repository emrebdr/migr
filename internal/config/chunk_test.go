package config

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
)

func TestChunkingDefaultsToAPacedSizeAndZeroTurnsItOff(t *testing.T) {
	cfg, err := loadSeeding(t, "", "")
	if err != nil {
		t.Fatalf("a config naming no safety block must load: %v", err)
	}

	if cfg.Safety.Rows() != driver.DefaultChunkRows ||
		cfg.Safety.ChunkPause.Duration() != driver.DefaultChunkPause {
		t.Errorf("chunking defaulted to %d rows paused %s, want %d and %s", cfg.Safety.Rows(),
			cfg.Safety.ChunkPause, driver.DefaultChunkRows, driver.DefaultChunkPause)
	}

	off, err := loadSeeding(t, "safety:\n  chunk_rows: 0\n", "")
	if err != nil {
		t.Fatalf("chunk_rows: 0 was refused: %v", err)
	}

	if off.Safety.Rows() != 0 {
		t.Errorf("chunk_rows: 0 resolved to %d, want 0 -- which runs every data operation as one "+
			"statement", off.Safety.Rows())
		t.Log("Zero is the value, not the absence of one: it is the remedy the engine names for a")
		t.Log("table with no primary key, so defaulting it away would leave that table unrunnable.")
	}
}

func TestANegativeChunkOrPauseIsRefused(t *testing.T) {
	cases := map[string]string{
		"safety.chunk_rows":  "safety:\n  chunk_rows: -1\n",
		"safety.chunk_pause": "safety:\n  chunk_pause: -5ms\n",
	}

	for path, block := range cases {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, path); !found {
			t.Errorf("%q loaded, or failed without naming %s: %v", block, path, err)
		}
	}
}

func TestNeitherHistoryMayBeNamedAsTheOthersCheckpointTable(t *testing.T) {
	cases := map[string]string{
		"seeds.table":   "history:\n  table: runs\nseeds:\n  table: runs_progress\n",
		"history.table": "history:\n  table: plants_progress\nseeds:\n  table: plants\n",
	}

	for path, block := range cases {
		_, err := loadSeeding(t, block, "")
		if _, found := fieldProblem(err, path); !found {
			t.Errorf("%q loaded, or failed without naming %s: %v", block, path, err)
			t.Log("Each history keeps its checkpoints in a table named after it, so one named as the")
			t.Log("other's checkpoint table would have Ensure create a table the other already is.")
		}
	}
}
