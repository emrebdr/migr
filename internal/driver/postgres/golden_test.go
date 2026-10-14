package postgres

import (
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/drivertest"
)

func TestGolden(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	drivertest.Suite{Driver: Name, Compiler: d}.Run(t)

	t.Log("The harness compiles each operation ten times and fails on any difference, so a")
	t.Log("REFERENCES list or a column list rendered from a map rather than the declared order")
	t.Log("is caught here rather than as an intermittent golden diff (invariant #8).")
	t.Log("No statement carries no-transaction: nothing this driver emits opts out of a")
	t.Log("transaction until CREATE INDEX CONCURRENTLY arrives in M13-T11.")
}

func TestChunkGolden(t *testing.T) {
	d, err := New(driver.Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	chunker, renders := d.(driver.ChunkCompiler)
	if !renders {
		t.Fatal("the driver renders no chunk of a data operation")
	}

	drivertest.Chunks{Driver: Name, Compiler: chunker}.Run(t)
}
