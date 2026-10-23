package migrtest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
)

// Where migr is, and why finding it is a failure rather than a skip.
//
// Nothing in this SDK can compile an operation into SQL -- the drivers are the engine's, and will
// stay there -- so running a migration against a real database means running the real binary. A
// helper that quietly skipped itself when the binary was missing would report a pass it did not
// earn, which migr validate refuses to do too; so this fails and names both rungs.
// Found is exported for a suite that would rather decide for itself.
//
// MIGR_BIN set to something that is not an executable is an error and not a fall-through to PATH:
// whoever set it meant it, and silently running a different binary is how a test suite starts
// measuring something nobody chose.

// BinaryVar is the environment variable naming which migr to run, and the only one migr itself reads.
const BinaryVar = "MIGR_BIN"

// binaryName is what this ladder looks for on PATH, which the exec package suffixes on Windows.
const binaryName = "migr"

// ErrNoBinary marks a machine with no migr to run, which no project helper can work without.
var ErrNoBinary = errors.New("no migr binary")

// Found reports where migr is, or why this machine has none, starting no process to find out.
func Found() (string, error) {
	return found(os.LookupEnv, exec.LookPath)
}

// found walks the two rungs, reading the environment through the lookup it is given.
func found(lookup func(string) (string, bool), path func(string) (string, error)) (string, error) {
	if named, set := lookup(BinaryVar); set {
		return stated(named)
	}

	resolved, err := path(binaryName)
	if err != nil {
		return "", fmt.Errorf("%w: %s names none, and %q is not on PATH (%w)",
			ErrNoBinary, BinaryVar, binaryName, err)
	}

	return resolved, nil
}

// stated reports the binary MIGR_BIN names, which is a failure rather than a fallback when it is wrong.
func stated(named string) (string, error) {
	if named == "" {
		return "", fmt.Errorf("%w: %s is set to nothing", ErrNoBinary, BinaryVar)
	}

	info, err := os.Stat(named)
	if err != nil {
		return "", fmt.Errorf("%w: %s names %q, which is not there (%w)",
			ErrNoBinary, BinaryVar, named, err)
	}

	if info.IsDir() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("%w: %s names %q, which is not an executable",
			ErrNoBinary, BinaryVar, named)
	}

	return named, nil
}
