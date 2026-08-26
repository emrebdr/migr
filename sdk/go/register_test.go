package migr

import (
	"errors"
	"strings"
	"testing"
)

func TestRegisteringFromAFileThatIsNotAMigrationIsReported(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}

	Register(plain{})

	_, err := All()
	if !errors.Is(err, ErrMalformedName) {
		t.Fatalf("All returned %v, want %v", err, ErrMalformedName)
	}

	t.Log("This file is not named as a migration, and Register read that from the call site")
	t.Log("rather than taking anyone's word for it. The happy path is proved from a file that")
	t.Log("is named as one, which is the whole of 20260727T153000_register_derives_identity_test.go.")

	if !strings.Contains(err.Error(), "register_test.go") {
		t.Errorf("the problem does not name the file it came from: %v", err)
	}
}

func TestAFileTheGrammarRefusesIsReportedRatherThanLoaded(t *testing.T) {
	staged(t, map[string]Migration{"add_users.go": plain{}})

	_, err := All()
	if !errors.Is(err, ErrMalformedName) {
		t.Fatalf("All returned %v, want %v", err, ErrMalformedName)
	}

	if !strings.Contains(err.Error(), "add_users.go") {
		t.Errorf("the problem does not name the file: %v", err)
	}
}

func TestAVersionThatIsNotATimeIsRefused(t *testing.T) {
	staged(t, map[string]Migration{"20260230T000000_add_users.go": plain{}})

	if _, err := All(); !errors.Is(err, ErrMalformedName) {
		t.Errorf("All returned %v, want %v", err, ErrMalformedName)
		t.Log("A timestamp that cannot be a time is a typo, and a typo in a version is a")
		t.Log("migration that runs in the wrong place.")
	}
}

func TestAFileThatIsNotGoIsRefused(t *testing.T) {
	staged(t, map[string]Migration{"20260727T153000_add_users.sql": plain{}})

	if _, err := All(); !errors.Is(err, ErrMalformedName) {
		t.Errorf("All returned %v, want %v", err, ErrMalformedName)
	}
}

func TestTwoMigrationsInOneFileAreRefusedAsADuplicate(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}
	registered.add(plain{}, file)
	registered.add(reversible{}, file)

	_, err := All()
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("All returned %v, want %v", err, ErrDuplicate)
	}

	t.Log("History joins a file to its row by version, and --to names one. Two migrations under")
	t.Log("one version is a database that cannot say which of them ran.")

	if !strings.Contains(err.Error(), version) {
		t.Errorf("the problem does not name the version: %v", err)
	}
}

func TestAMigrationThatBothAuthorsADownAndDeclaresItHasNoneIsRefused(t *testing.T) {
	staged(t, map[string]Migration{file: contradictory{}})

	if _, err := All(); !errors.Is(err, ErrBothDirections) {
		t.Errorf("All returned %v, want %v", err, ErrBothDirections)
		t.Log("Both cannot be true, and the engine would only ever see the declaration -- the")
		t.Log("Down the author wrote would never run, silently.")
	}
}

func TestADeclarationWithNoReasonIsRefused(t *testing.T) {
	staged(t, map[string]Migration{file: silentlyOneWay{}})

	if _, err := All(); !errors.Is(err, ErrNoReason) {
		t.Errorf("All returned %v, want %v", err, ErrNoReason)
		t.Log("Invariant #10: a Down may declare itself irreversible with a reason. The reason is")
		t.Log("what a rollback halts with, so a declaration without one halts saying nothing.")
	}
}

func TestEveryProblemIsReportedRatherThanTheFirst(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}
	registered.add(plain{}, "add_users.go")
	registered.add(contradictory{}, file)

	_, err := All()

	for _, sentinel := range []error{ErrMalformedName, ErrBothDirections} {
		if !errors.Is(err, sentinel) {
			t.Errorf("All returned %v, which does not carry %v", err, sentinel)
		}
	}

	t.Log("Three complaints about one defect is how a report stops being read, but one")
	t.Log("complaint about three defects is how an author fixes them one build at a time.")
}

func TestARegistrationWithNoMigrationIsReportedRatherThanIgnored(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}
	registered.add(nil, file)

	if _, err := All(); !errors.Is(err, ErrNoMigration) {
		t.Errorf("All returned %v, want %v", err, ErrNoMigration)
	}
}

func TestARegistrationWithNoCallerIsReportedRatherThanGuessed(t *testing.T) {
	previous := registered
	t.Cleanup(func() { registered = previous })

	registered = &registry{loaded: map[string]*Registered{}}
	registered.add(plain{}, "")

	if _, err := All(); !errors.Is(err, ErrNoCaller) {
		t.Errorf("All returned %v, want %v", err, ErrNoCaller)
	}
}

func TestLookupFindsWhatWasRegisteredAndNothingElse(t *testing.T) {
	staged(t, map[string]Migration{file: plain{}})

	if _, ok := Lookup(version); !ok {
		t.Errorf("Lookup(%s) found nothing", version)
	}

	if _, ok := Lookup("20260101T000000"); ok {
		t.Error("Lookup found a migration nothing registered")
	}
}
