package main

import (
	"github.com/emrebdr/migr/internal/driver"
	"github.com/emrebdr/migr/internal/driver/postgres"
)

// drivers builds the registry this binary can reach, refusing a name registered twice.
func drivers() (*driver.Registry, error) {
	registry := driver.NewRegistry()

	if err := registry.Register(postgres.Name, postgres.New); err != nil {
		return nil, err
	}

	return registry, nil
}
