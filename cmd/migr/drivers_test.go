package main

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/mysql"
	"github.com/emrebdr/migr/internal/driver/postgres"
	"github.com/emrebdr/migr/internal/driver/sqlite"
)

func TestEveryDriverThisBinaryRegistersCanBeBuilt(t *testing.T) {
	registry, err := drivers()
	if err != nil {
		t.Fatalf("drivers: %v", err)
	}

	want := []string{mysql.Name, postgres.Name, sqlite.Name}
	if got := registry.Names(); !slices.Equal(got, want) {
		t.Fatalf("this binary registers %v, want %v", got, want)
	}

	for _, name := range want {
		built, err := registry.New(name, driver.Options{})
		if err != nil {
			t.Errorf("building %s: %v", name, err)

			continue
		}

		if built.Name() != name {
			t.Errorf("%s reports its name as %q", name, built.Name())
		}
	}

	t.Log("This is the only package that may name a concrete driver (invariant #1), so a driver")
	t.Log("whose constructor refuses the options a bare project gives it is unreachable and")
	t.Log("nothing else would say so.")
}
