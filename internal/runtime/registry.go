package runtime

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Constructor builds a runtime adapter, which needs nothing from migr.yaml that a Project lacks.
type Constructor func() (Runtime, error)

// ErrNoConstructor reports a registration with nothing to construct.
var ErrNoConstructor = errors.New("constructor is required")

// Registry resolves a language name to the adapter registered under it.
type Registry struct {
	mu    sync.RWMutex
	ctors map[string]Constructor
}

// NewRegistry returns an empty registry. Registries share no state with one another.
func NewRegistry() *Registry {
	return &Registry{ctors: make(map[string]Constructor)}
}

// DuplicateError reports a name that was already registered.
type DuplicateError struct {
	// Name is the language that was registered twice.
	Name string
}

// Error names the language that was registered more than once.
func (e *DuplicateError) Error() string {
	return fmt.Sprintf("runtime %q is already registered", e.Name)
}

// UnknownError reports a language this build has no adapter for.
type UnknownError struct {
	// Name is the language that was asked for.
	Name string
	// Known lists every language this build does have, sorted.
	Known []string
}

// Error names the language that was asked for and the ones this build has.
func (e *UnknownError) Error() string {
	return fmt.Sprintf("unknown runtime %q: this build of migr has %s", e.Name, describeKnown(e.Known))
}

// describeKnown renders the registered set, saying so plainly when it is empty.
func describeKnown(known []string) string {
	if len(known) == 0 {
		return "no runtimes registered"
	}

	return strings.Join(known, ", ")
}

// Register adds an adapter under a language name, refusing a duplicate rather than replacing it.
func (r *Registry) Register(name string, build Constructor) error {
	if err := checkName(name); err != nil {
		return err
	}

	if build == nil {
		return fmt.Errorf("runtime %q: %w", name, ErrNoConstructor)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.ctors[name]; taken {
		return &DuplicateError{Name: name}
	}

	r.ctors[name] = build

	return nil
}

// New builds the adapter registered under a language, or reports which languages exist.
func (r *Registry) New(name string) (Runtime, error) {
	r.mu.RLock()
	build, ok := r.ctors[name]
	r.mu.RUnlock()

	if !ok {
		return nil, &UnknownError{Name: name, Known: r.Names()}
	}

	rt, err := build()
	if err != nil {
		return nil, fmt.Errorf("runtime %q: %w", name, err)
	}

	if rt == nil {
		return nil, fmt.Errorf("runtime %q: constructor returned no runtime", name)
	}

	return rt, checkReportedName(name, rt.Name())
}

// checkReportedName reports whether an adapter answers to the name it was registered under.
func checkReportedName(registered, reported string) error {
	if reported == registered {
		return nil
	}

	return fmt.Errorf("runtime %q reports its name as %q; the registry key and the runtime must agree",
		registered, reported)
}

// Names returns every registered language, sorted, as a copy.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	names := make([]string, 0, len(r.ctors))
	for name := range r.ctors {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
}

// checkName reports whether a language name is the one canonical spelling of itself.
func checkName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("runtime name is required")
	}

	if lower := strings.ToLower(name); lower != name {
		return fmt.Errorf("runtime name %q must be lowercase (use %q)", name, lower)
	}

	if strings.TrimSpace(name) != name {
		return fmt.Errorf("runtime name %q must not be padded with whitespace", name)
	}

	return nil
}
