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

	t.Log("Every golden file records a refusal today, because this driver compiles nothing yet.")
	t.Log("That is the format's purpose rather than a placeholder: M3-T2 arrives as a diff")
	t.Log("turning each refusal into SQL, and an operation that ever stops compiling arrives")
	t.Log("the same way instead of as an edited test.")
}
