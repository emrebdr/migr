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

	t.Log("Every operation is a recorded refusal naming M8-T2, so that task arrives as a diff")
	t.Log("turning each one into SQL rather than as twenty new files -- and an operation that")
	t.Log("silently stopped being refused would be a diff here rather than an edited test.")
}
