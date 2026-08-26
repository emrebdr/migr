package migr

import (
	"cmp"
	"fmt"
	"path/filepath"
	stdruntime "runtime"
	"slices"
	"sync"
)

// callerDepth is how far above Register the migration's own file sits, which is one frame.
const callerDepth = 1

// registry is every migration this program registered, and everything wrong with the set.
type registry struct {
	mu sync.Mutex
	// loaded is each registered migration, keyed by the version its filename carries.
	loaded map[string]*Registered
	// problems are the registrations that could not be accepted, in the order they were made.
	problems []error
}

// registered is the one registry a program has, filled by each migration's init.
var registered = &registry{loaded: map[string]*Registered{}}

// Registered is one migration this SDK loaded, and what its file says about it.
type Registered struct {
	// Version is the UTC timestamp its filename carries, which the engine keys everything by.
	Version string
	// Name is the snake_case name, for a message a human reads.
	Name string
	// File is the filename it was registered from, which migration.discover reports.
	File string
	// Migration is the migration itself.
	Migration Migration
}

// Register adds one migration, reading its version and filename from the file that calls it.
func Register(m Migration) {
	registered.add(m, caller(callerDepth+1))
}

// caller reports the file the registration was made from, which is the migration's own.
func caller(depth int) string {
	_, file, _, ok := stdruntime.Caller(depth)
	if !ok {
		return ""
	}

	return filepath.Base(file)
}

// add records one migration, or the reason it could not be recorded.
func (r *registry) add(m Migration, file string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, err := accept(m, file)
	if err != nil {
		r.problems = append(r.problems, err)

		return
	}

	if taken, ok := r.loaded[entry.Version]; ok {
		r.problems = append(r.problems, fmt.Errorf(
			"%w: %s and %s both register version %s",
			ErrDuplicate, taken.File, entry.File, entry.Version))

		return
	}

	r.loaded[entry.Version] = entry
}

// accept reads what a registration says about itself, refusing everything discovery would refuse.
func accept(m Migration, file string) (*Registered, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: %s registered no migration at all", ErrNoMigration, describeFile(file))
	}

	if file == "" {
		return nil, fmt.Errorf(
			"%w: this build reports no file for the registration of %T, so its version is unknowable",
			ErrNoCaller, m)
	}

	parsed, err := parseName(file)
	if err != nil {
		return nil, err
	}

	if err := checkDirections(m, parsed.file); err != nil {
		return nil, err
	}

	return &Registered{
		Version: parsed.version, Name: parsed.name, File: parsed.file, Migration: m,
	}, nil
}

// checkDirections refuses a migration that both authors an undo and declares it has none.
func checkDirections(m Migration, file string) error {
	_, writes := authored(m)

	reason, states := declared(m)
	if !writes || !states {
		return checkReason(reason, states, file)
	}

	return fmt.Errorf("%w: %s writes a Down and also declares itself irreversible, "+
		"and a migration is one or the other", ErrBothDirections, file)
}

// checkReason refuses a declaration with no reason, which invariant #10 requires be given.
func checkReason(reason string, states bool, file string) error {
	if states && reason == "" {
		return fmt.Errorf("%w: %s declares itself irreversible and gives no reason",
			ErrNoReason, file)
	}

	return nil
}

// describeFile names the file a problem was found in, saying so when this build reports none.
func describeFile(file string) string {
	if file == "" {
		return "an unknown file"
	}

	return file
}

// All returns every migration this program registered, in version order, or what stopped it.
func All() ([]*Registered, error) {
	return registered.all()
}

// all returns the loaded set in version order, reporting every problem rather than the first.
func (r *registry) all() ([]*Registered, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.problems) > 0 {
		return nil, joinProblems(r.problems)
	}

	found := make([]*Registered, 0, len(r.loaded))
	for _, entry := range r.loaded {
		found = append(found, entry)
	}

	slices.SortFunc(found, func(a, b *Registered) int { return cmp.Compare(a.Version, b.Version) })

	return found, nil
}

// Lookup returns the migration registered under one version, and whether there is one.
func Lookup(version string) (*Registered, bool) {
	return registered.lookup(version)
}

// lookup finds one version, which is what migration.execute and migration.validate name.
func (r *registry) lookup(version string) (*Registered, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()

	entry, ok := r.loaded[version]

	return entry, ok
}
