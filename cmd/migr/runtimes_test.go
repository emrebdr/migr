package main

import (
	"slices"
	"testing"

	"github.com/emrebdr/migr/internal/config"
	"github.com/emrebdr/migr/internal/runtime/golang"
	"github.com/emrebdr/migr/internal/runtime/node"
)

func TestEveryRegisteredRuntimeIsALanguageMigrYamlAccepts(t *testing.T) {
	registry, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry: %v", err)
	}

	accepted := config.Languages()

	for _, name := range registry.Names() {
		if !slices.Contains(accepted, config.Language(name)) {
			t.Log("A registry key nothing can name in migrations.language is unreachable: the")
			t.Log("config refuses the value before anything asks the registry for the adapter.")
			t.Errorf("runtime %q is registered, and migr.yaml accepts %v", name, accepted)
		}
	}
}

func TestTheGoAdapterIsRegisteredUnderTheLanguageMigrYamlSpells(t *testing.T) {
	registry, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry: %v", err)
	}

	if golang.Name != string(config.LanguageGo) {
		t.Errorf("the adapter names itself %q and migr.yaml spells it %q",
			golang.Name, config.LanguageGo)
	}

	adapter, err := registry.New(string(config.LanguageGo))
	if err != nil {
		t.Fatalf("the registry has nothing under %q: %v", config.LanguageGo, err)
	}

	if adapter.Name() != golang.Name {
		t.Errorf("the registry built an adapter naming itself %q", adapter.Name())
	}
}

func TestTheNodeAdapterIsRegisteredUnderTheLanguageMigrYamlSpells(t *testing.T) {
	registry, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry: %v", err)
	}

	if node.Name != string(config.LanguageTypeScript) {
		t.Log("The adapter runs TypeScript and JavaScript both, and the key is the one migr.yaml")
		t.Log("spells, since a registry key nothing can name in migrations.language is unreachable.")
		t.Errorf("the adapter names itself %q and migr.yaml spells it %q",
			node.Name, config.LanguageTypeScript)
	}

	adapter, err := registry.New(string(config.LanguageTypeScript))
	if err != nil {
		t.Fatalf("the registry has nothing under %q: %v", config.LanguageTypeScript, err)
	}

	if adapter.Name() != node.Name {
		t.Errorf("the registry built an adapter naming itself %q", adapter.Name())
	}
}

func TestBuildingTheRuntimeRegistryTwiceYieldsIndependentOnes(t *testing.T) {
	first, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry: %v", err)
	}

	second, err := runtimes()
	if err != nil {
		t.Fatalf("building the runtime registry a second time: %v", err)
	}

	if first == second {
		t.Error("two calls returned one registry, so a duplicate would go unreported")
	}
}
