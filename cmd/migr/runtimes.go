package main

import (
	"github.com/emrebdr/migr/internal/runtime"
	"github.com/emrebdr/migr/internal/runtime/golang"
)

// runtimes builds the registry of languages this binary can run, refusing a name registered twice.
func runtimes() (*runtime.Registry, error) {
	registry := runtime.NewRegistry()

	if err := registry.Register(golang.Name, golang.New); err != nil {
		return nil, err
	}

	return registry, nil
}
