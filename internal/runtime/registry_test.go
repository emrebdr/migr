package runtime

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// registered is a constructor that hands back one prepared adapter.
func registered(rt Runtime) Constructor {
	return func() (Runtime, error) { return rt, nil }
}

func TestARegistryResolvesWhatWasRegisteredUnderAName(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("fake", registered(&fakeRuntime{})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	rt, err := r.New("fake")
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if rt.Name() != "fake" {
		t.Errorf("the registry built %q", rt.Name())
	}
}

func TestADuplicateIsReportedRatherThanReplacing(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("fake", registered(&fakeRuntime{})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var duplicate *DuplicateError
	if err := r.Register("fake", registered(&fakeRuntime{})); !errors.As(err, &duplicate) {
		t.Errorf("the second Register returned %v, want a duplicate", err)
		t.Log("Registration is explicit rather than init()-based precisely so a")
		t.Log("duplicate name is reportable. Replacing silently is how two builds of one binary")
		t.Log("run different code under the same configuration.")
	}
}

func TestAnUnknownRuntimeNamesWhatThisBuildHas(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("fake", registered(&fakeRuntime{})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	var unknown *UnknownError
	if _, err := r.New("python"); !errors.As(err, &unknown) {
		t.Fatalf("New returned %v, want an unknown runtime", err)
	}

	if !slices.Equal(unknown.Known, []string{"fake"}) {
		t.Errorf("the refusal lists %v", unknown.Known)
	}

	if _, err := NewRegistry().New("go"); !strings.Contains(err.Error(), "no runtimes registered") {
		t.Errorf("an empty registry reported %v", err)
	}
}

func TestARuntimeMustAnswerToTheNameItWasRegisteredUnder(t *testing.T) {
	r := NewRegistry()

	if err := r.Register("go", registered(&fakeRuntime{})); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.New("go"); err == nil {
		t.Error("an adapter registered under one name and reporting another was accepted")
		t.Log("The key is what migr.yaml's migrations.language resolves through and the name is")
		t.Log("what every message renders. Two answers to one question is one too many.")
	}
}

func TestARegistrationIsRefusedWhenItCouldNotBeResolved(t *testing.T) {
	r := NewRegistry()

	for _, c := range []struct {
		name  string
		build Constructor
		want  string
	}{
		{"", registered(&fakeRuntime{}), "required"},
		{"  ", registered(&fakeRuntime{}), "required"},
		{"Go", registered(&fakeRuntime{}), "lowercase"},
		{"fake", nil, "constructor"},
	} {
		if err := r.Register(c.name, c.build); err == nil ||
			!strings.Contains(err.Error(), c.want) {
			t.Errorf("Register(%q) returned %v, want a refusal naming %q", c.name, err, c.want)
		}
	}
}

func TestAConstructorThatFailsIsReportedAgainstItsName(t *testing.T) {
	r := NewRegistry()

	refused := errors.New("no toolchain")

	if err := r.Register("fake", func() (Runtime, error) { return nil, refused }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.New("fake"); !errors.Is(err, refused) {
		t.Errorf("New returned %v, want the constructor's own failure", err)
	}

	if err := r.Register("empty", func() (Runtime, error) { return nil, nil }); err != nil {
		t.Fatalf("Register: %v", err)
	}

	if _, err := r.New("empty"); err == nil {
		t.Error("a constructor that returned nothing was accepted")
	}
}

func TestTheProtocolsOwnBoundsAreWhatAnUnsetTimeoutMeans(t *testing.T) {
	resolved := Timeouts{}.Resolve()

	if resolved != (Timeouts{}.Resolve()) {
		t.Fatal("resolving is not deterministic")
	}

	if resolved.Handshake <= 0 || resolved.Request <= 0 ||
		resolved.CancelGrace <= 0 || resolved.ShutdownGrace <= 0 {
		t.Fatalf("an unset bound resolved to %+v, and none means forever", resolved)
	}

	stated := Timeouts{Handshake: 1, Request: 2, CancelGrace: 3, ShutdownGrace: 4}
	if stated.Resolve() != stated {
		t.Errorf("a stated bound was replaced: %+v", stated.Resolve())
	}
}
