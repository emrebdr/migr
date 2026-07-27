package driver

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

// register adds a fake driver under its own name, failing the test if it cannot.
func register(t *testing.T, r *Registry, name string) *fakeDriver {
	t.Helper()

	d := newFake(name)
	if err := r.Register(name, d.constructor()); err != nil {
		t.Fatalf("Register(%q): %v", name, err)
	}

	return d
}

func TestARegisteredDriverIsResolvedByItsName(t *testing.T) {
	r := NewRegistry()
	want := register(t, r, "postgres")

	got, err := r.New("postgres", Options{Schema: "public"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got != Driver(want) {
		t.Errorf("New returned %#v, want the registered driver", got)
	}
}

func TestRegisteringADuplicateNameIsAnError(t *testing.T) {
	r := NewRegistry()
	register(t, r, "postgres")

	err := r.Register("postgres", newFake("postgres").constructor())

	var duplicate *DuplicateError
	if !errors.As(err, &duplicate) {
		t.Fatalf("registering a duplicate returned %v, want a *DuplicateError", err)
	}

	if duplicate.Name != "postgres" {
		t.Errorf("Name = %q, want %q", duplicate.Name, "postgres")
	}
}

func TestADuplicateRegistrationLeavesTheFirstConstructorInPlace(t *testing.T) {
	r := NewRegistry()
	first := register(t, r, "postgres")

	second := newFake("postgres")
	if err := r.Register("postgres", second.constructor()); err == nil {
		t.Fatal("the second registration was accepted")
	}

	got, err := r.New("postgres", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if got == Driver(second) {
		t.Error("the rejected registration replaced the first one anyway")
		t.Log("Returning an error is not the same as refusing. A registry that reports a")
		t.Log("duplicate and overwrites regardless is worse than one that just overwrites,")
		t.Log("because the error suggests nothing happened.")
	}

	if got != Driver(first) {
		t.Errorf("New returned %#v, want the first registered driver", got)
	}
}

func TestAnUnknownDriverNamesTheOnesThisBuildHas(t *testing.T) {
	r := NewRegistry()
	register(t, r, "postgres")
	register(t, r, "sqlite")

	_, err := r.New("mssql", Options{})

	var unknown *UnknownError
	if !errors.As(err, &unknown) {
		t.Fatalf("New with an unregistered name returned %v, want an *UnknownError", err)
	}

	for _, want := range []string{"mssql", "postgres", "sqlite"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
			t.Log("A misconfigured driver name is a typo most of the time, and the fix is")
			t.Log("visible only if the message lists what this build actually has.")
		}
	}
}

func TestAnEmptyRegistrySaysSoRatherThanListingNothing(t *testing.T) {
	_, err := NewRegistry().New("postgres", Options{})
	if err == nil {
		t.Fatal("an empty registry resolved a name")
	}

	if !strings.Contains(err.Error(), "no drivers registered") {
		t.Errorf("message %q does not say the registry is empty", err)
		t.Log("This is the state for the whole of M2, so it is the message most likely to")
		t.Log("be read first. Without the special case it trails off after \"has \".")
	}
}

func TestRegisteredNamesAreSortedSoOutputIsDeterministic(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"sqlite", "postgres", "mysql", "duckdb", "clickhouse", "oracle"} {
		register(t, r, name)
	}

	want := []string{"clickhouse", "duckdb", "mysql", "oracle", "postgres", "sqlite"}

	for i := range 32 {
		got := r.Names()

		if !slices.IsSorted(got) {
			t.Fatalf("call %d returned %v, which is not sorted", i, got)
		}

		if !reflect.DeepEqual(got, want) {
			t.Fatalf("call %d returned %v, want %v", i, got, want)
		}
	}

	t.Log("Map iteration order is randomized per range, so an unsorted list makes error")
	t.Log("messages and help output differ between runs (invariant #8). Repeating the call")
	t.Log("is what stops one lucky ordering from passing for a sort that never happened.")
}

func TestTheNameSliceIsACopy(t *testing.T) {
	r := NewRegistry()
	register(t, r, "postgres")

	got := r.Names()
	got[0] = "tampered"

	if r.Names()[0] != "postgres" {
		t.Errorf("Names() = %v after a caller wrote to an earlier result", r.Names())
	}
}

func TestARegistryRefusesANamelessOrConstructorlessDriver(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		build Constructor
	}{
		{name: "empty name", key: "", build: newFake("x").constructor()},
		{name: "blank name", key: "   ", build: newFake("x").constructor()},
		{name: "no constructor", key: "postgres", build: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := NewRegistry().Register(tt.key, tt.build); err == nil {
				t.Errorf("Register(%q, %v) was accepted", tt.key, tt.build)
			}
		})
	}
}

func TestADriverNameIsRejectedRatherThanSilentlyLowercased(t *testing.T) {
	r := NewRegistry()

	err := r.Register("PostgreSQL", newFake("PostgreSQL").constructor())
	if err == nil {
		t.Fatal("a mixed-case driver name was accepted")
	}

	if !strings.Contains(err.Error(), "postgresql") {
		t.Errorf("message %q does not offer the canonical spelling", err)
	}

	if len(r.Names()) != 0 {
		t.Errorf("the rejected name was registered anyway: %v", r.Names())
		t.Log("Accepting a second spelling puts two names in the registry for one driver,")
		t.Log("and migr.yaml deliberately has no driver whitelist -- this is the only place")
		t.Log("a driver name is ever checked.")
	}
}

func TestADriverMustReportTheNameItIsRegisteredUnder(t *testing.T) {
	r := NewRegistry()
	mismatched := newFake("postgresql")

	if err := r.Register("postgres", mismatched.constructor()); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := r.New("postgres", Options{})
	if err == nil {
		t.Fatal("a driver answering to a different name was accepted")
	}

	for _, want := range []string{"postgres", "postgresql"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("message %q does not name %q", err, want)
		}
	}

	t.Log("Every refusal a driver produces names itself. If that name can differ from the")
	t.Log("registry key, an operator reads an error about a driver their config never named.")
}

func TestAConstructorFailureNamesTheDriver(t *testing.T) {
	r := NewRegistry()
	boom := errors.New("dsn has no host")

	if err := r.Register("postgres", func(Options) (Driver, error) { return nil, boom }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	_, err := r.New("postgres", Options{})
	if !errors.Is(err, boom) {
		t.Errorf("New returned %v, want it to wrap the constructor's error", err)
	}

	if !strings.Contains(err.Error(), "postgres") {
		t.Errorf("message %q does not name the driver that failed to build", err)
	}
}

func TestAConstructorReturningNothingIsAnErrorNotANilDriver(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("postgres", func(Options) (Driver, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := r.New("postgres", Options{})
	if err == nil {
		t.Log("The caller would get a nil Driver and discover it at the first method call,")
		t.Log("a panic with no indication of which driver was misconfigured.")
		t.Fatal("a constructor returning no driver and no error was accepted")
	}

	if got != nil {
		t.Errorf("New returned %#v alongside an error", got)
	}
}

func TestTwoRegistriesShareNoState(t *testing.T) {
	first, second := NewRegistry(), NewRegistry()
	register(t, first, "postgres")

	if names := second.Names(); len(names) != 0 {
		t.Errorf("a second registry saw %v", names)
	}

	if err := second.Register("postgres", newFake("postgres").constructor()); err != nil {
		t.Errorf("registering the same name in a separate registry failed: %v", err)
		t.Log("Registries are values rather than package state, which is what lets each test")
		t.Log("and the conformance suite run against one they built themselves.")
	}
}

func TestRegistrationIsSafeUnderConcurrentUse(t *testing.T) {
	r := NewRegistry()
	names := []string{"postgres", "mysql", "sqlite", "duckdb", "clickhouse"}

	var wg sync.WaitGroup

	for _, name := range names {
		wg.Add(2)

		go func() {
			defer wg.Done()

			_ = r.Register(name, newFake(name).constructor())
		}()

		go func() {
			defer wg.Done()

			_ = r.Names()
		}()
	}

	wg.Wait()

	if got := len(r.Names()); got != len(names) {
		t.Errorf("registered %d drivers, want %d", got, len(names))
	}
}
