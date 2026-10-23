package migrtest

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// staged builds a lookup answering one variable, so a case stages the environment without setting it.
func staged(value string, set bool) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if name != BinaryVar {
			return "", false
		}

		return value, set
	}
}

// onPath builds a PATH resolver answering with one binary, or refusing to find any.
func onPath(resolved string) func(string) (string, error) {
	return func(name string) (string, error) {
		if resolved == "" {
			return "", errors.New("executable file not found in $PATH")
		}

		return filepath.Join(resolved, name), nil
	}
}

// executable writes something this ladder will accept as a binary, and returns its path.
func executable(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)

	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("writing a stand-in binary: %v", err)
	}

	return path
}

func TestTheVariableIsPreferredToWhateverIsOnPath(t *testing.T) {
	named := executable(t, "migr")

	found, err := found(staged(named, true), onPath("/usr/local/bin"))
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if found != named {
		t.Errorf("the ladder resolved %q, want the %s it was given", found, BinaryVar)
	}

	t.Log("Whoever set MIGR_BIN meant it. Preferring PATH would silently measure a different")
	t.Log("binary than the one the operator chose, which is how a suite stops meaning anything.")
}

func TestPathIsTheSecondRung(t *testing.T) {
	resolved, err := found(staged("", false), onPath("/usr/local/bin"))
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}

	if resolved != "/usr/local/bin/migr" {
		t.Errorf("the ladder resolved %q, want the migr on PATH", resolved)
	}
}

func TestNeitherRungIsAFailureNamingBoth(t *testing.T) {
	_, err := found(staged("", false), onPath(""))

	if !errors.Is(err, ErrNoBinary) {
		t.Fatalf("the ladder reported %v, want a refusal", err)
	}

	for _, phrase := range []string{BinaryVar, "PATH"} {
		if !strings.Contains(err.Error(), phrase) {
			t.Errorf("the refusal is %q, which does not name %q", err, phrase)
		}
	}

	t.Log("A check that drops itself silently reports a pass it did")
	t.Log("not earn. Found is what a suite calls when it would rather skip deliberately.")
}

func TestAVariableNamingSomethingThatIsNotAnExecutableIsAFailureAndNotAFallback(t *testing.T) {
	for _, c := range []struct {
		name  string
		named func(*testing.T) string
		says  string
	}{
		{"it names nothing at all", func(*testing.T) string { return "" }, "set to nothing"},
		{"it names a path that is not there", func(t *testing.T) string {
			return filepath.Join(t.TempDir(), "absent")
		}, "not there"},
		{"it names a directory", func(t *testing.T) string { return t.TempDir() },
			"not an executable"},
		{"it names a file nothing may run", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "migr")
			if err := os.WriteFile(path, []byte("not a program"), 0o600); err != nil {
				t.Fatalf("writing it: %v", err)
			}

			return path
		}, "not an executable"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := found(staged(c.named(t), true), onPath("/usr/local/bin"))

			if !errors.Is(err, ErrNoBinary) {
				t.Fatalf("the ladder reported %v, want a refusal rather than the PATH rung", err)
			}

			if !strings.Contains(err.Error(), c.says) {
				t.Errorf("the refusal is %q, which does not say %q", err, c.says)
			}
		})
	}
}

func TestFoundReadsTheRealEnvironmentWithoutStartingAnything(t *testing.T) {
	t.Setenv(BinaryVar, executable(t, "migr"))

	resolved, err := Found()
	if err != nil {
		t.Fatalf("Found: %v", err)
	}

	if resolved == "" {
		t.Error("Found resolved nothing while the variable named an executable")
	}
}
