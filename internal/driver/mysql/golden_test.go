package mysql

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

	t.Log("Two operations record their SQL and eighteen record a refusal naming M9-T2. The two are")
	t.Log("create_table and add_index, which the history table is itself created through (invariant")
	t.Log("#3), so M9-T2 arrives here as a diff of eighteen rather than as an edited test.")
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
