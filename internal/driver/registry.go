package driver

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Options are the settings a driver needs at construction, before any connection exists.
type Options struct {
	// Schema is the namespace migrations target, where the engine has one.
	Schema string
	// HistoryTable is where migrations are recorded, and with Schema it names one installation.
	HistoryTable string
	// SeedTable is where seeds are recorded, on the same session and under the same lock.
	SeedTable string
	// LockStrategy is how migr.yaml asked for the migration lock, empty meaning the driver's own.
	LockStrategy string
	// LockWait bounds the wait for the migration lock, and is migr.yaml's lock.timeout.
	LockWait time.Duration
	// StatementLockWait bounds one statement's wait for a lock, and is migr.yaml's safety.lock_timeout.
	StatementLockWait time.Duration
}

// Namespace returns what identifies one migr installation, defaulting what migr.yaml left unset.
func (o Options) Namespace() (schema, table string) {
	if o.HistoryTable == "" {
		return o.Schema, DefaultHistoryTable
	}

	return o.Schema, o.HistoryTable
}

// Seeding returns the table seeds are recorded in, defaulting what migr.yaml left unset.
func (o Options) Seeding() string {
	if o.SeedTable == "" {
		return DefaultSeedTable
	}

	return o.SeedTable
}

// Waits returns the two lock bounds, defaulting whichever migr.yaml left unset.
func (o Options) Waits() (lock, statement time.Duration) {
	return orDefault(o.LockWait, DefaultLockWait), orDefault(o.StatementLockWait,
		DefaultStatementLockWait)
}

// orDefault substitutes the contract's bound for a wait a caller left unset, since none means forever.
func orDefault(wait, fallback time.Duration) time.Duration {
	if wait <= 0 {
		return fallback
	}

	return wait
}

// Constructor builds a driver from settings resolved out of migr.yaml.
type Constructor func(opts Options) (Driver, error)

// ErrNoConstructor reports a registration with nothing to construct.
var ErrNoConstructor = errors.New("constructor is required")

// Registry resolves a driver name to the constructor registered under it.
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
	// Name is the driver name that was registered twice.
	Name string
}

// Error names the driver that was registered more than once.
func (e *DuplicateError) Error() string {
	return fmt.Sprintf("driver %q is already registered", e.Name)
}

// UnknownError reports a driver name this build has nothing registered under.
type UnknownError struct {
	// Name is the driver that was asked for.
	Name string
	// Known lists every driver this build does have, sorted.
	Known []string
}

// Error names the driver that was asked for and the ones this build has.
func (e *UnknownError) Error() string {
	return fmt.Sprintf("unknown driver %q: this build of migr has %s", e.Name, describeKnown(e.Known))
}

// describeKnown renders the registered set, saying so plainly when it is empty.
func describeKnown(known []string) string {
	if len(known) == 0 {
		return "no drivers registered"
	}

	return strings.Join(known, ", ")
}

// Register adds a driver under a name, refusing a duplicate rather than replacing it.
func (r *Registry) Register(name string, build Constructor) error {
	if err := checkName(name); err != nil {
		return err
	}

	if build == nil {
		return fmt.Errorf("driver %q: %w", name, ErrNoConstructor)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, taken := r.ctors[name]; taken {
		return &DuplicateError{Name: name}
	}

	r.ctors[name] = build

	return nil
}

// New builds the driver registered under a name, or reports which names exist.
func (r *Registry) New(name string, opts Options) (Driver, error) {
	r.mu.RLock()
	build, ok := r.ctors[name]
	r.mu.RUnlock()

	if !ok {
		return nil, &UnknownError{Name: name, Known: r.Names()}
	}

	d, err := build(opts)
	if err != nil {
		return nil, fmt.Errorf("driver %q: %w", name, err)
	}

	if d == nil {
		return nil, fmt.Errorf("driver %q: constructor returned no driver", name)
	}

	return d, checkReportedName(name, d.Name())
}

// checkReportedName reports whether a driver answers to the name it was registered under.
func checkReportedName(registered, reported string) error {
	if reported == registered {
		return nil
	}

	return fmt.Errorf("driver %q reports its name as %q; the registry key and the driver must agree",
		registered, reported)
}

// Names returns every registered driver name, sorted, as a copy.
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

// checkName reports whether a driver name is the one canonical spelling of itself.
func checkName(name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("driver name is required")
	}

	if lower := strings.ToLower(name); lower != name {
		return fmt.Errorf("driver name %q must be lowercase (use %q)", name, lower)
	}

	if strings.TrimSpace(name) != name {
		return fmt.Errorf("driver name %q must not be padded with whitespace", name)
	}

	return nil
}
