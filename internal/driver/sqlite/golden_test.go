package sqlite

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

	t.Log("Thirteen operations record their SQL and seven record a refusal naming the table")
	t.Log("rebuild, so an operation that started or stopped being refused -- and M8-T4 turning a")
	t.Log("refusal into statements -- arrives as a diff here rather than as an edited test.")
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
